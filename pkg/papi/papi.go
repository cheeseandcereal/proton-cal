// Package papi provides authenticated Proton API requests and a go-proton-api
// Manager for SRP authentication.
//
// On 401, requests adopt or rotate tokens under the cross-process session lock,
// persist both tokens, and retry once. Clients stay bound to their login's UID.
package papi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	proton "github.com/ProtonMail/go-proton-api"

	"github.com/cheeseandcereal/proton-cal/pkg/config"
)

const (
	// AppVersion is sent as x-pm-appversion (verified accepted by the
	// API; see docs/crypto.md).
	AppVersion = "Other"
	// UserAgent is sent on raw requests.
	UserAgent = "proton-cal/0.1"

	maxRateLimitRetries = 3
	maxRetryAfter       = 60 * time.Second
	refreshPath         = "/auth/v4/refresh"
)

// Proton API response codes observed on the calendar endpoints (see
// docs/overview.md "Error codes").
const (
	// CodeSuccess is the per-entry and top-level success code.
	CodeSuccess = 1000
	// CodeSuccessMulti is the top-level multi-status code on batched
	// sync calls (individual entries may still have failed).
	CodeSuccessMulti = 1001
	// CodeNotFound means the requested calendar or event does not exist.
	CodeNotFound = 2501
	// CodeInsufficientScope accompanies 403 responses when the session
	// lacks the "locked" scope (password re-verification required).
	CodeInsufficientScope = 9101
)

// NewManager builds a go-proton-api Manager configured for this app.
func NewManager(baseURL string) *proton.Manager {
	return proton.New(
		proton.WithHostURL(baseURL),
		proton.WithAppVersion(AppVersion),
		proton.WithLogger(quietLogger{}),
	)
}

// quietLogger silences resty's internal retry/error logging (errors still
// surface as returned errors; the retry warnings only confuse users).
type quietLogger struct{}

func (quietLogger) Errorf(string, ...any) {}
func (quietLogger) Warnf(string, ...any)  {}
func (quietLogger) Debugf(string, ...any) {}

// API is the raw-request surface of Client consumed by the calendar and
// event packages; in-memory fakes satisfy it in tests.
type API interface {
	Get(ctx context.Context, path string, query url.Values, out any) error
	Put(ctx context.Context, path string, body, out any) error
	Post(ctx context.Context, path string, body, out any) error
	Delete(ctx context.Context, path string, out any) error
}

// Client makes authenticated requests using a persisted Proton session.
type Client struct {
	m       *proton.Manager
	store   *config.SessionStore
	baseURL string
	httpc   *http.Client

	mu   sync.Mutex
	sess config.Session // cached tokens, bound to one login
}

// New builds a Client for sess, which must already be saved in store.
// Call Close when done.
func New(sess config.Session, store *config.SessionStore, baseURL string) *Client {
	return &Client{
		m:       NewManager(baseURL),
		store:   store,
		baseURL: baseURL,
		httpc:   &http.Client{Timeout: 60 * time.Second},
		sess:    sess,
	}
}

// FromSession restores a Client from the persisted session. The returned
// Client owns its manager; call Close when done.
func FromSession(store *config.SessionStore, baseURL string) (*Client, error) {
	sess, err := store.Load()
	if err != nil {
		return nil, err
	}
	if !sess.Valid() {
		return nil, config.ErrNoSession
	}
	return New(sess, store, baseURL), nil
}

// Close releases the client's manager.
func (c *Client) Close() {
	c.m.Close()
}

// Manager exposes the manager for the SRP scope-elevation handshake.
func (c *Client) Manager() *proton.Manager { return c.m }

// Store exposes the session store the client persists tokens to.
func (c *Client) Store() *config.SessionStore { return c.store }

// SessionUID returns the session's UID (stable across token refreshes;
// rotates on re-login). Used to scope per-session caches.
func (c *Client) SessionUID() (string, error) {
	sess, err := c.session()
	if err != nil {
		return "", err
	}
	return sess.UID, nil
}

// session returns the cached tokens, avoiding a locked disk read per request.
func (c *Client) session() (config.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess.Valid() {
		return c.sess, nil
	}
	return config.Session{}, config.ErrNoSession
}

func (c *Client) setSession(sess config.Session) {
	c.mu.Lock()
	c.sess = sess
	c.mu.Unlock()
}

// Error is a Proton API error. Status is zero for errors inside a sync response.
type Error struct {
	Status  int
	Code    int
	Message string
}

// ErrRefresh marks a failed session refresh, including persistence failures.
var ErrRefresh = errors.New("session refresh failed")

func (e *Error) Error() string {
	return fmt.Sprintf("proton api error: status=%d code=%d: %s", e.Status, e.Code, e.Message)
}

// IsCode reports whether err is a papi/go-proton-api error with the given
// Proton error code.
func IsCode(err error, code int) bool {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code == code
	}
	var ae *proton.APIError
	if errors.As(err, &ae) {
		return int(ae.Code) == code
	}
	return false
}

// Get performs a raw authenticated GET.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	return c.Do(ctx, http.MethodGet, path, query, nil, out)
}

// Put performs a raw authenticated PUT with a JSON body.
func (c *Client) Put(ctx context.Context, path string, body, out any) error {
	return c.Do(ctx, http.MethodPut, path, nil, body, out)
}

// Post performs a raw authenticated POST with a JSON body.
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.Do(ctx, http.MethodPost, path, nil, body, out)
}

// Delete performs a raw authenticated DELETE (no body).
func (c *Client) Delete(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodDelete, path, nil, nil, out)
}

// Do performs an authenticated request. On 401 it refreshes and retries once;
// on 429 it honours Retry-After.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	sess, err := c.session()
	if err != nil {
		return err
	}
	refreshed := false
	for attempt := 0; ; attempt++ {
		status, respBody, err := c.doOnce(ctx, method, path, query, body, sess)
		if err != nil {
			return err
		}

		switch {
		case status == http.StatusUnauthorized && !refreshed:
			refreshed = true
			sess, err = c.store.Refresh(ctx, sess, func(current config.Session) (config.Session, error) {
				return c.rotateTokens(ctx, current)
			})
			if err != nil {
				return fmt.Errorf("%w: %w", ErrRefresh, err)
			}
			c.setSession(sess)
			continue
		case status == http.StatusTooManyRequests && attempt < maxRateLimitRetries:
			wait := retryAfter(respBody.retryAfter)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}

		if status < 200 || status >= 300 {
			err := &Error{Status: status, Code: respBody.envelope.Code, Message: respBody.envelope.Message}
			if status == http.StatusUnauthorized {
				return fmt.Errorf("session unauthorized (run `proton-cal login`): %w", err)
			}
			return err
		}
		if out != nil {
			if err := json.Unmarshal(respBody.raw, out); err != nil {
				return fmt.Errorf("decoding %s %s response: %w", method, path, err)
			}
		}
		return nil
	}
}

// rotateTokens sends the single-use refresh token exactly once.
func (c *Client) rotateTokens(ctx context.Context, sess config.Session) (config.Session, error) {
	if err := ctx.Err(); err != nil {
		return config.Session{}, err
	}
	body := proton.AuthRefreshReq{
		UID:          sess.UID,
		RefreshToken: sess.RefreshToken,
		AccessToken:  sess.AccessToken,
		ResponseType: "token",
		GrantType:    "refresh_token",
		RedirectURI:  "https://protonmail.ch",
		State:        rand.Text(),
	}
	// Once sent, finish the rotation even if the caller cancels. The HTTP
	// timeout bounds the wait; losing the response would lose the shared tokens.
	status, resp, err := c.doOnce(context.WithoutCancel(ctx), http.MethodPost, refreshPath, nil, body, config.Session{})
	if err != nil {
		return config.Session{}, fmt.Errorf("refresh response unavailable (run `proton-cal login`): %w", err)
	}
	if status < 200 || status >= 300 || (resp.envelope.Code != 0 && resp.envelope.Code != CodeSuccess) {
		err := &Error{Status: status, Code: resp.envelope.Code, Message: resp.envelope.Message}
		if status >= 200 && status < 300 {
			return config.Session{}, fmt.Errorf("invalid refresh response (run `proton-cal login`): %w", err)
		}
		if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusUnprocessableEntity || err.Code == int(proton.AuthRefreshTokenInvalid) {
			return config.Session{}, fmt.Errorf("refresh token rejected (run `proton-cal login`): %w", err)
		}
		return config.Session{}, err
	}
	var auth proton.Auth
	if err := json.Unmarshal(resp.raw, &auth); err != nil {
		return config.Session{}, fmt.Errorf("decoding refreshed session (run `proton-cal login`): %w", err)
	}
	if auth.UID == "" {
		auth.UID = sess.UID
	}
	return config.Session{UID: auth.UID, AccessToken: auth.AccessToken, RefreshToken: auth.RefreshToken}, nil
}

type rawResponse struct {
	raw        []byte
	retryAfter string
	envelope   struct {
		Code    int
		Message string `json:"Error"`
	}
}

func (c *Client) doOnce(ctx context.Context, method, path string, query url.Values, body any, sess config.Session) (int, rawResponse, error) {
	var resp rawResponse

	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, resp, err
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reader)
	if err != nil {
		return 0, resp, err
	}
	if sess.UID != "" {
		req.Header.Set("x-pm-uid", sess.UID)
	}
	if sess.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+sess.AccessToken)
	}
	req.Header.Set("x-pm-appversion", AppVersion)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/vnd.protonmail.v1+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	httpc := c.httpc
	if path == refreshPath {
		// Disable redirects and transport body replay for a single-use token.
		req.GetBody = nil
		single := *httpc
		single.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		httpc = &single
	}
	res, err := httpc.Do(req)
	if err != nil {
		return 0, resp, err
	}
	defer func() { _ = res.Body.Close() }()

	resp.raw, err = io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return 0, resp, err
	}
	resp.retryAfter = res.Header.Get("Retry-After")
	_ = json.Unmarshal(resp.raw, &resp.envelope)
	return res.StatusCode, resp, nil
}

// retryAfter parses a Retry-After header (delta-seconds or HTTP-date form),
// clamped to [10s, maxRetryAfter].
func retryAfter(header string) time.Duration {
	const fallback = 10 * time.Second
	if header == "" {
		return fallback
	}
	var d time.Duration
	if secs, err := strconv.Atoi(header); err == nil {
		d = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(header); err == nil {
		d = time.Until(t)
	} else {
		return fallback
	}
	if d < fallback {
		return fallback
	}
	return min(d, maxRetryAfter)
}
