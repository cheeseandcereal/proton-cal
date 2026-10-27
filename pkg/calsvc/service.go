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
	"slices"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/cheeseandcereal/proton-cal/pkg/auth"
	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/config"
	"github.com/cheeseandcereal/proton-cal/pkg/event"
	"github.com/cheeseandcereal/proton-cal/pkg/papi"
)

// maxConcurrentCalendars bounds how many calendars are listed in parallel for a
// multi-calendar window. Each calendar already fans its own window out into
// chunks, so this caps the outer concurrency to avoid hammering the API.
const maxConcurrentCalendars = 4

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

// Calendars refreshes the calendar list and invalidates the cached default so
// subsequent reads reflect calendar and default changes in the Proton app.
func (s *Service) Calendars(ctx context.Context) ([]calendar.Info, error) {
	cals, err := s.fetchCalendars(ctx, s.freshAPI)
	if err == nil {
		s.invalidateCache(calendar.UserSettingsPath)
	}
	return cals, err
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

// resolveWith loads the cached calendar list (memory, then disk, then one
// fresh fetch) and applies resolve. A failed resolution or an unlisted default
// refreshes cached metadata once. Reading the default is non-fatal.
func resolveWith[T any](ctx context.Context, s *Service, needDefault bool, resolve func(cals []calendar.Info, defaultID string) (T, error)) (T, error) {
	var zero T
	s.calMu.Lock()
	cals := s.cals
	s.calMu.Unlock()

	fresh := false // whether cals is known to be fresh off the network
	if cals == nil {
		var err error
		cals, err = s.listCalendars(ctx)
		if err != nil {
			return zero, err
		}
		fresh = s.cacheAPI == nil || !s.cacheAPI.servedAny(calendar.APIPath)
	}

	defaultID := ""
	if needDefault {
		defaultID, _ = s.DefaultCalendarID(ctx)
	}

	out, err := resolve(cals, defaultID)
	missingDefault := defaultID != "" && !slices.ContainsFunc(cals, func(c calendar.Info) bool { return c.ID == defaultID })
	if !fresh && (err != nil || missingDefault) {
		refetched, ferr := s.Calendars(ctx)
		if ferr != nil {
			return zero, ferr
		}
		if needDefault {
			defaultID, _ = s.DefaultCalendarID(ctx)
		}
		out, err = resolve(refetched, defaultID)
	} else if missingDefault && s.cacheAPI != nil && s.cacheAPI.servedAny(calendar.UserSettingsPath) {
		s.invalidateCache(calendar.UserSettingsPath)
		defaultID, _ = s.DefaultCalendarID(ctx)
		out, err = resolve(cals, defaultID)
	}
	return out, err
}

// resolveCalendar resolves a selector ("" = server-side default, else first)
// against the cached list, refetching fresh once if a stale list fails.
func (s *Service) resolveCalendar(ctx context.Context, selector string) (calendar.Info, error) {
	return resolveWith(ctx, s, selector == "",
		func(cals []calendar.Info, defaultID string) (calendar.Info, error) {
			return calendar.Resolve(cals, selector, defaultID)
		})
}

// resolveCalendars resolves a set of selectors (or all calendars) to calendar
// infos, with the same stale-cache refetch fallback as resolveCalendar.
func (s *Service) resolveCalendars(ctx context.Context, selectors []string, all bool) ([]calendar.Info, error) {
	return resolveWith(ctx, s, !all && len(selectors) == 0,
		func(cals []calendar.Info, defaultID string) ([]calendar.Info, error) {
			if all {
				return calendar.ResolveAll(cals)
			}
			return calendar.ResolveMany(cals, selectors, defaultID)
		})
}

// listAcrossCalendars lists the same window for each calendar concurrently
// (bounded), then heals any calendar that came back degraded with a serial
// relist. A failure for any calendar fails the whole listing (no partial
// results). Healing is serial because it mutates the shared keychain; concurrent
// stale-key healing across calendars is rare (it follows key rotation on a
// cached account) and not worth the locking complexity.
func (s *Service) listAcrossCalendars(ctx context.Context, cals []calendar.Info, start, end int64, tz string) ([]calendarListing, error) {
	listings := make([]calendarListing, len(cals))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentCalendars)
	for i, info := range cals {
		g.Go(func() error {
			cl, err := s.listOneCalendar(gctx, info, start, end, tz)
			if err != nil {
				return err
			}
			listings[i] = cl
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Serial heal pass for any degraded calendar (stale cached keys).
	for i := range listings {
		if !listings[i].degraded || !s.healCalendarKeys(listings[i].info.ID) {
			continue
		}
		cl, err := s.listOneCalendar(ctx, listings[i].info, start, end, tz)
		if err != nil {
			return nil, err
		}
		listings[i] = cl
	}
	return listings, nil
}

// listOneCalendar unlocks one calendar's keys and lists the window. The
// degraded flag reports a partial-decrypt result (the caller may heal+retry).
func (s *Service) listOneCalendar(ctx context.Context, info calendar.Info, start, end int64, tz string) (calendarListing, error) {
	kc, err := s.keychainLazy(ctx)
	if err != nil {
		return calendarListing{}, err
	}
	access, err := s.unlockCalendar(ctx, info, kc)
	if err != nil {
		return calendarListing{}, err
	}
	listed, err := event.ListWindow(ctx, s.client, access.KR, info.ID, start, end, tz)
	if err != nil {
		return calendarListing{}, fmt.Errorf("listing events for calendar %q: %w", info.Name, err)
	}
	return calendarListing{
		info:     info,
		settings: access.Settings,
		listed:   listed,
		degraded: anyDecryptFailed(listed),
	}, nil
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
	acc, err := s.unlockCalendar(ctx, info, kc)
	return info, acc, err
}

func (s *Service) unlockCalendar(ctx context.Context, info calendar.Info, kc *calendar.Keychain) (*calendar.Access, error) {
	acc, err := kc.Unlock(ctx, info)
	if err != nil && ctx.Err() == nil && !papi.IsCode(err, papi.CodeNotFound) && s.healCalendarKeys(info.ID) {
		// The calendar key material came from cache and may be stale
		// (key rotation): refetch fresh and retry once.
		if kc, kerr := s.keychainLazy(ctx); kerr == nil {
			acc, err = kc.Unlock(ctx, info)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("unlocking calendar keys: %w", err)
	}
	return acc, nil
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
	if err != nil && ctx.Err() == nil && s.cacheAPI != nil && s.cacheAPI.servedAny(accountKeyCacheKeys()...) {
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

// invalidateCalendarList drops the list (disk + memory) and default setting so
// the next resolution reads consistent metadata after calendar changes.
func (s *Service) invalidateCalendarList() {
	s.invalidateCache(calendar.APIPath, calendar.UserSettingsPath)
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

// withAccess retries once for stale keys or a missing resource. Missing resources
// refresh calendar metadata, but only a different calendar ID permits replay.
func (s *Service) withAccess(ctx context.Context, selector string, fn func(info calendar.Info, access *calendar.Access) error) error {
	var failedID string
	var firstErr error
	for attempt := 0; ; attempt++ {
		info, access, err := s.access(ctx, selector)
		if failedID != "" && info.ID == failedID {
			return firstErr
		}
		if attempt > 0 && failedID == "" && err != nil {
			return firstErr
		}
		if err == nil {
			err = fn(info, access)
		}
		if attempt > 0 && failedID != "" && err != nil {
			return errors.Join(err, firstErr)
		}
		if attempt == 0 {
			switch {
			case info.ID != "" && papi.IsCode(err, papi.CodeNotFound):
				failedID, firstErr = info.ID, err
				s.invalidateCalendarList()
				continue
			case errors.Is(err, event.ErrDecryptDegraded) && s.healCalendarKeys(info.ID):
				firstErr = err
				continue
			}
		}
		return err
	}
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
	var selected calendar.Info
	attempt := 0
	werr := s.withAccess(ctx, selector, func(info calendar.Info, access *calendar.Access) error {
		attempt++
		res, degraded, err := fn(info, access)
		if err != nil {
			return err
		}
		out = res
		selected = info
		if attempt == 1 && degraded {
			return event.ErrDecryptDegraded
		}
		return nil
	})
	if werr != nil && (!errors.Is(werr, event.ErrDecryptDegraded) || out == nil) {
		return nil, werr
	}
	if selector == "" && out != nil {
		s.notify("Using calendar: " + selected.Name)
	}
	return out, nil
}

func (s *Service) notify(msg string) {
	if s.Notify != nil {
		s.Notify(msg)
	}
}
