package calsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sync"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/auth"
	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/config"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
)

// Cache TTLs. Liberal by design: the cached responses are bootstrap data
// whose validity is verified cryptographically by use - stale key material
// fails loudly and self-heals (invalidate + refetch + retry), and calendar
// resolution refreshes on miss. Event content is NEVER cached.
const (
	// keyMaterialTTL covers users, addresses, calendar passphrases, keys
	// and members: effectively immutable outside password resets / key
	// rotations, all of which surface as unlock or decrypt failures.
	keyMaterialTTL = 30 * 24 * time.Hour
	// calendarListTTL covers GET /calendar/v1. Staleness is cosmetic or
	// recoverable: unknown selectors refresh the list, and `calendars`
	// always fetches fresh.
	calendarListTTL = 7 * 24 * time.Hour
)

var calKeyMaterialRe = regexp.MustCompile(`^` + calendar.APIPath + `/[^/]+/(passphrase|keys|members)$`)

// cacheTTL returns the TTL for a cacheable GET path; ok is false for
// everything that must never be cached (event content, writes, anything
// unrecognized).
func cacheTTL(path string) (time.Duration, bool) {
	switch {
	case path == auth.UsersPath || path == auth.AddressesPath:
		return keyMaterialTTL, true
	case path == calendar.APIPath:
		return calendarListTTL, true
	case calKeyMaterialRe.MatchString(path):
		return keyMaterialTTL, true
	}
	return 0, false
}

// accountKeyCacheKeys are the cache keys behind the account key unlock.
func accountKeyCacheKeys() []string {
	return []string{auth.UsersPath, auth.AddressesPath}
}

// calendarKeyCacheKeys are the cache keys behind one calendar's key unlock.
func calendarKeyCacheKeys(calendarID string) []string {
	return []string{
		calendar.APIPath + "/" + calendarID + "/passphrase",
		calendar.APIPath + "/" + calendarID + "/keys",
		calendar.APIPath + "/" + calendarID + "/members",
	}
}

// cachedAPI decorates a papi.API with a read-through response cache for
// the bootstrap endpoints. Only parameterless GETs on recognized paths are
// cached; Put always passes through. It records which keys were served
// from cache during this run, so self-healing only retries when stale
// cached data could actually be the cause of a failure.
type cachedAPI struct {
	inner      papi.API
	cache      *config.Cache
	bypassRead bool // fetch fresh but still populate the cache
	now        func() time.Time

	mu     sync.Mutex
	served map[string]bool
}

func newCachedAPI(inner papi.API, cache *config.Cache, bypassRead bool) *cachedAPI {
	return &cachedAPI{inner: inner, cache: cache, bypassRead: bypassRead, now: time.Now, served: make(map[string]bool)}
}

func (a *cachedAPI) Get(ctx context.Context, path string, query url.Values, out any) error {
	ttl, cacheable := cacheTTL(path)
	if !cacheable || len(query) > 0 {
		return a.inner.Get(ctx, path, query, out)
	}

	if !a.bypassRead {
		if data, fetchedAt, ok := a.cache.Get(path); ok && a.now().Sub(fetchedAt) < ttl {
			if err := unmarshalTo(data, out); err == nil {
				a.mu.Lock()
				a.served[path] = true
				a.mu.Unlock()
				return nil
			}
			// Undecodable entry: treat as a miss and refetch.
		}
	}

	var raw json.RawMessage
	if err := a.inner.Get(ctx, path, query, &raw); err != nil {
		return err
	}
	a.cache.Put(path, raw)
	return unmarshalTo(raw, out)
}

func (a *cachedAPI) Put(ctx context.Context, path string, body, out any) error {
	return a.inner.Put(ctx, path, body, out)
}

func unmarshalTo(data json.RawMessage, out any) error {
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decoding cached/raw response: %w", err)
	}
	return nil
}

// servedAny reports whether any of keys was served from cache during this
// run (i.e. stale cached data could explain a downstream failure).
func (a *cachedAPI) servedAny(keys ...string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, k := range keys {
		if a.served[k] {
			return true
		}
	}
	return false
}

// invalidate drops keys from the cache and forgets that they were served,
// so a subsequent fetch hits the network (and a second failure is final).
func (a *cachedAPI) invalidate(keys ...string) {
	a.cache.Delete(keys...)
	a.mu.Lock()
	for _, k := range keys {
		delete(a.served, k)
	}
	a.mu.Unlock()
}
