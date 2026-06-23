// Package auth implements the interactive Proton login flow, session
// teardown, and key unlocking for proton-cal.
//
// Credential model (bridge-style): Login derives the salted key passphrase
// once (from the account/mailbox password and the primary key salt) and
// persists it in the session store alongside the session tokens. The account
// password itself is never persisted. Later commands restore the session and
// call UnlockKeys, which unlocks user/address keys from the stored salted
// passphrase without re-prompting.
//
// Scope note (live-verified): GET /core/v4/keys/salts requires the elevated
// "locked" scope, which a freshly logged-in or restored session may lack
// (HTTP 403, Proton code 9101). Gaining it requires a full SRP proof to
// PUT /core/v4/users/unlock, which go-proton-api does not implement; see
// unlockScope. The scope is dropped again (PUT /core/v4/users/lock) as soon
// as the salts have been fetched; see docs/overview.md for the full
// unlock/lock dance.
package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	proton "github.com/ProtonMail/go-proton-api"
	srp "github.com/ProtonMail/go-srp"
	"github.com/pkg/browser"

	"github.com/cheeseandcereal/proton-cal/pkg/config"
	"github.com/cheeseandcereal/proton-cal/pkg/papi"
)

// Login runs the interactive login flow and persists the session (tokens +
// salted key passphrase), returning the logged-in username for the caller to
// persist. Handles captcha (manual token paste) and TOTP; FIDO2-only 2FA and
// email-code verification are unsupported.
func Login(ctx context.Context, prompter Prompter, cfg config.Config) (string, error) {
	username, password, err := promptCredentials(prompter, cfg.Username)
	if err != nil {
		return "", err
	}

	m := papi.NewManager(cfg.EffectiveBaseURL())
	defer m.Close()

	pc, a, err := authenticate(ctx, m, prompter, username, password)
	if err != nil {
		return "", err
	}
	defer pc.Close()

	// Persist tokens now so the raw client below reads them and any refresh
	// lands in the store; written again later since the salted key passphrase
	// needs salts fetched with a working session.
	store, err := config.NewSessionStore()
	if err != nil {
		return "", fmt.Errorf("opening session store: %w", err)
	}
	if err := store.Save(config.Session{
		UID:          a.UID,
		AccessToken:  a.AccessToken,
		RefreshToken: a.RefreshToken,
	}); err != nil {
		return "", fmt.Errorf("saving session: %w", err)
	}
	raw := papi.New(pc, store, cfg.EffectiveBaseURL())

	keyPassword, err := promptKeyPassword(prompter, a.PasswordMode, password)
	if err != nil {
		return "", err
	}

	salts, err := fetchSalts(ctx, pc, m, raw, username, password)
	if err != nil {
		return "", err
	}

	user, err := pc.GetUser(ctx)
	if err != nil {
		return "", fmt.Errorf("fetching user: %w", err)
	}
	addrs, err := pc.GetAddresses(ctx)
	if err != nil {
		return "", fmt.Errorf("fetching addresses: %w", err)
	}

	saltedKeyPass, err := deriveAndVerifyKeyPass(user, addrs, salts, keyPassword)
	if err != nil {
		return "", err
	}

	if err := persistKeyPass(store, saltedKeyPass); err != nil {
		return "", err
	}

	prompter.Notify("Login successful. Session saved.")

	return username, nil
}

// promptCredentials returns the configured username (prompting only when
// unset) and a freshly prompted password.
func promptCredentials(prompter Prompter, configured string) (username, password string, err error) {
	username = configured
	if username == "" {
		input, err := prompter.Prompt("Proton username/email")
		if err != nil {
			return "", "", fmt.Errorf("reading username: %w", err)
		}
		username = strings.TrimSpace(input)
		if username == "" {
			return "", "", errors.New("no username provided")
		}
	}

	password, err = prompter.PromptSecret("Password")
	if err != nil {
		return "", "", fmt.Errorf("reading password: %w", err)
	}
	if password == "" {
		return "", "", errors.New("no password provided")
	}
	return username, password, nil
}

// authenticate performs SRP login (with captcha fallback) and TOTP 2FA,
// returning an authenticated client.
func authenticate(ctx context.Context, m *proton.Manager, prompter Prompter, username, password string) (*proton.Client, proton.Auth, error) {
	pc, a, err := m.NewClientWithLogin(ctx, username, []byte(password))
	if err != nil {
		pc, a, err = loginWithHumanVerification(ctx, m, prompter, username, password, err)
		if err != nil {
			return nil, proton.Auth{}, err
		}
	}

	// Two-factor authentication. TwoFA.Enabled is a bitmask: HasTOTP (1),
	// HasFIDO2 (2), HasFIDO2AndTOTP (3). Prefer TOTP when available.
	switch {
	case a.TwoFA.Enabled&proton.HasTOTP != 0:
		code, err := prompter.Prompt("2FA code")
		if err != nil {
			pc.Close()
			return nil, proton.Auth{}, fmt.Errorf("reading 2FA code: %w", err)
		}
		if err := pc.Auth2FA(ctx, proton.Auth2FAReq{TwoFactorCode: strings.TrimSpace(code)}); err != nil {
			pc.Close()
			return nil, proton.Auth{}, fmt.Errorf("2FA verification failed: %w", err)
		}
	case a.TwoFA.Enabled&proton.HasFIDO2 != 0:
		pc.Close()
		return nil, proton.Auth{}, errors.New("this account requires FIDO2 two-factor authentication, which proton-cal does not support; enable TOTP at https://account.proton.me and retry")
	}
	return pc, a, nil
}

// promptKeyPassword returns the key-unlock password: the prompted mailbox
// password in two-password mode, else the login password.
func promptKeyPassword(prompter Prompter, mode proton.PasswordMode, loginPassword string) (string, error) {
	if mode != proton.TwoPasswordMode {
		return loginPassword, nil
	}
	keyPassword, err := prompter.PromptSecret("Mailbox password")
	if err != nil {
		return "", fmt.Errorf("reading mailbox password: %w", err)
	}
	if keyPassword == "" {
		return "", errors.New("no mailbox password provided")
	}
	return keyPassword, nil
}

// fetchSalts fetches the key salts. On insufficient scope it gains the
// "locked" scope (SRP proof to /core/v4/users/unlock), retries once, then
// drops the scope again (best effort).
func fetchSalts(ctx context.Context, pc *proton.Client, m *proton.Manager, raw *papi.Client, username, password string) (proton.Salts, error) {
	salts, err := pc.GetSalts(ctx)
	if err == nil {
		return salts, nil
	}
	if !isInsufficientScope(err) {
		return nil, fmt.Errorf("fetching key salts: %w", err)
	}
	werr := WithLockedScope(ctx, m, raw, username, password, func() error {
		salts, err = pc.GetSalts(ctx)
		if err != nil {
			return fmt.Errorf("fetching key salts: %w", err)
		}
		return nil
	})
	if werr != nil {
		return nil, werr
	}
	return salts, nil
}

// WithLockedScope gains the elevated "locked" scope (SRP proof of the LOGIN
// password to PUT /core/v4/users/unlock), runs fn, then drops it again (PUT
// /core/v4/users/lock, best effort). Sensitive ops on a restored session need
// this since its token lacks the scope (Proton code 9101); password is always
// the login password and a wrong one surfaces as a server-proof mismatch.
func WithLockedScope(ctx context.Context, m *proton.Manager, raw *papi.Client, username, password string, fn func() error) error {
	if err := unlockScope(ctx, m, raw, username, password); err != nil {
		return fmt.Errorf("unlocking session scope: %w", err)
	}
	// Best-effort re-lock: drop the elevated scope when done.
	defer func() { _ = raw.Put(ctx, "/core/v4/users/lock", struct{}{}, nil) }()
	return fn()
}

// deriveAndVerifyKeyPass derives the salted key passphrase from the key
// password and primary key salt, verifying it unlocks key material before use.
func deriveAndVerifyKeyPass(user proton.User, addrs []proton.Address, salts proton.Salts, keyPassword string) ([]byte, error) {
	primaryKeyID, err := primaryUserKeyID(user)
	if err != nil {
		return nil, err
	}
	saltedKeyPass, err := salts.SaltForKey([]byte(keyPassword), primaryKeyID)
	if err != nil {
		return nil, fmt.Errorf("deriving salted key passphrase: %w", err)
	}
	if len(saltedKeyPass) == 0 {
		return nil, errors.New("deriving salted key passphrase: empty result")
	}

	userKR, addrKRs, err := proton.Unlock(user, addrs, saltedKeyPass, nil)
	if err != nil {
		return nil, fmt.Errorf("verifying key unlock (wrong mailbox password?): %w", err)
	}
	if len(addrKRs) == 0 {
		return nil, errors.New("no address keys could be unlocked; calendar decryption would fail")
	}
	userKR.ClearPrivateParams()
	for _, kr := range addrKRs {
		kr.ClearPrivateParams()
	}
	return saltedKeyPass, nil
}

// persistKeyPass adds the salted key passphrase to the stored session via a
// read-modify-write, since tokens may have rotated since the initial save.
func persistKeyPass(store *config.SessionStore, saltedKeyPass []byte) error {
	sess, err := store.Load()
	if err != nil {
		return fmt.Errorf("reloading session: %w", err)
	}
	sess.SaltedKeyPass = saltedKeyPass
	if err := store.Save(sess); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}
	return nil
}

// Logout best-effort revokes the current session with the API (AuthDelete)
// and clears the session store. A missing session is not an error.
func Logout(ctx context.Context) error {
	store, err := config.NewSessionStore()
	if err != nil {
		return fmt.Errorf("opening session store: %w", err)
	}

	sess, err := store.Load()
	if errors.Is(err, config.ErrNoSession) {
		return nil
	} else if err != nil {
		return fmt.Errorf("loading session: %w", err)
	}

	if sess.Valid() {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}
		m := papi.NewManager(cfg.EffectiveBaseURL())
		pc := m.NewClient(sess.UID, sess.AccessToken, sess.RefreshToken)
		// Best effort: the session may already be expired or revoked.
		_ = pc.AuthDelete(ctx)
		pc.Close()
		m.Close()
	}

	if err := store.Clear(); err != nil {
		return fmt.Errorf("clearing session: %w", err)
	}
	// The bootstrap cache holds (encrypted) key material scoped to the
	// session that just ended; never leave it behind.
	if err := config.ClearCache(); err != nil {
		return fmt.Errorf("clearing cache: %w", err)
	}
	return nil
}

// hvDetails is proton.APIError.Details on a human verification error (code
// 9001): a superset of proton.APIHVDetails adding the hosted page URL.
type hvDetails struct {
	HumanVerificationMethods []string
	HumanVerificationToken   string
	WebURL                   string `json:"WebUrl"`
}

// loginWithHumanVerification handles a captcha human-verification request
// (code 9001) by walking the user through the manual token paste and retrying;
// any other error is returned wrapped.
func loginWithHumanVerification(ctx context.Context, m *proton.Manager, prompter Prompter, username, password string, loginErr error) (*proton.Client, proton.Auth, error) {
	var apiErr *proton.APIError
	if !errors.As(loginErr, &apiErr) || !apiErr.IsHVError() {
		return nil, proton.Auth{}, fmt.Errorf("login failed: %w", loginErr)
	}

	var details hvDetails
	if err := json.Unmarshal(apiErr.Details, &details); err != nil {
		return nil, proton.Auth{}, fmt.Errorf("parsing human verification details: %w", err)
	}

	prompter.Notify("Human verification required. Available methods: " + strings.Join(details.HumanVerificationMethods, ", "))

	if !slices.Contains(details.HumanVerificationMethods, "captcha") {
		return nil, proton.Auth{}, fmt.Errorf(
			"no supported verification method available (methods: %s); try logging in at https://account.proton.me first, then retry",
			strings.Join(details.HumanVerificationMethods, ", "),
		)
	}

	verifyURL := details.WebURL
	if verifyURL == "" {
		verifyURL = "https://account.proton.me/verify?methods=captcha&token=" + url.QueryEscape(details.HumanVerificationToken)
	}

	token, err := captchaToken(prompter, verifyURL)
	if err != nil {
		return nil, proton.Auth{}, err
	}

	// Retry with the pasted token. go-proton-api builds the HV headers from
	// APIHVDetails (token from Token, type from Methods joined by ","), so
	// pass exactly {"captcha"} to get type header "captcha".
	pc, a, err := m.NewClientWithLoginWithHVToken(ctx, username, []byte(password), &proton.APIHVDetails{
		Methods: []string{"captcha"},
		Token:   token,
	})
	if err != nil {
		return nil, proton.Auth{}, fmt.Errorf("login failed after captcha verification: %w", err)
	}
	return pc, a, nil
}

// captchaToken guides the user through manually capturing the captcha token
// and returns the pasted token.
func captchaToken(prompter Prompter, verifyURL string) (string, error) {
	prompter.Notify("\nCAPTCHA verification required. Here's what to do:\n")
	prompter.Notify("1. A browser window will open with the CAPTCHA.")
	prompter.Notify("2. BEFORE solving it, open the browser console (F12 > Console)")
	prompter.Notify("3. Paste this one-liner and press Enter:\n")
	prompter.Notify(`   window.addEventListener("message", e => { let d = typeof e.data === "string" ? JSON.parse(e.data) : e.data; if (d.type === "HUMAN_VERIFICATION_SUCCESS") { document.title = "TOKEN:" + d.payload.token; console.log("TOKEN:", d.payload.token); } })`)
	prompter.Notify("\n4. Now solve the CAPTCHA.")
	prompter.Notify("5. The token will appear in the console and page title.\n")

	// Best-effort browser launch; on failure just show the URL.
	if err := browser.OpenURL(verifyURL); err != nil {
		prompter.Notify("Open this URL: " + verifyURL)
	}

	token, err := prompter.Prompt("Paste the token here")
	if err != nil {
		return "", fmt.Errorf("reading captcha token: %w", err)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errors.New("no captcha token provided")
	}
	return token, nil
}

// isInsufficientScope reports whether the token lacks scope (Proton code
// 9101). Code-based only: a bare HTTP 403 can be a real permission problem and
// must not trigger the SRP unlock dance.
func isInsufficientScope(err error) bool {
	return papi.IsCode(err, papi.CodeInsufficientScope)
}

// unlockScope gains the "locked" scope via SRP proof of the LOGIN password to
// PUT /core/v4/users/unlock (raw papi client, since go-proton-api lacks this
// endpoint), verifying the server proof. Only exercised by live integration
// runs; the in-memory test server implements neither scope nor endpoint.
func unlockScope(ctx context.Context, m *proton.Manager, raw *papi.Client, username, password string) error {
	info, err := m.AuthInfo(ctx, proton.AuthInfoReq{Username: username})
	if err != nil {
		return fmt.Errorf("fetching auth info: %w", err)
	}

	srpAuth, err := srp.NewAuth(info.Version, username, []byte(password), info.Salt, info.Modulus, info.ServerEphemeral)
	if err != nil {
		return fmt.Errorf("preparing SRP auth: %w", err)
	}

	proofs, err := srpAuth.GenerateProofs(2048)
	if err != nil {
		return fmt.Errorf("generating SRP proofs: %w", err)
	}

	var out struct {
		Code        int
		ServerProof string
	}
	if err := raw.Put(ctx, "/core/v4/users/unlock", map[string]any{
		"ClientEphemeral": base64.StdEncoding.EncodeToString(proofs.ClientEphemeral),
		"ClientProof":     base64.StdEncoding.EncodeToString(proofs.ClientProof),
		"SRPSession":      info.SRPSession,
	}, &out); err != nil {
		return fmt.Errorf("PUT /core/v4/users/unlock: %w", err)
	}

	serverProof, err := base64.StdEncoding.DecodeString(out.ServerProof)
	if err != nil {
		return fmt.Errorf("decoding server proof: %w", err)
	}
	if !bytes.Equal(serverProof, proofs.ExpectedServerProof) {
		return errors.New("server proof mismatch on unlock: possible MITM or server misbehaviour")
	}
	return nil
}

// primaryUserKeyID returns the ID of the user key marked primary, falling
// back to the first key.
func primaryUserKeyID(user proton.User) (string, error) {
	if len(user.Keys) == 0 {
		return "", errors.New("user has no keys")
	}
	for _, key := range user.Keys {
		if key.Primary {
			return key.ID, nil
		}
	}
	return user.Keys[0].ID, nil
}
