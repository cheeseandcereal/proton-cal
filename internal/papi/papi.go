// Package papi wraps go-proton-api's Manager/Client and adds a raw request
// path for the calendar endpoints go-proton-api does not implement (most
// importantly PUT /calendar/v1/{id}/events/sync, and GET /calendar/v1 whose
// response shape go-proton-api's types no longer match).
//
// Token refresh strategy: only go-proton-api's Client ever refreshes tokens
// (it auto-refreshes on 401 and notifies the registered auth handler, which
// persists the rotated tokens to the session store). The raw path NEVER
// refreshes on its own - on a 401 it makes a cheap typed call to trigger the
// client's refresh, re-reads the persisted tokens, and retries once. This
// avoids refresh-token rotation races.
package papi

import (
	"bytes"
	"context"
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

	"github.com/cheeseandcereal/proton-cal/internal/config"
)

const (
	// AppVersion is sent as x-pm-appversion (verified accepted by the
	// API; see docs/crypto.md).
	AppVersion = "Other"
	// UserAgent is sent on raw requests.
	UserAgent = "proton-cal/0.1"

	maxRateLimitRetries = 3
	maxRetryAfter       = 60 * time.Second
)

// Proton API response codes observed on the calendar endpoints (see
// docs/overview.md "Error codes").
const (
	// CodeSuccess is the per-entry and top-level success code.
	CodeSuccess = 1000
	// CodeSuccessMulti is the top-level multi-status code on batched
	// sync calls (individual entries may still have failed).
	CodeSuccessMulti = 1001
	// CodeInsufficientScope accompanies 403 responses when the session
	// lacks the "locked" scope (password re-verification required).
	CodeInsufficientScope = 9101
	// CodeHumanVerification accompanies 422 responses requiring a
	// human-verification (captcha) round-trip.
	CodeHumanVerification = 9001
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

// Client couples a go-proton-api client with the session store and a raw
// HTTP path for endpoints go-proton-api lacks.
type Client struct {
	pc      *proton.Client
	m       *proton.Manager // owned manager (FromSession); nil otherwise
	store   *config.SessionStore
	baseURL string
	httpc   *http.Client

	mu   sync.Mutex
	sess config.Session // cached tokens; zero until first use
}

// New wires a Client over an existing proton.Client, registering auth/deauth
// handlers that persist token rotations. Callers must not register their own.
func New(pc *proton.Client, store *config.SessionStore, baseURL string) *Client {
	c := &Client{
		pc:      pc,
		store:   store,
		baseURL: baseURL,
		httpc:   &http.Client{Timeout: 60 * time.Second},
	}
	pc.AddAuthHandler(func(auth proton.Auth) {
		_ = store.UpdateTokens(auth.UID, auth.AccessToken, auth.RefreshToken)
		c.setSession(config.Session{UID: auth.UID, AccessToken: auth.AccessToken, RefreshToken: auth.RefreshToken})
	})
	pc.AddDeauthHandler(func() {
		_ = store.Clear()
		c.setSession(config.Session{})
	})
	return c
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
	m := NewManager(baseURL)
	pc := m.NewClient(sess.UID, sess.AccessToken, sess.RefreshToken)
	c := New(pc, store, baseURL)
	c.m = m
	c.setSession(sess) // seed the token cache; saves a locked disk read
	return c, nil
}

// Close releases the underlying go-proton-api client (and the manager when
// the Client owns one, i.e. it came from FromSession).
func (c *Client) Close() {
	c.pc.Close()
	if c.m != nil {
		c.m.Close()
	}
}

// Proton exposes the typed go-proton-api client.
func (c *Client) Proton() *proton.Client { return c.pc }

// Manager exposes the owned go-proton-api Manager (nil unless built via
// FromSession). Needed for the SRP scope-elevation handshake (Manager.AuthInfo).
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

// session returns the cached tokens, loading from the store on first use; the
// cache avoids a locked disk read per request (rotations update it via the handler).
func (c *Client) session() (config.Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess.Valid() {
		return c.sess, nil
	}
	sess, err := c.store.Load()
	if err != nil {
		return config.Session{}, err
	}
	c.sess = sess
	return sess, nil
}

func (c *Client) setSession(sess config.Session) {
	c.mu.Lock()
	c.sess = sess
	c.mu.Unlock()
}

// Error is a Proton API error from the raw request path.
type Error struct {
	Status  int
	Code    int
	Message string
}

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

// Do performs a raw authenticated request. On 401 it re-reads the persisted
// session, else triggers the typed client's refresh (once) and retries; on 429
// it honours Retry-After.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	refreshed := false
	for attempt := 0; ; attempt++ {
		sess, err := c.session()
		if err != nil {
			return err
		}
		status, respBody, err := c.doOnce(ctx, method, path, query, body, sess)
		if err != nil {
			return err
		}

		switch {
		case status == http.StatusUnauthorized && !refreshed:
			refreshed = true
			// Another process may have rotated tokens; prefer the persisted
			// session when it differs from what we just used.
			if disk, derr := c.store.Load(); derr == nil && disk.Valid() && disk.AccessToken != sess.AccessToken {
				c.setSession(disk)
				continue
			}
			// Let the typed client refresh; its auth handler updates both
			// the store and the in-memory cache.
			if _, err := c.pc.GetUser(ctx); err != nil {
				return fmt.Errorf("session refresh failed: %w", err)
			}
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
			return &Error{Status: status, Code: respBody.envelope.Code, Message: respBody.envelope.Message}
		}
		if out != nil {
			if err := json.Unmarshal(respBody.raw, out); err != nil {
				return fmt.Errorf("decoding %s %s response: %w", method, path, err)
			}
		}
		return nil
	}
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
	req.Header.Set("x-pm-uid", sess.UID)
	req.Header.Set("Authorization", "Bearer "+sess.AccessToken)
	req.Header.Set("x-pm-appversion", AppVersion)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Accept", "application/vnd.protonmail.v1+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.httpc.Do(req)
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
