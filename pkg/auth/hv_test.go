package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	proton "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/go-proton-api/server"

	"github.com/cheeseandcereal/proton-cal/pkg/papi"
)

type hvPrompter struct {
	scriptPrompter
	prompt func(string) (string, error)
}

func (p *hvPrompter) Prompt(label string) (string, error) { return p.prompt(label) }

func TestAuthenticateCaptchaRetry(t *testing.T) {
	const challengeToken = "challenge+token/with spaces?x=1&embed=true#%2B"
	const wantURL = "https://verify.proton.me/?methods=captcha&token=challenge%2Btoken%2Fwith+spaces%3Fx%3D1%26embed%3Dtrue%23%252B"
	originalOpenBrowser := openBrowser
	browserAttempted := false
	openBrowser = func(gotURL string) error {
		browserAttempted = true
		if gotURL != wantURL {
			t.Errorf("browser URL = %q, want %q", gotURL, wantURL)
		}
		return errors.New("no browser available")
	}
	t.Cleanup(func() { openBrowser = originalOpenBrowser })
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
	var attempts atomic.Int32
	var promptCompleted atomic.Bool
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check every retry request, including the SRP auth-info request.
		if attempts.Load() > 0 && !promptCompleted.Load() {
			t.Errorf("request %s %s before CAPTCHA prompt completed", r.Method, r.URL.Path)
			http.Error(w, "CAPTCHA prompt pending", http.StatusBadRequest)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/auth/v4" {
			attempt := attempts.Add(1)
			wantToken, wantType := "", ""
			if attempt > 1 {
				wantToken, wantType = challengeToken, "captcha"
			}
			if got := r.Header.Get("x-pm-human-verification-token"); got != wantToken {
				t.Errorf("attempt %d: HV token = %q, want %q", attempt, got, wantToken)
			}
			if got := r.Header.Get("x-pm-human-verification-token-type"); got != wantType {
				t.Errorf("attempt %d: HV type = %q, want %q", attempt, got, wantType)
			}
			if attempt == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				if err := json.NewEncoder(w).Encode(map[string]any{
					"Code": 9001, "Error": "Human verification required",
					"Details": map[string]any{
						"HumanVerificationMethods": []string{"email", "captcha"},
						"HumanVerificationToken":   challengeToken,
						"WebUrl":                   "https://verify.proton.me/?methods=captcha&token=web-url-token&embed=true",
					},
				}); err != nil {
					t.Error(err)
				}
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	m := papi.NewManager(front.URL)
	t.Cleanup(m.Close)

	prompts := 0
	p := &hvPrompter{scriptPrompter: scriptPrompter{t: t}}
	p.prompt = func(label string) (string, error) {
		prompts++
		if !browserAttempted {
			t.Error("browser was not opened before the CAPTCHA prompt")
		}
		if label != "Press Enter after completing the CAPTCHA" {
			t.Errorf("prompt = %q", label)
		}
		if got := attempts.Load(); got != 1 {
			t.Errorf("login attempts at prompt = %d, want 1", got)
		}
		if notices := strings.Join(p.notices, "\n"); !slices.Contains(strings.Fields(notices), wantURL) {
			t.Errorf("notices before prompt lack canonical CAPTCHA URL %q: %q", wantURL, notices)
		}
		promptCompleted.Store(true)
		return "", nil // An empty line confirms completion.
	}
	pc, a, err := authenticate(t.Context(), m, p, testUser, testPass)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if pc == nil {
		t.Fatal("authenticate returned no client")
	}
	defer pc.Close()
	if a.UID == "" || a.AccessToken == "" || a.RefreshToken == "" {
		t.Error("authenticate returned incomplete session credentials")
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("login attempts = %d, want 2", got)
	}
	if prompts != 1 {
		t.Errorf("CAPTCHA prompts = %d, want 1", prompts)
	}
}

func TestLoginWithHumanVerificationErrors(t *testing.T) {
	originalOpenBrowser := openBrowser
	openBrowser = func(string) error { return nil }
	t.Cleanup(func() { openBrowser = originalOpenBrowser })
	var requests atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"Code":2000,"Error":"Unexpected retry"}`)
	}))
	t.Cleanup(api.Close)
	m := papi.NewManager(api.URL)
	t.Cleanup(m.Close)

	for _, tt := range []struct {
		name        string
		details     string
		promptErr   error
		wantMessage string
	}{
		{
			name:      "prompt error",
			details:   `{"HumanVerificationMethods":["captcha"],"HumanVerificationToken":"challenge"}`,
			promptErr: io.EOF,
		},
		{
			name:        "unsupported method",
			details:     `{"HumanVerificationMethods":["email"],"HumanVerificationToken":"challenge"}`,
			wantMessage: "supported",
		},
		{
			name:        "missing token",
			details:     `{"HumanVerificationMethods":["captcha"]}`,
			wantMessage: "token",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests.Store(0)
			prompts := 0
			p := &hvPrompter{scriptPrompter: scriptPrompter{t: t}, prompt: func(label string) (string, error) {
				prompts++
				if label != "Press Enter after completing the CAPTCHA" {
					t.Errorf("prompt = %q", label)
				}
				return "", tt.promptErr
			}}
			pc, _, err := loginWithHumanVerification(t.Context(), m, p, testUser, testPass, &proton.APIError{
				Code: 9001, Details: proton.ErrDetails(tt.details),
			})
			if pc != nil {
				pc.Close()
				t.Error("returned a client on failed verification")
			}
			if tt.promptErr != nil {
				if !errors.Is(err, tt.promptErr) {
					t.Errorf("error = %v, want wrapped %v", err, tt.promptErr)
				}
			} else if err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.wantMessage) {
				t.Errorf("error = %v, want message containing %q", err, tt.wantMessage)
			}
			wantPrompts := 0
			if tt.promptErr != nil {
				wantPrompts = 1
			}
			if prompts != wantPrompts {
				t.Errorf("prompts = %d, want %d", prompts, wantPrompts)
			}
			if got := requests.Load(); got != 0 {
				t.Errorf("retry requests = %d, want 0", got)
			}
		})
	}
}
