package papi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	proton "github.com/ProtonMail/go-proton-api"

	"github.com/cheeseandcereal/proton-cal/pkg/config"
)

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *config.SessionStore) {
	t.Helper()
	t.Setenv(config.EnvConfigDir, t.TempDir())
	store, err := config.NewSessionStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(config.Session{UID: "uid", AccessToken: "a0", RefreshToken: "r0", SaltedKeyPass: []byte("key")}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := FromSession(store, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client, store
}

func writeRotation(w http.ResponseWriter) {
	_, _ = io.WriteString(w, `{"Code":1000,"UID":"uid","AccessToken":"a1","RefreshToken":"r1"}`)
}

func TestRefreshReplaysRequest(t *testing.T) {
	var requests []string
	var store *config.SessionStore
	client, store := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		if r.URL.Path == "/auth/v4/refresh" {
			var req proton.AuthRefreshReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.UID != "uid" || req.AccessToken != "a0" || req.RefreshToken != "r0" || req.ResponseType != "token" || req.GrantType != "refresh_token" || req.RedirectURI != "https://protonmail.ch" || req.State == "" {
				t.Errorf("refresh request = %+v", req)
			}
			writeRotation(w)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != `{"event":"original"}` || r.Header.Get("x-pm-uid") != "uid" {
			t.Errorf("request body = %s, UID = %q, error = %v", body, r.Header.Get("x-pm-uid"), err)
		}
		if r.Header.Get("Authorization") == "Bearer a0" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer a1" {
			t.Errorf("retry Authorization = %q", r.Header.Get("Authorization"))
		}
		sess, err := store.Load()
		if err != nil || sess.AccessToken != "a1" || sess.RefreshToken != "r1" || string(sess.SaltedKeyPass) != "key" {
			t.Errorf("session before retry = %+v, %v", sess, err)
		}
		_, _ = io.WriteString(w, `{"result":"ok"}`)
	})
	var out struct{ Result string }
	err := client.Do(t.Context(), http.MethodPut, "/calendar/v1/test/events/sync", url.Values{"key": {"value"}}, map[string]string{"event": "original"}, &out)
	if err != nil || out.Result != "ok" {
		t.Fatalf("Do = %+v, %v", out, err)
	}
	want := []string{"PUT /calendar/v1/test/events/sync?key=value", "POST /auth/v4/refresh", "PUT /calendar/v1/test/events/sync?key=value"}
	if !reflect.DeepEqual(requests, want) {
		t.Errorf("requests = %v, want %v", requests, want)
	}
}

func TestConcurrentRefresh(t *testing.T) {
	for _, mode := range []string{"shared client", "separate clients", "separate processes"} {
		t.Run(mode, func(t *testing.T) {
			const workers = 4
			var initial, refreshes atomic.Int32
			ready := make(chan struct{})
			client, store := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth/v4/refresh" {
					if refreshes.Add(1) != 1 {
						w.WriteHeader(http.StatusUnprocessableEntity)
						return
					}
					writeRotation(w)
					return
				}
				if r.Header.Get("Authorization") == "Bearer a0" {
					if initial.Add(1) == workers {
						close(ready)
					}
					select {
					case <-ready:
					case <-r.Context().Done():
					}
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if r.Header.Get("Authorization") != "Bearer a1" {
					t.Errorf("unexpected Authorization: %q", r.Header.Get("Authorization"))
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			errs := make(chan error, workers)
			for range workers {
				c := client
				if mode == "separate clients" {
					var err error
					c, err = FromSession(store, client.baseURL)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(c.Close)
				}
				go func() {
					if mode == "separate processes" {
						cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRefreshProcess$")
						cmd.Env = append(os.Environ(), "PROTON_CAL_REFRESH_TEST_URL="+client.baseURL)
						out, err := cmd.CombinedOutput()
						if err != nil {
							err = fmt.Errorf("child process: %w: %s", err, out)
						}
						errs <- err
						return
					}
					errs <- c.Get(ctx, "/resource", nil, nil)
				}()
			}
			for range workers {
				if err := <-errs; err != nil {
					t.Error(err)
				}
			}
			if refreshes.Load() != 1 {
				t.Errorf("refresh calls = %d, want 1", refreshes.Load())
			}
		})
	}
}

func TestRefreshProcess(t *testing.T) {
	baseURL := os.Getenv("PROTON_CAL_REFRESH_TEST_URL")
	if baseURL == "" {
		t.Skip("subprocess helper")
	}
	store, err := config.NewSessionStore()
	if err != nil {
		t.Fatal(err)
	}
	client, err := FromSession(store, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Get(t.Context(), "/resource", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshAfterAdoptingTokens(t *testing.T) {
	var generation atomic.Int32
	var expired atomic.Bool
	expired.Store(true)
	first, store := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gen := generation.Load()
		if r.URL.Path == "/auth/v4/refresh" {
			var req proton.AuthRefreshReq
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			if req.RefreshToken != fmt.Sprintf("r%d", gen) {
				t.Errorf("reused refresh token: %q", req.RefreshToken)
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			gen = generation.Add(1)
			expired.Store(false)
			_, _ = fmt.Fprintf(w, `{"Code":1000,"AccessToken":"a%d","RefreshToken":"r%d"}`, gen, gen)
			return
		}
		if expired.Load() || r.Header.Get("Authorization") != fmt.Sprintf("Bearer a%d", gen) {
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	second, err := FromSession(store, first.baseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for i, client := range []*Client{first, second, second, first} {
		if i == 2 {
			expired.Store(true)
		}
		if err := client.Get(t.Context(), "/resource", nil, nil); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if generation.Load() != 2 {
		t.Errorf("refresh calls = %d, want 2", generation.Load())
	}
}

func TestStaleClientCannotChangeSession(t *testing.T) {
	for _, loggedOut := range []bool{false, true} {
		t.Run(fmt.Sprintf("loggedOut=%v", loggedOut), func(t *testing.T) {
			var requests atomic.Int32
			client, store := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/resource" {
					t.Errorf("stale client requested %s", r.URL.Path)
				}
				w.WriteHeader(http.StatusUnauthorized)
			})
			replacement := config.Session{UID: "another-login", AccessToken: "other-a", RefreshToken: "other-r", SaltedKeyPass: []byte("other-key")}
			var err error
			if loggedOut {
				err = store.Clear("uid")
			} else {
				err = store.Save(replacement)
			}
			if err != nil {
				t.Fatal(err)
			}
			err = client.Get(t.Context(), "/resource", nil, nil)
			wantErr := config.ErrSessionChanged
			if loggedOut {
				wantErr = config.ErrNoSession
			}
			if !errors.Is(err, wantErr) || requests.Load() != 1 {
				t.Fatalf("stale request = %v, requests = %d", err, requests.Load())
			}
			disk, err := store.Load()
			if loggedOut {
				if !errors.Is(err, config.ErrNoSession) {
					t.Fatalf("logged-out session recreated: %+v, %v", disk, err)
				}
			} else if err != nil || !reflect.DeepEqual(disk, replacement) {
				t.Fatalf("replacement session changed: %+v, %v", disk, err)
			}
		})
	}
}

func TestRefreshFailure(t *testing.T) {
	for _, status := range []int{400, 401, 403, 409, 422, 429, 500, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var refreshes, requests atomic.Int32
			client, store := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth/v4/refresh" {
					refreshes.Add(1)
					w.WriteHeader(status)
					_, _ = io.WriteString(w, `{"Code":2000,"Error":"refresh failed"}`)
					return
				}
				requests.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
			})
			err := client.Get(t.Context(), "/resource", nil, nil)
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Status != status {
				t.Fatalf("error = %v, want HTTP %d", err, status)
			}
			rejected := status == 400 || status == 401 || status == 403 || status == 422
			if strings.Contains(err.Error(), "proton-cal login") != rejected {
				t.Errorf("re-login guidance for rejected=%v: %v", rejected, err)
			}
			if requests.Load() != 1 || refreshes.Load() != 1 {
				t.Errorf("requests = %d, refreshes = %d", requests.Load(), refreshes.Load())
			}
			disk, err := store.Load()
			if err != nil || disk.AccessToken != "a0" || disk.RefreshToken != "r0" || string(disk.SaltedKeyPass) != "key" {
				t.Fatalf("session changed on refresh failure: %+v, %v", disk, err)
			}
		})
	}
}

func TestCancelledCallerPreservesRotation(t *testing.T) {
	consumed, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var refreshes atomic.Int32
	client, store := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == refreshPath {
			if refreshes.Add(1) != 1 {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
			close(consumed)
			<-release
			writeRotation(w)
			return
		}
		if r.Header.Get("Authorization") != "Bearer a1" {
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	other, err := FromSession(store, client.baseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Get(ctx, "/resource", nil, nil) }()
	<-consumed
	cancel()
	select {
	case err := <-done:
		t.Fatalf("abandoned an in-flight rotation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled caller = %v", err)
	}
	if err := other.Get(t.Context(), "/resource", nil, nil); err != nil {
		t.Fatalf("other client: %v", err)
	}
	disk, err := store.Load()
	if err != nil || disk.AccessToken != "a1" || disk.RefreshToken != "r1" || refreshes.Load() != 1 {
		t.Fatalf("rotation lost: %+v, %v, refreshes=%d", disk, err, refreshes.Load())
	}
}

func TestRefreshTransportFailure(t *testing.T) {
	for _, failure := range []string{"307", "308", "truncated response"} {
		t.Run(failure, func(t *testing.T) {
			var refreshes, requests atomic.Int32
			client, store := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != refreshPath {
					requests.Add(1)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				refreshes.Add(1)
				if failure == "truncated response" {
					w.Header().Set("Content-Length", "1000")
					_, _ = io.WriteString(w, `{"Code":1000,`)
					return
				}
				status, _ := strconv.Atoi(failure)
				http.Redirect(w, r, refreshPath, status)
			})
			err := client.Get(t.Context(), "/resource", nil, nil)
			if !errors.Is(err, ErrRefresh) || refreshes.Load() != 1 || requests.Load() != 1 {
				t.Fatalf("error=%v, refreshes=%d, requests=%d", err, refreshes.Load(), requests.Load())
			}
			if failure == "truncated response" && (!errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "proton-cal login")) {
				t.Errorf("truncated response error = %v", err)
			}
			disk, err := store.Load()
			if err != nil || disk.AccessToken != "a0" || disk.RefreshToken != "r0" {
				t.Fatalf("session changed: %+v, %v", disk, err)
			}
		})
	}
}

func TestInvalidRefreshResponse(t *testing.T) {
	for _, body := range []string{
		`not json`,
		`{"Code":1000,"AccessToken":"a1"}`,
		`{"Code":1000,"AccessToken":123,"RefreshToken":"r1"}`,
		`{"Code":1000,"UID":"another-login","AccessToken":"a1","RefreshToken":"r1"}`,
	} {
		t.Run(body, func(t *testing.T) {
			var requests atomic.Int32
			client, store := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth/v4/refresh" {
					_, _ = io.WriteString(w, body)
					return
				}
				requests.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
			})
			if err := client.Get(t.Context(), "/resource", nil, nil); err == nil || !strings.Contains(err.Error(), "proton-cal login") {
				t.Fatalf("invalid refresh error = %v", err)
			}
			disk, err := store.Load()
			if err != nil || disk.AccessToken != "a0" || disk.RefreshToken != "r0" || requests.Load() != 1 {
				t.Fatalf("invalid response saved or retried: %+v, %v, requests=%d", disk, err, requests.Load())
			}
		})
	}
}

func TestRefreshRetriesOnce(t *testing.T) {
	var refreshes, requests atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/v4/refresh" {
			refreshes.Add(1)
			writeRotation(w)
			return
		}
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	var apiErr *Error
	err := client.Get(t.Context(), "/resource", nil, nil)
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized || !strings.Contains(err.Error(), "proton-cal login") || refreshes.Load() != 1 || requests.Load() != 2 {
		t.Fatalf("error = %v, refreshes = %d, requests = %d", err, refreshes.Load(), requests.Load())
	}
}

func TestSessionChangesDuringRefresh(t *testing.T) {
	for _, change := range []string{"login", "logout", "key passphrase"} {
		t.Run(change, func(t *testing.T) {
			refreshing, release := make(chan struct{}), make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			client, store := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth/v4/refresh" {
					close(refreshing)
					<-release
					writeRotation(w)
					return
				}
				if r.Header.Get("Authorization") == "Bearer a0" {
					w.WriteHeader(http.StatusUnauthorized)
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			requestDone := make(chan error, 1)
			go func() { requestDone <- client.Get(ctx, "/resource", nil, nil) }()
			select {
			case <-refreshing:
			case <-ctx.Done():
				t.Fatal("refresh did not start")
			}

			// A second request can cancel while the first holds the session lock.
			waiting, stopWaiting := context.WithTimeout(ctx, 20*time.Millisecond)
			err := client.Get(waiting, "/resource", nil, nil)
			stopWaiting()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("waiting request = %v", err)
			}

			changeDone := make(chan error, 1)
			go func() {
				switch change {
				case "login":
					changeDone <- store.Save(config.Session{UID: "new-login", AccessToken: "new-a", RefreshToken: "new-r"})
				case "logout":
					changeDone <- store.Clear("uid")
				case "key passphrase":
					changeDone <- store.SetKeyPass("uid", []byte("derived"))
				}
			}()
			select {
			case err := <-changeDone:
				t.Fatalf("session change completed during rotation: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			unblock()
			if err := <-requestDone; err != nil {
				t.Fatal(err)
			}
			if err := <-changeDone; err != nil {
				t.Fatal(err)
			}
			disk, err := store.Load()
			switch change {
			case "login":
				if err != nil || disk.UID != "new-login" || disk.AccessToken != "new-a" || disk.RefreshToken != "new-r" {
					t.Fatalf("login overwritten: %+v, %v", disk, err)
				}
			case "logout":
				if !errors.Is(err, config.ErrNoSession) {
					t.Fatalf("logout overwritten: %+v, %v", disk, err)
				}
			case "key passphrase":
				if err != nil || disk.AccessToken != "a1" || disk.RefreshToken != "r1" || string(disk.SaltedKeyPass) != "derived" {
					t.Fatalf("rotation or passphrase overwritten: %+v, %v", disk, err)
				}
			}
		})
	}
}

func TestRefreshPersistenceFailure(t *testing.T) {
	var requests atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/v4/refresh" {
			path := filepath.Join(os.Getenv(config.EnvConfigDir), "session.json")
			if err := os.Remove(path); err != nil {
				t.Error(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Error(err)
			}
			writeRotation(w)
			return
		}
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	err := client.Get(t.Context(), "/resource", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "saving refreshed session") || !strings.Contains(err.Error(), "proton-cal login") || requests.Load() != 1 {
		t.Fatalf("error = %v, requests = %d", err, requests.Load())
	}
}
