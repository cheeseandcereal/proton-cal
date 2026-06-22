package config

import (
	"errors"
	"testing"
)

func TestEffectiveBaseURL(t *testing.T) {
	if got := (Config{}).EffectiveBaseURL(); got != DefaultBaseURL {
		t.Errorf("default = %q, want %q", got, DefaultBaseURL)
	}
	if got := (Config{BaseURL: "https://example.test"}).EffectiveBaseURL(); got != "https://example.test" {
		t.Errorf("override = %q", got)
	}
}

func TestEffectiveTimezone(t *testing.T) {
	if got := (Config{Timezone: "Europe/Berlin"}).EffectiveTimezone(); got != "Europe/Berlin" {
		t.Errorf("explicit = %q", got)
	}
	// Empty falls through to system detection, which must never be empty.
	if got := (Config{}).EffectiveTimezone(); got == "" {
		t.Error("detected timezone must not be empty")
	}
}

func TestConfigSaveLoadRoundTrip(t *testing.T) {
	t.Setenv(EnvConfigDir, t.TempDir())

	// Missing file -> zero config, no error.
	got, err := Load()
	if err != nil {
		t.Fatalf("Load (missing): %v", err)
	}
	if (got != Config{}) {
		t.Errorf("missing config not zero: %+v", got)
	}

	want := Config{Username: "u@proton.me", Timezone: "America/New_York", DefaultCalendar: "Work", BaseURL: "https://h.test"}
	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Errorf("round trip:\n got %+v\nwant %+v", got, want)
	}
}

func TestSessionValid(t *testing.T) {
	if (Session{}).Valid() {
		t.Error("empty session must be invalid")
	}
	if (Session{UID: "u", AccessToken: "a"}).Valid() {
		t.Error("session without refresh token must be invalid")
	}
	if !(Session{UID: "u", AccessToken: "a", RefreshToken: "r"}).Valid() {
		t.Error("complete session must be valid")
	}
}

func TestSessionStoreLifecycle(t *testing.T) {
	t.Setenv(EnvConfigDir, t.TempDir())
	store, err := NewSessionStore()
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	// No session yet.
	if _, err := store.Load(); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Load (empty) = %v, want ErrNoSession", err)
	}

	sess := Session{UID: "u", AccessToken: "a", RefreshToken: "r", SaltedKeyPass: []byte("secret")}
	if err := store.Save(sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.UID != "u" || string(got.SaltedKeyPass) != "secret" {
		t.Errorf("loaded session = %+v", got)
	}

	// UpdateTokens rotates tokens but preserves the key passphrase.
	if err := store.UpdateTokens("u2", "a2", "r2"); err != nil {
		t.Fatalf("UpdateTokens: %v", err)
	}
	got, err = store.Load()
	if err != nil {
		t.Fatalf("Load after rotate: %v", err)
	}
	if got.UID != "u2" || got.AccessToken != "a2" || got.RefreshToken != "r2" {
		t.Errorf("tokens not rotated: %+v", got)
	}
	if string(got.SaltedKeyPass) != "secret" {
		t.Errorf("UpdateTokens dropped the key passphrase: %q", got.SaltedKeyPass)
	}

	// Clear removes it; a second Clear is a no-op (not an error).
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear (already gone): %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNoSession) {
		t.Errorf("Load after Clear = %v, want ErrNoSession", err)
	}
}

// UpdateTokens on an empty store creates a fresh session rather than erroring.
func TestUpdateTokensFromEmpty(t *testing.T) {
	t.Setenv(EnvConfigDir, t.TempDir())
	store, err := NewSessionStore()
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	if err := store.UpdateTokens("u", "a", "r"); err != nil {
		t.Fatalf("UpdateTokens from empty: %v", err)
	}
	got, err := store.Load()
	if err != nil || !got.Valid() {
		t.Fatalf("session after UpdateTokens = %+v, err %v", got, err)
	}
}
