// Package calsvc is the shared service layer between the user-facing
// frontends (the CLI and the MCP server) and the calendar/event domain
// packages.
//
// It owns everything between "user-shaped strings" and domain calls:
// session bootstrap, key unlocking, calendar resolution and caching,
// datetime/occurrence parsing, recurrence-option validation, and the
// orchestration of list/create/update/delete operations. Frontends are
// reduced to argument binding and rendering.
package calsvc

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/cheeseandcereal/proton-cal/internal/auth"
	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/config"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
)

// Service bundles the authenticated state shared by calendar operations.
// Construct with New; a Service is safe for concurrent use (the MCP server
// shares one across tool calls).
type Service struct {
	cfg    config.Config
	client *papi.Client

	// Notify receives short human-readable notices (e.g. which calendar
	// was picked when no selector was given). Nil = silent.
	Notify func(msg string)

	mu       sync.Mutex
	cals     []calendar.Info // cached calendar list (nil until first fetch)
	keychain *calendar.Keychain
}

// New loads the config and restores an authenticated Service from the
// persisted session. A missing session yields a friendly "run login" error.
// Call Close when done.
func New() (*Service, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	store, err := config.NewSessionStore()
	if err != nil {
		return nil, fmt.Errorf("opening session store: %w", err)
	}
	client, err := papi.FromSession(store, cfg.EffectiveBaseURL())
	if errors.Is(err, config.ErrNoSession) {
		return nil, errors.New("not logged in; run `proton-cal login` first")
	} else if err != nil {
		return nil, fmt.Errorf("restoring session: %w", err)
	}
	return &Service{cfg: cfg, client: client}, nil
}

// NewDetached returns a Service with config only - no session or client.
// It exists for tests exercising the validation paths, which fail before
// any network use; domain operations on a detached Service will panic.
func NewDetached(cfg config.Config) *Service {
	return &Service{cfg: cfg}
}

// Close releases the underlying API client.
func (s *Service) Close() {
	if s.client != nil {
		s.client.Close()
	}
}

// EffectiveTimezone resolves a timezone: the explicit override when given,
// else the configured / system timezone.
func (s *Service) EffectiveTimezone(override string) string {
	if override != "" {
		return override
	}
	return s.cfg.EffectiveTimezone()
}

// DefaultCalendarSelector returns the configured default-calendar selector
// ("" when none), for rendering default markers.
func (s *Service) DefaultCalendarSelector() string {
	return s.cfg.DefaultCalendar
}

// Calendars fetches the calendar list (always fresh - a long-running MCP
// server must see calendars created after startup) and updates the cache
// used by selector resolution.
func (s *Service) Calendars(ctx context.Context) ([]calendar.Info, error) {
	cals, err := calendar.List(ctx, s.client)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cals = cals
	s.mu.Unlock()
	return cals, nil
}

// resolveCalendar resolves a calendar selector (ID or name; "" = the
// configured default calendar, else the first calendar) against the cached
// calendar list, refreshing the cache once when a cached list does not
// match (the calendar may have been created or renamed since).
func (s *Service) resolveCalendar(ctx context.Context, selector string) (calendar.Info, error) {
	s.mu.Lock()
	cals := s.cals
	s.mu.Unlock()

	cached := cals != nil
	if !cached {
		var err error
		cals, err = s.Calendars(ctx)
		if err != nil {
			return calendar.Info{}, err
		}
	}

	info, err := calendar.Resolve(cals, selector, s.cfg.DefaultCalendar)
	if err != nil && cached {
		cals, ferr := s.Calendars(ctx)
		if ferr != nil {
			return calendar.Info{}, ferr
		}
		info, err = calendar.Resolve(cals, selector, s.cfg.DefaultCalendar)
	}
	return info, err
}

// access resolves the calendar selector and unlocks that calendar's keys,
// unlocking the account keys on first use.
func (s *Service) access(ctx context.Context, selector string) (calendar.Info, *calendar.Access, error) {
	info, err := s.resolveCalendar(ctx, selector)
	if err != nil {
		return calendar.Info{}, nil, err
	}
	if selector == "" {
		s.notify("Using calendar: " + info.Name)
	}

	kc, err := s.keychainLazy(ctx)
	if err != nil {
		return calendar.Info{}, nil, err
	}
	acc, err := kc.Unlock(ctx, info)
	if err != nil {
		return calendar.Info{}, nil, fmt.Errorf("unlocking calendar keys: %w", err)
	}
	return info, acc, nil
}

// keychainLazy unlocks the account keys on first use and returns the
// session keychain.
func (s *Service) keychainLazy(ctx context.Context) (*calendar.Keychain, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keychain != nil {
		return s.keychain, nil
	}
	unlocked, err := auth.UnlockKeys(ctx, s.client)
	if err != nil {
		return nil, fmt.Errorf("unlocking keys: %w", err)
	}
	s.keychain = calendar.NewKeychain(s.client, unlocked)
	return s.keychain, nil
}

func (s *Service) notify(msg string) {
	if s.Notify != nil {
		s.Notify(msg)
	}
}
