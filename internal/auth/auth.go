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
// as the salts have been fetched; see RESEARCH.md for the full unlock/lock
// dance.
package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"slices"
	"strings"

	proton "github.com/ProtonMail/go-proton-api"
	srp "github.com/ProtonMail/go-srp"

	"github.com/cheeseandcereal/proton-cal-go/internal/config"
	"github.com/cheeseandcereal/proton-cal-go/internal/papi"
)

// codeInsufficientScope is the Proton error code returned when the access
// token lacks the scope required by an endpoint (e.g. "locked" for
// /core/v4/keys/salts). Live-verified June 2026: HTTP 403, Code 9101,
// "Access token does not have sufficient scope".
const codeInsufficientScope = 9101

// Login runs the full interactive login flow and persists the session
// (tokens + salted key passphrase) and config (username, timezone).
//
// The flow handles captcha human verification (manual token paste) and TOTP
// two-factor authentication. FIDO2-only 2FA and email-code human
// verification are not supported.
func Login(ctx context.Context, prompter Prompter) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	username := cfg.Username
	if username == "" {
		input, err := prompter.Prompt("Proton username/email")
		if err != nil {
			return fmt.Errorf("reading username: %w", err)
		}
		username = strings.TrimSpace(input)
		if username == "" {
			return errors.New("no username provided")
		}
	}

	password, err := prompter.PromptSecret("Password")
	if err != nil {
		return fmt.Errorf("reading password: %w", err)
	}
	if password == "" {
		return errors.New("no password provided")
	}

	m := papi.NewManager(cfg.EffectiveBaseURL())

	pc, a, err := m.NewClientWithLogin(ctx, username, []byte(password))
	if err != nil {
		pc, a, err = loginWithHumanVerification(ctx, m, prompter, username, password, err)
		if err != nil {
			return err
		}
	}
	defer pc.Close()

	// Two-factor authentication. TwoFA.Enabled is a bitmask: HasTOTP (1),
	// HasFIDO2 (2), HasFIDO2AndTOTP (3). Prefer TOTP when available.
	switch {
	case a.TwoFA.Enabled&proton.HasTOTP != 0:
		code, err := prompter.Prompt("2FA code")
		if err != nil {
			return fmt.Errorf("reading 2FA code: %w", err)
		}
		if err := pc.Auth2FA(ctx, proton.Auth2FAReq{TwoFactorCode: strings.TrimSpace(code)}); err != nil {
			return fmt.Errorf("2FA verification failed: %w", err)
		}
	case a.TwoFA.Enabled&proton.HasFIDO2 != 0:
		return errors.New("this account requires FIDO2 two-factor authentication, which proton-cal does not support; enable TOTP at https://account.proton.me and retry")
	}

	// Persist tokens now so the raw client (and any token refresh during the
	// remaining steps) operates against the stored session.
	store, err := config.NewSessionStore()
	if err != nil {
		return fmt.Errorf("opening session store: %w", err)
	}
	if err := store.Save(config.Session{
		UID:          a.UID,
		AccessToken:  a.AccessToken,
		RefreshToken: a.RefreshToken,
	}); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}
	papi.RegisterPersistence(pc, store)
	raw := papi.New(pc, store, cfg.EffectiveBaseURL())

	// In two-password mode the mailbox password (not the login password)
	// unlocks the keys. The login password is still what unlockScope needs.
	keyPassword := password
	if a.PasswordMode == proton.TwoPasswordMode {
		keyPassword, err = prompter.PromptSecret("Mailbox password")
		if err != nil {
			return fmt.Errorf("reading mailbox password: %w", err)
		}
		if keyPassword == "" {
			return errors.New("no mailbox password provided")
		}
	}

	// Fetch key salts; on insufficient scope, gain the "locked" scope via an
	// SRP proof to /core/v4/users/unlock and retry once. Drop the elevated
	// scope again afterwards (best effort).
	didUnlock := false
	salts, err := pc.GetSalts(ctx)
	if err != nil && isInsufficientScope(err) {
		if err := unlockScope(ctx, m, raw, username, password); err != nil {
			return fmt.Errorf("unlocking session scope: %w", err)
		}
		didUnlock = true
		salts, err = pc.GetSalts(ctx)
	}
	if err != nil {
		return fmt.Errorf("fetching key salts: %w", err)
	}

	user, err := pc.GetUser(ctx)
	if err != nil {
		return fmt.Errorf("fetching user: %w", err)
	}
	addrs, err := pc.GetAddresses(ctx)
	if err != nil {
		return fmt.Errorf("fetching addresses: %w", err)
	}

	primaryKeyID, err := primaryUserKeyID(user)
	if err != nil {
		return err
	}
	saltedKeyPass, err := salts.SaltForKey([]byte(keyPassword), primaryKeyID)
	if err != nil {
		return fmt.Errorf("deriving salted key passphrase: %w", err)
	}
	if len(saltedKeyPass) == 0 {
		return errors.New("deriving salted key passphrase: empty result")
	}

	// Verify the derived passphrase actually unlocks key material before
	// persisting it.
	userKR, addrKRs, err := proton.Unlock(user, addrs, saltedKeyPass, nil)
	if err != nil {
		return fmt.Errorf("verifying key unlock (wrong mailbox password?): %w", err)
	}
	if len(addrKRs) == 0 {
		return errors.New("no address keys could be unlocked; calendar decryption would fail")
	}
	userKR.ClearPrivateParams()
	for _, kr := range addrKRs {
		kr.ClearPrivateParams()
	}

	// Best-effort re-lock: drop the elevated "locked" scope if we gained it.
	if didUnlock {
		_ = raw.Put(ctx, "/core/v4/users/lock", struct{}{}, nil)
	}

	// Persist the salted key passphrase. Tokens may have rotated since the
	// initial Save (the auth handler persists rotations), so read-modify-write.
	sess, err := store.Load()
	if err != nil {
		return fmt.Errorf("reloading session: %w", err)
	}
	sess.SaltedKeyPass = saltedKeyPass
	if err := store.Save(sess); err != nil {
		return fmt.Errorf("saving session: %w", err)
	}

	cfg.Username = username
	if cfg.Timezone == "" {
		cfg.Timezone = config.DetectTimezone()
	}
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	prompter.Notify("Login successful. Session saved.")

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
	return nil
}

// hvDetails is the shape of proton.APIError.Details on a human verification
// error (code 9001). It is a superset of proton.APIHVDetails: the API also
// returns the URL of the hosted verification page.
type hvDetails struct {
	HumanVerificationMethods []string
	HumanVerificationToken   string
	WebURL                   string `json:"WebUrl"`
}

// loginWithHumanVerification handles a failed login attempt: if loginErr is a
// human verification request (code 9001) offering the captcha method, it
// walks the user through the manual captcha token paste flow and retries
// the login with the token.
// Any other error is returned wrapped.
func loginWithHumanVerification(ctx context.Context, m *proton.Manager, prompter Prompter, username, password string, loginErr error) (*proton.Client, proton.Auth, error) {
	var apiErr *proton.APIError
	if !errors.As(loginErr, &apiErr) || !apiErr.IsHVError() {
		return nil, proton.Auth{}, fmt.Errorf("login failed: %w", loginErr)
	}

	var details hvDetails
	if err := json.Unmarshal(apiErr.Details, &details); err != nil {
		return nil, proton.Auth{}, fmt.Errorf("parsing human verification details: %w", err)
	}

	prompter.Notify(fmt.Sprintf("Human verification required. Available methods: %s", strings.Join(details.HumanVerificationMethods, ", ")))

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

	// Retry the login with the pasted token. go-proton-api derives the HV
	// headers from APIHVDetails: x-pm-human-verification-token from Token,
	// and x-pm-human-verification-token-type from Methods joined with ","
	// (see hv.go addHVToRequest). Pass exactly {"captcha"} so the type
	// header is "captcha".
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
	if err := exec.Command("xdg-open", verifyURL).Start(); err != nil {
		prompter.Notify(fmt.Sprintf("Open this URL: %s", verifyURL))
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

// isInsufficientScope reports whether err indicates the access token lacks
// the scope for the attempted endpoint (Proton code 9101 / HTTP 403).
func isInsufficientScope(err error) bool {
	if papi.IsCode(err, codeInsufficientScope) {
		return true
	}
	var apiErr *proton.APIError
	return errors.As(err, &apiErr) && apiErr.Status == 403
}

// unlockScope gains the elevated "locked" scope by proving knowledge of the
// LOGIN password via SRP to PUT /core/v4/users/unlock (always the login
// password, even in two-password mode). go-proton-api does not implement
// this endpoint, so the raw papi client is used. The server proof is
// verified before returning.
//
// The in-memory test server implements neither the scope restriction nor
// this endpoint, so this path is only exercised by live integration runs.
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
