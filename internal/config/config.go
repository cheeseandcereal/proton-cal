// Package config manages the proton-cal configuration directory:
//
//   - config.toml: user-editable settings (username, timezone, default calendar)
//   - session.json: session tokens + derived salted key passphrase (mode 0600)
//
// The session file is guarded by a flock'd lock file so concurrent processes
// (e.g. a running MCP server and a CLI command) do not clobber each other's
// token refreshes.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/gofrs/flock"
	"github.com/natefinch/atomic"
)

// EnvConfigDir overrides the config directory (used by tests and the
// integration suite).
const EnvConfigDir = "PROTON_CAL_CONFIG_DIR"

const (
	dirName     = "proton-cal"
	configFile  = "config.toml"
	sessionFile = "session.json"
	lockFile    = "session.lock"
)

// DefaultBaseURL is the Proton API endpoint used unless overridden in
// config.toml. This host is verified to work with app version "Other"
// (see docs/crypto.md).
const DefaultBaseURL = "https://mail-api.proton.me"

// Config is the user-editable configuration stored in config.toml.
type Config struct {
	Username        string `toml:"username,omitempty"`
	Timezone        string `toml:"timezone,omitempty"`
	DefaultCalendar string `toml:"default_calendar,omitempty"`
	BaseURL         string `toml:"base_url,omitempty"`
}

// EffectiveBaseURL returns the configured base URL or the default.
func (c Config) EffectiveBaseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return DefaultBaseURL
}

// EffectiveTimezone returns the configured IANA timezone, else a best-effort
// system detection, else UTC.
func (c Config) EffectiveTimezone() string {
	if c.Timezone != "" {
		return c.Timezone
	}
	return DetectTimezone()
}

// Dir returns the configuration directory, creating it if needed.
func Dir() (string, error) {
	if dir := os.Getenv(EnvConfigDir); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating user config dir: %w", err)
	}
	dir := filepath.Join(base, dirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// Load reads config.toml. A missing file yields a zero Config.
func Load() (Config, error) {
	dir, err := Dir()
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	data, err := os.ReadFile(filepath.Join(dir, configFile))
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	} else if err != nil {
		return cfg, err
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing config.toml: %w", err)
	}
	return cfg, nil
}

// Save writes config.toml (mode 0600).
func Save(cfg Config) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, configFile), buf.Bytes())
}

// Session holds everything needed to use the API after login: the session
// tokens and the derived salted key passphrase (which unlocks user/address
// keys without the account password).
type Session struct {
	UID           string `json:"uid"`
	AccessToken   string `json:"access_token"`
	RefreshToken  string `json:"refresh_token"`
	SaltedKeyPass []byte `json:"salted_key_pass,omitempty"`
}

// Valid reports whether the session has the fields required to make
// authenticated API calls.
func (s Session) Valid() bool {
	return s.UID != "" && s.AccessToken != "" && s.RefreshToken != ""
}

// ErrNoSession is returned when no session file exists.
var ErrNoSession = errors.New("no saved session (run `proton-cal login`)")

// SessionStore reads and writes session.json under an advisory file lock.
type SessionStore struct {
	dir string
}

// NewSessionStore returns a store rooted at the config directory.
func NewSessionStore() (*SessionStore, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return &SessionStore{dir: dir}, nil
}

func (s *SessionStore) sessionPath() string { return filepath.Join(s.dir, sessionFile) }
func (s *SessionStore) lockPath() string    { return filepath.Join(s.dir, lockFile) }

// Load reads the current session.
func (s *SessionStore) Load() (Session, error) {
	unlock, err := lock(s.lockPath())
	if err != nil {
		return Session{}, err
	}
	defer unlock()
	return s.loadLocked()
}

func (s *SessionStore) loadLocked() (Session, error) {
	data, err := os.ReadFile(s.sessionPath())
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, ErrNoSession
	} else if err != nil {
		return Session{}, err
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return Session{}, fmt.Errorf("parsing session.json: %w", err)
	}
	return sess, nil
}

// Save writes the session (mode 0600).
func (s *SessionStore) Save(sess Session) error {
	unlock, err := lock(s.lockPath())
	if err != nil {
		return err
	}
	defer unlock()
	return s.saveLocked(sess)
}

func (s *SessionStore) saveLocked(sess Session) error {
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(s.sessionPath(), data)
}

// UpdateTokens persists rotated session tokens, preserving the stored salted
// key passphrase. Used as the go-proton-api auth refresh handler.
func (s *SessionStore) UpdateTokens(uid, accessToken, refreshToken string) error {
	unlock, err := lock(s.lockPath())
	if err != nil {
		return err
	}
	defer unlock()
	sess, err := s.loadLocked()
	if err != nil && !errors.Is(err, ErrNoSession) {
		return err
	}
	sess.UID = uid
	sess.AccessToken = accessToken
	sess.RefreshToken = refreshToken
	return s.saveLocked(sess)
}

// Clear removes the session file.
func (s *SessionStore) Clear() error {
	unlock, err := lock(s.lockPath())
	if err != nil {
		return err
	}
	defer unlock()
	if err := os.Remove(s.sessionPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// lock acquires an exclusive advisory lock on path, returning an unlock
// func. Cross-platform via gofrs/flock (flock on unix, LockFileEx on
// Windows).
func lock(path string) (func(), error) {
	fl := flock.New(path)
	if err := fl.Lock(); err != nil {
		return nil, fmt.Errorf("locking %s: %w", path, err)
	}
	return func() { _ = fl.Unlock() }, nil
}

// writeFileAtomic writes data to path atomically and durably
// (temp file in the same directory, fsync, rename) via natefinch/atomic.
// Fresh files are created mode 0600 (os.CreateTemp's default); existing
// files keep their permissions.
func writeFileAtomic(path string, data []byte) error {
	return atomic.WriteFile(path, bytes.NewReader(data))
}
