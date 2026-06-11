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
	"time"

	proton "github.com/ProtonMail/go-proton-api"

	"github.com/cheeseandcereal/proton-cal-go/internal/config"
)

const (
	// AppVersion is sent as x-pm-appversion (verified accepted by the
	// API; see RESEARCH.md).
	AppVersion = "Other"
	// UserAgent is sent on raw requests.
	UserAgent = "proton-cal-go/0.1"

	maxRateLimitRetries = 3
	maxRetryAfter       = 60 * time.Second
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

// Client couples a go-proton-api client with the session store and a raw
// HTTP path for endpoints go-proton-api lacks.
type Client struct {
	pc      *proton.Client
	store   *config.SessionStore
	baseURL string
	httpc   *http.Client
}

// New wires a Client. The given proton.Client must already have an auth
// handler persisting refreshed tokens to store (see FromSession / auth pkg).
func New(pc *proton.Client, store *config.SessionStore, baseURL string) *Client {
	return &Client{
		pc:      pc,
		store:   store,
		baseURL: baseURL,
		httpc:   &http.Client{Timeout: 60 * time.Second},
	}
}

// FromSession restores a Client from the persisted session. It registers the
// auth/deauth handlers on the underlying go-proton-api client.
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
	RegisterPersistence(pc, store)
	return New(pc, store, baseURL), nil
}

// RegisterPersistence wires token-refresh persistence and deauth cleanup.
func RegisterPersistence(pc *proton.Client, store *config.SessionStore) {
	pc.AddAuthHandler(func(auth proton.Auth) {
		_ = store.UpdateTokens(auth.UID, auth.AccessToken, auth.RefreshToken)
	})
	pc.AddDeauthHandler(func() {
		_ = store.Clear()
	})
}

// Proton exposes the typed go-proton-api client.
func (c *Client) Proton() *proton.Client { return c.pc }

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

// Do performs a raw authenticated request. On 401 it triggers the typed
// client's token refresh (once) and retries; on 429 it honours Retry-After.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	refreshed := false
	for attempt := 0; ; attempt++ {
		status, respBody, err := c.doOnce(ctx, method, path, query, body)
		if err != nil {
			return err
		}

		switch {
		case status == http.StatusUnauthorized && !refreshed:
			// Let the typed client refresh; its auth handler persists the
			// rotated tokens, which doOnce re-reads from the store.
			if _, err := c.pc.GetUser(ctx); err != nil {
				return fmt.Errorf("session refresh failed: %w", err)
			}
			refreshed = true
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

func (c *Client) doOnce(ctx context.Context, method, path string, query url.Values, body any) (int, rawResponse, error) {
	var resp rawResponse

	sess, err := c.store.Load()
	if err != nil {
		return 0, resp, err
	}

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

func retryAfter(header string) time.Duration {
	if header == "" {
		return 10 * time.Second
	}
	secs, err := strconv.Atoi(header)
	if err != nil || secs < 1 {
		return 10 * time.Second
	}
	if d := time.Duration(secs) * time.Second; d < maxRetryAfter {
		return d
	}
	return maxRetryAfter
}
