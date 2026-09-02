package papi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal/pkg/config"
)

const (
	testUID        = "uid-1"
	testOldAccess  = "access-old"
	testOldRefresh = "refresh-old"
	testNewAccess  = "access-new"
	testNewRefresh = "refresh-new"
)

func writeAPIJSON(t *testing.T, w http.ResponseWriter, status int, body any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Errorf("encoding response: %v", err)
	}
}

func newStoredTestSession(t *testing.T) *config.SessionStore {
	t.Helper()
	store, err := config.NewSessionStore()
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	if err := store.Save(config.Session{
		UID:           testUID,
		AccessToken:   testOldAccess,
		RefreshToken:  testOldRefresh,
		SaltedKeyPass: []byte("key-pass"),
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return store
}

func TestConcurrentUnauthorizedRequestsShareOneRefresh(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	store := newStoredTestSession(t)

	var oldRequests atomic.Int32
	var refreshRequests atomic.Int32
	bothOldRequests := make(chan struct{})
	var releaseOldRequests sync.Once

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/calendar/v1":
			switch r.Header.Get("Authorization") {
			case "Bearer " + testOldAccess:
				if oldRequests.Add(1) == 2 {
					releaseOldRequests.Do(func() { close(bothOldRequests) })
				}
				select {
				case <-bothOldRequests:
				case <-r.Context().Done():
					return
				}
				writeAPIJSON(t, w, http.StatusUnauthorized, map[string]any{"Code": 10013, "Error": "expired"})
			case "Bearer " + testNewAccess:
				writeAPIJSON(t, w, http.StatusOK, map[string]any{"Code": 1000, "Calendars": []any{}})
			default:
				writeAPIJSON(t, w, http.StatusUnauthorized, map[string]any{"Code": 10013, "Error": "unknown token"})
			}
		case "/core/v4/users":
			if r.Header.Get("Authorization") == "Bearer "+testNewAccess {
				writeAPIJSON(t, w, http.StatusOK, map[string]any{"Code": 1000, "User": map[string]any{"ID": "user-1"}})
				return
			}
			writeAPIJSON(t, w, http.StatusUnauthorized, map[string]any{"Code": 10013, "Error": "expired"})
		case "/auth/v4/refresh":
			refreshRequests.Add(1)
			writeAPIJSON(t, w, http.StatusOK, map[string]any{
				"Code":         1000,
				"UID":          testUID,
				"AccessToken":  testNewAccess,
				"RefreshToken": testNewRefresh,
			})
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	c1, err := FromSession(store, srv.URL)
	if err != nil {
		t.Fatalf("FromSession(c1): %v", err)
	}
	defer c1.Close()
	c2, err := FromSession(store, srv.URL)
	if err != nil {
		t.Fatalf("FromSession(c2): %v", err)
	}
	defer c2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errs := make(chan error, 2)
	for _, client := range []*Client{c1, c2} {
		go func() {
			var out map[string]any
			errs <- client.Get(ctx, "/calendar/v1", nil, &out)
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("Get after transparent refresh: %v", err)
		}
	}

	if got := refreshRequests.Load(); got != 1 {
		t.Fatalf("refresh requests = %d, want 1", got)
	}
	sess, err := store.Load()
	if err != nil {
		t.Fatalf("Load refreshed session: %v", err)
	}
	if sess.AccessToken != testNewAccess || sess.RefreshToken != testNewRefresh {
		t.Fatalf("saved tokens = access %q refresh %q", sess.AccessToken, sess.RefreshToken)
	}
	if string(sess.SaltedKeyPass) != "key-pass" {
		t.Fatalf("refresh dropped salted key passphrase: %q", sess.SaltedKeyPass)
	}
}

func TestRejectedRefreshReturnsLoginGuidance(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	store := newStoredTestSession(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/calendar/v1", "/core/v4/users":
			writeAPIJSON(t, w, http.StatusUnauthorized, map[string]any{"Code": 10013, "Error": "expired"})
		case "/auth/v4/refresh":
			writeAPIJSON(t, w, http.StatusUnprocessableEntity, map[string]any{
				"Code":  10013,
				"Error": "invalid refresh token",
			})
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client, err := FromSession(store, srv.URL)
	if err != nil {
		t.Fatalf("FromSession: %v", err)
	}
	defer client.Close()

	err = client.Get(context.Background(), "/calendar/v1", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "run `proton-cal login`") {
		t.Fatalf("Get error = %v, want login guidance", err)
	}
	if _, err := store.Load(); !errors.Is(err, config.ErrNoSession) {
		t.Fatalf("Load after rejected refresh = %v, want ErrNoSession", err)
	}
}

func TestStaleManagedClientDoesNotClearRefreshedSession(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	store := newStoredTestSession(t)

	var refreshRequests atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/calendar/v1":
			if r.Header.Get("Authorization") == "Bearer "+testNewAccess {
				writeAPIJSON(t, w, http.StatusOK, map[string]any{"Code": 1000})
				return
			}
			writeAPIJSON(t, w, http.StatusUnauthorized, map[string]any{"Code": 10013, "Error": "expired"})
		case "/core/v4/users":
			if r.Header.Get("Authorization") == "Bearer "+testNewAccess {
				writeAPIJSON(t, w, http.StatusOK, map[string]any{"Code": 1000, "User": map[string]any{"ID": "user-1"}})
				return
			}
			writeAPIJSON(t, w, http.StatusUnauthorized, map[string]any{"Code": 10013, "Error": "expired"})
		case "/auth/v4/refresh":
			if refreshRequests.Add(1) == 1 {
				writeAPIJSON(t, w, http.StatusOK, map[string]any{
					"Code":         1000,
					"UID":          testUID,
					"AccessToken":  testNewAccess,
					"RefreshToken": testNewRefresh,
				})
				return
			}
			writeAPIJSON(t, w, http.StatusUnprocessableEntity, map[string]any{
				"Code":  10013,
				"Error": "refresh token already consumed",
			})
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client, err := FromSession(store, srv.URL)
	if err != nil {
		t.Fatalf("FromSession: %v", err)
	}
	defer client.Close()

	if err := client.Get(context.Background(), "/calendar/v1", nil, nil); err != nil {
		t.Fatalf("raw Get after refresh: %v", err)
	}
	if _, err := client.pc.GetUser(context.Background()); err == nil {
		t.Fatal("stale managed client unexpectedly refreshed a second time")
	}

	sess, err := store.Load()
	if err != nil {
		t.Fatalf("Load after stale managed client deauth: %v", err)
	}
	if sess.AccessToken != testNewAccess || sess.RefreshToken != testNewRefresh {
		t.Fatalf("stale managed client changed refreshed tokens: %+v", sess)
	}
}
