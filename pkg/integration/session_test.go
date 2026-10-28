//go:build integration

package integration

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/papi"
)

// TestSession asserts the stored session works live: setup ran auth.UnlockKeys
// (fetching user and addresses), so a populated Unlocked proves the round-trip.
func TestSession(t *testing.T) {
	s := setup(t)

	if s.unlocked.User.ID == "" {
		t.Error("unlocked.User.ID is empty; GetUser did not return a user")
	}
	if len(s.unlocked.Addresses) == 0 {
		t.Error("no addresses on the account")
	}
	if len(s.unlocked.AddrKRs) < 1 {
		t.Errorf("expected at least 1 unlocked address keyring, got %d", len(s.unlocked.AddrKRs))
	}
}

// Concurrent clients must share one real rotation and persist usable tokens.
func TestConcurrentSessionRefresh(t *testing.T) {
	s := setup(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	before, err := s.store.Load()
	if err != nil {
		t.Fatalf("loading session: %v", err)
	}
	calID := s.resolved[0].ID
	path := calendar.BootstrapPath(calID)
	target, err := url.Parse(s.cfg.EffectiveBaseURL())
	if err != nil {
		t.Fatal(err)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) { r.SetURL(target) },
	}
	var unauthorized, refreshes atomic.Int32
	bothUnauthorized := make(chan struct{})
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == path:
			if r.Header.Get("Authorization") == "Bearer "+before.AccessToken {
				if unauthorized.Add(1) == 2 {
					close(bothUnauthorized)
				}
				// Both clients must use the initial tokens before either refreshes.
				select {
				case <-bothUnauthorized:
				case <-r.Context().Done():
					return
				}
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		case r.Method == http.MethodPost && r.URL.Path == "/auth/v4/refresh":
			if refreshes.Add(1) != 1 {
				// Detect duplicate attempts without spending the token twice live.
				http.Error(w, "duplicate refresh attempt", http.StatusConflict)
				return
			}
		default:
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer front.Close()

	g, requestCtx := errgroup.WithContext(ctx)
	for range 2 {
		client := papi.New(before, s.store, front.URL)
		t.Cleanup(client.Close)
		g.Go(func() error {
			_, err := calendar.FetchSettings(requestCtx, client, calID)
			return err
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("concurrent calendar reads: %v", err)
	}
	if got := unauthorized.Load(); got != 2 {
		t.Fatalf("initial unauthorized responses = %d, want 2", got)
	}

	after, err := s.store.Load()
	if err != nil {
		t.Fatalf("loading rotated session: %v", err)
	}
	if !after.Valid() || after.AccessToken == before.AccessToken || after.RefreshToken == before.RefreshToken {
		t.Fatal("both rotated tokens must be saved")
	}
	if after.UID != before.UID || !bytes.Equal(after.SaltedKeyPass, before.SaltedKeyPass) {
		t.Fatal("refresh changed the session UID or key passphrase")
	}
	restored, err := papi.FromSession(s.store, front.URL)
	if err != nil {
		t.Fatalf("restoring rotated session: %v", err)
	}
	defer restored.Close()
	if _, err := calendar.FetchSettings(ctx, restored, calID); err != nil {
		t.Fatalf("calendar read with saved tokens: %v", err)
	}
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("refresh requests = %d, want 1", got)
	}
}
