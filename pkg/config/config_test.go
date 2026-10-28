package config

import (
	"context"
	"errors"
	"reflect"
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

	want := Config{Username: "u@proton.me", Timezone: "America/New_York", BaseURL: "https://h.test"}
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

	// Rotation preserves the key passphrase.
	if _, err := store.Refresh(t.Context(), sess, func(Session) (Session, error) {
		return Session{UID: "u", AccessToken: "a2", RefreshToken: "r2"}, nil
	}); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	got, err = store.Load()
	if err != nil {
		t.Fatalf("Load after rotate: %v", err)
	}
	if got.UID != "u" || got.AccessToken != "a2" || got.RefreshToken != "r2" {
		t.Errorf("tokens not rotated: %+v", got)
	}
	if string(got.SaltedKeyPass) != "secret" {
		t.Errorf("Refresh dropped the key passphrase: %q", got.SaltedKeyPass)
	}

	if err := store.SetKeyPass("u", []byte("derived")); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear("another-login"); err != nil {
		t.Fatal(err)
	}
	got, err = store.Load()
	if err != nil || got.AccessToken != "a2" || got.RefreshToken != "r2" || string(got.SaltedKeyPass) != "derived" {
		t.Fatalf("session after key update and stale logout = %+v, %v", got, err)
	}

	// Clear removes it; a second Clear is a no-op (not an error).
	if err := store.Clear("u"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if err := store.Clear("u"); err != nil {
		t.Fatalf("Clear (already gone): %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNoSession) {
		t.Errorf("Load after Clear = %v, want ErrNoSession", err)
	}
}

func TestRefreshAdoptsOrRejectsSavedSession(t *testing.T) {
	previous := Session{UID: "u", AccessToken: "a", RefreshToken: "r"}
	for _, tc := range []struct {
		name string
		disk Session
		err  error
	}{
		{"rotated access", Session{UID: "u", AccessToken: "a2", RefreshToken: "r"}, nil},
		{"rotated refresh", Session{UID: "u", AccessToken: "a", RefreshToken: "r2"}, nil},
		{"another login", Session{UID: "other", AccessToken: "a2", RefreshToken: "r2"}, ErrSessionChanged},
		{"incomplete", Session{UID: "u"}, ErrNoSession},
		{"logged out", Session{}, ErrNoSession},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &SessionStore{dir: t.TempDir()}
			if tc.disk.UID != "" {
				if err := store.Save(tc.disk); err != nil {
					t.Fatal(err)
				}
			}
			got, err := store.Refresh(t.Context(), previous, func(Session) (Session, error) {
				t.Error("unexpected token rotation")
				return Session{}, nil
			})
			if !errors.Is(err, tc.err) {
				t.Fatalf("Refresh error = %v, want %v", err, tc.err)
			}
			if err == nil && !reflect.DeepEqual(got, tc.disk) {
				t.Errorf("adopted session = %+v, want %+v", got, tc.disk)
			}
			if tc.disk.UID != previous.UID {
				if err := store.SetKeyPass(previous.UID, []byte("stale")); !errors.Is(err, tc.err) {
					t.Errorf("SetKeyPass error = %v, want %v", err, tc.err)
				}
			}
			if tc.disk.UID != "" && tc.disk.UID != previous.UID {
				disk, err := store.Load()
				if err != nil || !reflect.DeepEqual(disk, tc.disk) {
					t.Fatalf("replacement login changed: %+v, %v", disk, err)
				}
			}
		})
	}
}

func TestRefreshCancelledWhileWaitingForLock(t *testing.T) {
	store := &SessionStore{dir: t.TempDir()}
	unlock, err := lock(store.lockPath())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = store.Refresh(ctx, Session{}, func(Session) (Session, error) {
		t.Error("cancelled refresh called rotate")
		return Session{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Refresh error = %v, want context.Canceled", err)
	}
}
