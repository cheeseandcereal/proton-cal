package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	proton "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/go-proton-api/server"

	"github.com/cheeseandcereal/proton-cal/pkg/config"
	"github.com/cheeseandcereal/proton-cal/pkg/papi"
)

func TestLoginRefreshesRequests(t *testing.T) {
	for _, path := range []string{"/auth/v4/2fa", "/core/v4/keys/salts", UsersPath, AddressesPath} {
		t.Run(path, func(t *testing.T) {
			t.Setenv(config.EnvConfigDir, t.TempDir())
			backend := server.New(server.WithTLS(false))
			t.Cleanup(backend.Close)
			if _, _, err := backend.CreateUser(testUser, []byte(testPass)); err != nil {
				t.Fatal(err)
			}
			target, err := url.Parse(backend.GetHostURL())
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(target)
			if path == "/auth/v4/2fa" {
				proxy.ModifyResponse = func(r *http.Response) error {
					if r.Request.URL.Path != "/auth/v4" {
						return nil
					}
					var a proton.Auth
					err := json.NewDecoder(r.Body).Decode(&a)
					_ = r.Body.Close()
					if err != nil {
						return err
					}
					a.TwoFA.Enabled = proton.HasTOTP
					data, err := json.Marshal(a)
					if err != nil {
						return err
					}
					r.Body = io.NopCloser(bytes.NewReader(data))
					r.ContentLength = int64(len(data))
					r.Header.Set("Content-Length", strconv.Itoa(len(data)))
					return nil
				}
			}
			var expired atomic.Bool
			var refreshes atomic.Int32
			front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == path && expired.CompareAndSwap(false, true) {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if r.URL.Path == "/auth/v4/2fa" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"Code":1000}`)
					return
				}
				if r.URL.Path == "/auth/v4/refresh" {
					refreshes.Add(1)
				}
				proxy.ServeHTTP(w, r)
			}))
			t.Cleanup(front.Close)
			prompter := &scriptPrompter{t: t, answers: map[string]string{"Password": testPass, "2FA code": "123456"}}
			cfg := config.Config{Username: testUser, BaseURL: front.URL}
			if _, err := Login(t.Context(), prompter, cfg); err != nil {
				t.Fatal(err)
			}
			if !expired.Load() || refreshes.Load() != 1 {
				t.Fatalf("expired=%v, refreshes=%d", expired.Load(), refreshes.Load())
			}
			store, err := config.NewSessionStore()
			if err != nil {
				t.Fatal(err)
			}
			sess, err := store.Load()
			if err != nil || !sess.Valid() || len(sess.SaltedKeyPass) == 0 {
				t.Fatalf("incomplete login: %+v, %v", sess, err)
			}
		})
	}
}

func TestScopeCleanupReturnsRefreshFailure(t *testing.T) {
	for _, operationErr := range []error{nil, errors.New("operation failed")} {
		t.Run("operationFailed="+strconv.FormatBool(operationErr != nil), func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv(config.EnvConfigDir, dir)
			backend := server.New(server.WithTLS(false))
			t.Cleanup(backend.Close)
			if _, _, err := backend.CreateUser(testUser, []byte(testPass)); err != nil {
				t.Fatal(err)
			}
			target, err := url.Parse(backend.GetHostURL())
			if err != nil {
				t.Fatal(err)
			}
			proxy := httputil.NewSingleHostReverseProxy(target)
			front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/core/v4/users/unlock":
					var proof map[string]any
					if err := json.NewDecoder(r.Body).Decode(&proof); err != nil {
						t.Error(err)
						return
					}
					proof["Username"] = testUser
					data, err := json.Marshal(proof)
					if err != nil {
						t.Error(err)
						return
					}
					r.URL.Path, r.Method = "/auth/v4", http.MethodPost
					r.Body = io.NopCloser(bytes.NewReader(data))
					r.ContentLength = int64(len(data))
					proxy.ServeHTTP(w, r)
				case "/core/v4/users/lock":
					w.WriteHeader(http.StatusUnauthorized)
				case "/auth/v4/refresh":
					path := filepath.Join(dir, "session.json")
					if err := os.Remove(path); err != nil {
						t.Error(err)
					}
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Error(err)
					}
					_, _ = io.WriteString(w, `{"Code":1000,"AccessToken":"a1","RefreshToken":"r1"}`)
				default:
					proxy.ServeHTTP(w, r)
				}
			}))
			t.Cleanup(front.Close)
			store, err := config.NewSessionStore()
			if err != nil {
				t.Fatal(err)
			}
			sess := config.Session{UID: "uid", AccessToken: "a0", RefreshToken: "r0"}
			if err := store.Save(sess); err != nil {
				t.Fatal(err)
			}
			client := papi.New(sess, store, front.URL)
			defer client.Close()
			err = WithLockedScope(t.Context(), client.Manager(), client, testUser, testPass, func() error { return operationErr })
			if !errors.Is(err, papi.ErrRefresh) || !strings.Contains(err.Error(), "saving refreshed session") || (operationErr != nil && !errors.Is(err, operationErr)) {
				t.Fatalf("scope cleanup error = %v", err)
			}
		})
	}
}

func TestLogoutPreservesConcurrentLogin(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	store, err := config.NewSessionStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(config.Session{UID: "logout", AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatal(err)
	}
	replacement := config.Session{UID: "login", AccessToken: "new-a", RefreshToken: "new-r", SaltedKeyPass: []byte("new-key")}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/auth/v4" || r.Header.Get("x-pm-uid") != "logout" {
			t.Errorf("unexpected logout request: %s %s, UID %q", r.Method, r.URL.Path, r.Header.Get("x-pm-uid"))
		}
		if err := store.Save(replacement); err != nil {
			t.Error(err)
		}
		_, _ = io.WriteString(w, `{"Code":1000}`)
	}))
	defer api.Close()
	if err := config.Save(config.Config{BaseURL: api.URL}); err != nil {
		t.Fatal(err)
	}
	if err := Logout(t.Context()); err != nil {
		t.Fatal(err)
	}
	sess, err := store.Load()
	if err != nil || !reflect.DeepEqual(sess, replacement) {
		t.Fatalf("concurrent login changed: %+v, %v", sess, err)
	}
}

func TestLogoutClearsLocalStateBeforeRevocation(t *testing.T) {
	for _, status := range []int{0, http.StatusOK, http.StatusUnauthorized, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv(config.EnvConfigDir, dir)
			store, err := config.NewSessionStore()
			if err != nil {
				t.Fatal(err)
			}
			if status != 0 {
				if err := store.Save(config.Session{UID: "uid", AccessToken: "a0", RefreshToken: "r0"}); err != nil {
					t.Fatal(err)
				}
			}
			cache, err := config.OpenCache("uid")
			if err != nil {
				t.Fatal(err)
			}
			cache.Put(UsersPath, json.RawMessage(`{"User":{}}`))
			assertCleared := func() {
				t.Helper()
				if _, err := store.Load(); !errors.Is(err, config.ErrNoSession) {
					t.Errorf("session was not cleared: %v", err)
				}
				if _, err := os.Stat(filepath.Join(dir, "cache.json")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("cache was not cleared: %v", err)
				}
			}
			var requests atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				assertCleared()
				if status == 0 || r.Method != http.MethodDelete || r.URL.Path != "/auth/v4" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(status)
			}))
			defer api.Close()
			if err := config.Save(config.Config{BaseURL: api.URL}); err != nil {
				t.Fatal(err)
			}
			if err := Logout(t.Context()); err != nil {
				t.Fatal(err)
			}
			assertCleared()
			if (status == 0 && requests.Load() != 0) || (status != 0 && requests.Load() != 1) {
				t.Errorf("revocation requests = %d", requests.Load())
			}
		})
	}
}
