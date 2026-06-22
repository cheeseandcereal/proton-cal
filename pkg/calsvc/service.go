// Package calsvc is the shared service layer between the user-facing
// frontends (the CLI and the MCP server) and the calendar/event domain
// packages.
//
// It owns everything between "user-shaped strings" and domain calls:
// session bootstrap, key unlocking, calendar resolution and caching,
// datetime/occurrence parsing, recurrence-option validation, and the
// orchestration of list/create/update/delete operations. Frontends are
// reduced to argument binding and rendering.
//
// Bootstrap responses (key material and the calendar list - never event
// content) are cached on disk with liberal TTLs; stale entries are
// self-healing because key material fails cryptographically when wrong
// (see cacheapi.go).
package calsvc

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/cheeseandcereal/proton-cal/pkg/auth"
	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/config"
	"github.com/cheeseandcereal/proton-cal/pkg/event"
	"github.com/cheeseandcereal/proton-cal/pkg/papi"
)

// Service bundles the authenticated state shared by calendar operations.
// Construct with New; safe for concurrent use (the MCP server shares one).
type Service struct {
	cfg    config.Config
	client *papi.Client

	// api is the bootstrap read path (caching decorator when enabled, else raw
	// client). cacheAPI is that decorator (nil if disabled); freshAPI bypasses
	// cache reads but still populates the cache.
	api      papi.API
	cacheAPI *cachedAPI
	freshAPI papi.API

	// Notify receives short human-readable notices (e.g. which calendar
	// was picked when no selector was given). Nil = silent.
	Notify func(msg string)

	calMu sync.Mutex
	cals  []calendar.Info // cached calendar list (nil until first fetch)

	kcMu     sync.Mutex
	keychain *calendar.Keychain
}

// New restores an authenticated Service from the persisted session (missing
// session yields a "run login" error). noCache disables the on-disk bootstrap
// cache. Call Close when done.
func New(noCache bool) (*Service, error) {
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

	s := &Service{cfg: cfg, client: client, api: client, freshAPI: client}
	if !noCache {
		// A broken cache must never break the app: fall back to uncached.
		if uid, err := client.SessionUID(); err == nil {
			if cache, err := config.OpenCache(uid + "|" + cfg.EffectiveBaseURL()); err == nil {
				s.cacheAPI = newCachedAPI(client, cache, false)
				s.api = s.cacheAPI
				s.freshAPI = newCachedAPI(client, cache, true)
			}
		}
	}
	return s, nil
}

// NewDetached returns a Service with config only - no session or client. For
// tests exercising validation paths; domain operations on it will panic.
func NewDetached(cfg config.Config) *Service {
	return &Service{cfg: cfg}
}

// NewWithAPI builds a Service whose reads are served by the given papi.API (both
// cached and fresh path), with no session client. A test seam for resolution and
// read-only ops; operations needing the *papi.Client (key unlock) will panic.
func NewWithAPI(cfg config.Config, api papi.API) *Service {
	return &Service{cfg: cfg, api: api, freshAPI: api}
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

// DefaultCalendarID returns the account's server-side default calendar ID
// (source of truth for default markers and unspecified selectors); cached fetch,
// empty = no default set.
func (s *Service) DefaultCalendarID(ctx context.Context) (string, error) {
	return calendar.DefaultCalendarID(ctx, s.api)
}

// Calendars fetches the calendar list fresh (so a long-running MCP server sees
// newly created calendars; the cache-staleness escape hatch), still updating both caches.
func (s *Service) Calendars(ctx context.Context) ([]calendar.Info, error) {
	return s.fetchCalendars(ctx, s.freshAPI)
}

// listCalendars returns the calendar list for selector resolution, served
// from the on-disk cache when fresh enough.
func (s *Service) listCalendars(ctx context.Context) ([]calendar.Info, error) {
	return s.fetchCalendars(ctx, s.api)
}

func (s *Service) fetchCalendars(ctx context.Context, api papi.API) ([]calendar.Info, error) {
	cals, err := calendar.List(ctx, api)
	if err != nil {
		return nil, err
	}
	s.calMu.Lock()
	s.cals = cals
	s.calMu.Unlock()
	return cals, nil
}

// resolveCalendar resolves a selector ("" = server-side default, else first)
// against the cached list (in-memory, then disk), falling back to one fresh
// fetch when a cached list does not match (calendar created/renamed since).
func (s *Service) resolveCalendar(ctx context.Context, selector string) (calendar.Info, error) {
	s.calMu.Lock()
	cals := s.cals
	s.calMu.Unlock()

	fresh := false // whether cals is known to be fresh off the network
	if cals == nil {
		var err error
		cals, err = s.listCalendars(ctx)
		if err != nil {
			return calendar.Info{}, err
		}
		fresh = s.cacheAPI == nil || !s.cacheAPI.servedAny(calendar.APIPath)
	}

	// The server default is consulted only when no selector is given. A failure
	// to read it is non-fatal — Resolve then falls back to the first calendar.
	defaultID := ""
	if selector == "" {
		defaultID, _ = s.DefaultCalendarID(ctx)
	}

	info, err := calendar.Resolve(cals, selector, defaultID)
	if err != nil && !fresh {
		// The list came from memory or disk; it may be stale. One fresh
		// fetch, then the resolution error is final.
		cals, ferr := s.Calendars(ctx)
		if ferr != nil {
			return calendar.Info{}, ferr
		}
		info, err = calendar.Resolve(cals, selector, defaultID)
	}
	return info, err
}

// access resolves the selector and unlocks that calendar's keys (account keys
// on first use). Resolution and the account-key unlock are independent and run
// concurrently; the calendar key unlock then needs both.
func (s *Service) access(ctx context.Context, selector string) (calendar.Info, *calendar.Access, error) {
	var info calendar.Info
	var kc *calendar.Keychain

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		var err error
		info, err = s.resolveCalendar(gctx, selector)
		return err
	})
	g.Go(func() error {
		var err error
		kc, err = s.keychainLazy(gctx)
		return err
	})
	if err := g.Wait(); err != nil {
		return calendar.Info{}, nil, err
	}
	if selector == "" {
		s.notify("Using calendar: " + info.Name)
	}

	acc, err := kc.Unlock(ctx, info)
	if err != nil && s.healCalendarKeys(info.ID) {
		// The calendar key material came from cache and may be stale
		// (key rotation): refetch fresh and retry once.
		if kc, kerr := s.keychainLazy(ctx); kerr == nil {
			acc, err = kc.Unlock(ctx, info)
		}
	}
	if err != nil {
		return calendar.Info{}, nil, fmt.Errorf("unlocking calendar keys: %w", err)
	}
	return info, acc, nil
}

// keychainLazy unlocks the account keys on first use and returns the keychain.
// On unlock failure with cached key material, invalidates and retries once with
// fresh data (password changes rotate the account keys).
func (s *Service) keychainLazy(ctx context.Context) (*calendar.Keychain, error) {
	s.kcMu.Lock()
	defer s.kcMu.Unlock()
	if s.keychain != nil {
		return s.keychain, nil
	}
	unlocked, err := auth.UnlockKeys(ctx, s.client.Store(), s.api)
	if err != nil && s.cacheAPI != nil && s.cacheAPI.servedAny(accountKeyCacheKeys()...) {
		s.cacheAPI.invalidate(accountKeyCacheKeys()...)
		unlocked, err = auth.UnlockKeys(ctx, s.client.Store(), s.api)
	}
	if err != nil {
		return nil, fmt.Errorf("unlocking keys: %w", err)
	}
	s.keychain = calendar.NewKeychain(s.api, unlocked)
	return s.keychain, nil
}

// healCalendarKeys reports whether a failed calendar-key op is worth retrying
// because cached key material may be stale; when so it invalidates that material
// (account + calendar tiers) and drops the keychain so the retry rebuilds fresh.
func (s *Service) healCalendarKeys(calendarID string) bool {
	if s.cacheAPI == nil {
		return false
	}
	keys := append(accountKeyCacheKeys(), calendarKeyCacheKeys(calendarID)...)
	if !s.cacheAPI.servedAny(keys...) {
		return false
	}
	s.cacheAPI.invalidate(keys...)
	s.kcMu.Lock()
	s.keychain = nil
	s.kcMu.Unlock()
	return true
}

// staleCalendarList reports whether err (a 404/422) plausibly means a cached
// calendar no longer exists; when so it invalidates the cached list so a retry
// re-resolves freshly.
func (s *Service) staleCalendarList(err error) bool {
	if s.cacheAPI == nil || !s.cacheAPI.servedAny(calendar.APIPath) {
		return false
	}
	var pe *papi.Error
	if !errors.As(err, &pe) || (pe.Status != 404 && pe.Status != 422) {
		return false
	}
	s.cacheAPI.invalidate(calendar.APIPath)
	s.calMu.Lock()
	s.cals = nil
	s.calMu.Unlock()
	return true
}

// invalidateCache drops the given cache keys (no-op when caching is
// disabled). Used after a write so a subsequent read reflects the change.
func (s *Service) invalidateCache(keys ...string) {
	if s.cacheAPI != nil {
		s.cacheAPI.invalidate(keys...)
	}
}

// invalidateCalendarKeys drops the cached Access and bootstrap response for one
// calendar so a subsequent unlock/settings read re-fetches fresh.
func (s *Service) invalidateCalendarKeys(calendarID string) {
	s.invalidateCache(calendar.BootstrapPath(calendarID))
	s.kcMu.Lock()
	kc := s.keychain
	s.kcMu.Unlock()
	if kc != nil {
		kc.Invalidate(calendarID)
	}
}

// invalidateCalendarList drops the cached calendar list (disk + in-memory) so the
// next resolution re-fetches it (e.g. after a name/color change or deletion).
func (s *Service) invalidateCalendarList() {
	s.invalidateCache(calendar.APIPath)
	s.calMu.Lock()
	s.cals = nil
	s.calMu.Unlock()
}

// loginUsername resolves the username for SRP re-authentication: the persisted
// config value, else the account name from the live user record.
func (s *Service) loginUsername(ctx context.Context) (string, error) {
	if s.cfg.Username != "" {
		return s.cfg.Username, nil
	}
	user, err := s.client.Proton().GetUser(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving account username: %w", err)
	}
	if user.Name == "" {
		return "", errors.New("could not determine login username; re-run `proton-cal login`")
	}
	return user.Name, nil
}

// withAccess runs fn with resolved access, retrying once on stale cached data:
// ErrDecryptDegraded with cached keys refetches keys and retries; a 404/422 with
// a cached list refreshes it and retries only if the selector now resolves to a
// DIFFERENT calendar. Fresh-data failures are final.
func (s *Service) withAccess(ctx context.Context, selector string, fn func(info calendar.Info, access *calendar.Access) error) error {
	info, access, err := s.access(ctx, selector)
	if err != nil {
		return err
	}
	err = fn(info, access)
	if err == nil {
		return nil
	}

	requireNewID := false
	switch {
	case errors.Is(err, event.ErrDecryptDegraded) && s.healCalendarKeys(info.ID):
	case s.staleCalendarList(err):
		requireNewID = true
	default:
		return err
	}

	info2, access2, err2 := s.access(ctx, selector)
	if err2 != nil || (requireNewID && info2.ID == info.ID) {
		return err // the original error stands
	}
	return fn(info2, access2)
}

// withAccessResult runs fn with resolved access and returns its result. fn's
// degraded flag on the first attempt raises ErrDecryptDegraded so withAccess
// heals keys and retries; a still-degraded second pass is accepted best-effort.
func withAccessResult[T any](
	ctx context.Context,
	s *Service,
	selector string,
	fn func(info calendar.Info, access *calendar.Access) (*T, bool, error),
) (*T, error) {
	var out *T
	attempt := 0
	werr := s.withAccess(ctx, selector, func(info calendar.Info, access *calendar.Access) error {
		attempt++
		res, degraded, err := fn(info, access)
		if err != nil {
			return err
		}
		out = res
		if attempt == 1 && degraded {
			return event.ErrDecryptDegraded
		}
		return nil
	})
	if werr != nil && (!errors.Is(werr, event.ErrDecryptDegraded) || out == nil) {
		return nil, werr
	}
	return out, nil
}

func (s *Service) notify(msg string) {
	if s.Notify != nil {
		s.Notify(msg)
	}
}
