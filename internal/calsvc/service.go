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

	"github.com/cheeseandcereal/proton-cal/internal/auth"
	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/config"
	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
)

// Service bundles the authenticated state shared by calendar operations.
// Construct with New; a Service is safe for concurrent use (the MCP server
// shares one across tool calls).
type Service struct {
	cfg    config.Config
	client *papi.Client

	// api is the read path for bootstrap endpoints: the caching decorator
	// when the cache is enabled, else the raw client. cacheAPI is the
	// same decorator (nil when caching is disabled); freshAPI bypasses
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

// New loads the config and restores an authenticated Service from the
// persisted session. A missing session yields a friendly "run login" error.
// noCache disables the on-disk bootstrap cache (fetches stay fresh; the
// cache file is neither read nor written). Call Close when done.
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

// Calendars fetches the calendar list fresh (the user asked for the truth:
// a long-running MCP server must see calendars created after startup, and
// `proton-cal calendars` is the natural cache-staleness escape hatch). The
// result still updates both the in-memory and on-disk caches.
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

// resolveCalendar resolves a calendar selector (ID or name; "" = the
// configured default calendar, else the first calendar) against the cached
// calendar list (in-memory, then disk), falling back to one fresh fetch
// when a cached list does not match (the calendar may have been created or
// renamed since).
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

	info, err := calendar.Resolve(cals, selector, s.cfg.DefaultCalendar)
	if err != nil && !fresh {
		// The list came from memory or disk; it may be stale. One fresh
		// fetch, then the resolution error is final.
		cals, ferr := s.Calendars(ctx)
		if ferr != nil {
			return calendar.Info{}, ferr
		}
		info, err = calendar.Resolve(cals, selector, s.cfg.DefaultCalendar)
	}
	return info, err
}

// access resolves the calendar selector and unlocks that calendar's keys,
// unlocking the account keys on first use. Calendar resolution and the
// account-key unlock are independent (different endpoints) and run
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

// keychainLazy unlocks the account keys on first use and returns the
// session keychain. When the unlock fails and the key material was served
// from cache, it invalidates and retries once with fresh data (password
// changes rotate the account keys).
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

// healCalendarKeys reports whether a failed calendar-key operation is
// worth retrying because cached key material may be stale; when so it
// invalidates that material (account + calendar tiers - liberal: at most
// one extra bootstrap) and drops the keychain so the retry rebuilds it
// from fresh responses.
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

// staleCalendarList reports whether err plausibly means the operation hit
// a calendar that no longer exists while the calendar list was served from
// cache (deleted and possibly recreated elsewhere); when so it invalidates
// the cached list so a retry re-resolves freshly.
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

// withAccess runs fn with resolved calendar access, retrying once when the
// failure is plausibly caused by stale cached bootstrap data:
//
//   - fn failed with event.ErrDecryptDegraded and the key material was
//     cached: refetch keys, rebuild access, retry.
//   - fn failed with a 404/422 API error and the calendar list was cached:
//     refresh the list; retry only when the selector now resolves to a
//     DIFFERENT calendar (deleted + recreated elsewhere), since otherwise
//     the failure had nothing to do with staleness.
//
// Fresh-data failures are final: no retry loops.
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

// withAccessResult runs fn with resolved calendar access and returns its
// result, plumbing the common "out pointer + retry" boilerplate. fn returns
// the result value plus a degraded flag: when degraded is true on the first
// attempt, the result is provisionally kept but ErrDecryptDegraded is raised
// so withAccess heals stale keys and retries; a still-degraded second pass is
// accepted and its (best-effort) result returned. Non-degrade errors are
// final.
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
