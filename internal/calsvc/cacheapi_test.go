package calsvc

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/config"
)

// fakeRawAPI counts raw GETs and serves canned JSON bodies per path.
type fakeRawAPI struct {
	bodies map[string]string
	gets   map[string]int
}

func (f *fakeRawAPI) Get(_ context.Context, path string, _ url.Values, out any) error {
	if f.gets == nil {
		f.gets = make(map[string]int)
	}
	f.gets[path]++
	body, ok := f.bodies[path]
	if !ok {
		body = `{}`
	}
	if out != nil {
		return json.Unmarshal([]byte(body), out)
	}
	return nil
}

func (f *fakeRawAPI) Put(context.Context, string, any, any) error { return nil }

func newTestCache(t *testing.T) *config.Cache {
	t.Helper()
	t.Setenv(config.EnvConfigDir, t.TempDir())
	cache, err := config.OpenCache("test-scope")
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	return cache
}

func TestCacheTTLPolicy(t *testing.T) {
	cases := []struct {
		path     string
		wantTTL  time.Duration
		cachable bool
	}{
		{"/core/v4/users", keyMaterialTTL, true},
		{"/core/v4/addresses", keyMaterialTTL, true},
		{"/calendar/v1", calendarListTTL, true},
		{"/calendar/v2/CAL_ID/bootstrap", keyMaterialTTL, true},
		// Event content must never be cached.
		{"/calendar/v1/CAL_ID/events", 0, false},
		{"/calendar/v1/CAL_ID/events/EV_ID", 0, false},
		{"/calendar/v1/CAL_ID/events/sync", 0, false},
		// The old v1 key endpoints are no longer fetched/cached.
		{"/calendar/v1/CAL_ID/passphrase", 0, false},
		{"/calendar/v1/CAL_ID/keys", 0, false},
		{"/calendar/v1/CAL_ID/members", 0, false},
		{"/core/v4/keys/salts", 0, false},
	}
	for _, tc := range cases {
		ttl, ok := cacheTTL(tc.path)
		if ok != tc.cachable || ttl != tc.wantTTL {
			t.Errorf("cacheTTL(%q) = %v, %v; want %v, %v", tc.path, ttl, ok, tc.wantTTL, tc.cachable)
		}
	}
}

func TestCachedAPIReadThrough(t *testing.T) {
	inner := &fakeRawAPI{bodies: map[string]string{
		"/core/v4/users": `{"User":{"ID":"u1"}}`,
	}}
	api := newCachedAPI(inner, newTestCache(t), false)

	var out struct {
		User struct{ ID string }
	}
	for i := range 3 {
		if err := api.Get(context.Background(), "/core/v4/users", nil, &out); err != nil {
			t.Fatalf("Get %d: %v", i, err)
		}
		if out.User.ID != "u1" {
			t.Fatalf("Get %d: out = %+v", i, out)
		}
	}
	if inner.gets["/core/v4/users"] != 1 {
		t.Errorf("network GETs = %d, want 1 (cache must serve repeats)", inner.gets["/core/v4/users"])
	}
	if !api.servedAny("/core/v4/users") {
		t.Error("servedAny must report the cache hit")
	}
	if api.servedAny("/core/v4/addresses") {
		t.Error("servedAny must not report paths never served")
	}
}

func TestCachedAPISkipsUncacheableAndQueries(t *testing.T) {
	inner := &fakeRawAPI{bodies: map[string]string{
		"/calendar/v1/c1/events": `{"Events":[],"Total":0}`,
		"/calendar/v1":           `{"Calendars":[]}`,
	}}
	api := newCachedAPI(inner, newTestCache(t), false)

	var out json.RawMessage
	// Event content: never cached even with nil query.
	for range 2 {
		if err := api.Get(context.Background(), "/calendar/v1/c1/events", nil, &out); err != nil {
			t.Fatal(err)
		}
	}
	if inner.gets["/calendar/v1/c1/events"] != 2 {
		t.Errorf("event GETs = %d, want 2 (never cached)", inner.gets["/calendar/v1/c1/events"])
	}

	// A cacheable path with query params is not cached either.
	q := url.Values{"Page": {"0"}}
	for range 2 {
		if err := api.Get(context.Background(), "/calendar/v1", q, &out); err != nil {
			t.Fatal(err)
		}
	}
	if inner.gets["/calendar/v1"] != 2 {
		t.Errorf("parameterized GETs = %d, want 2", inner.gets["/calendar/v1"])
	}
}

func TestCachedAPIBypassReadStillPopulates(t *testing.T) {
	inner := &fakeRawAPI{bodies: map[string]string{
		"/calendar/v1": `{"Calendars":[{"ID":"c1"}]}`,
	}}
	cache := newTestCache(t)
	fresh := newCachedAPI(inner, cache, true)
	cached := newCachedAPI(inner, cache, false)

	var out json.RawMessage
	if err := fresh.Get(context.Background(), "/calendar/v1", nil, &out); err != nil {
		t.Fatal(err)
	}
	if err := fresh.Get(context.Background(), "/calendar/v1", nil, &out); err != nil {
		t.Fatal(err)
	}
	if inner.gets["/calendar/v1"] != 2 {
		t.Errorf("bypass-read GETs = %d, want 2 (always network)", inner.gets["/calendar/v1"])
	}
	if fresh.servedAny("/calendar/v1") {
		t.Error("bypass reads must not count as cache hits")
	}

	// The bypass fetches populated the cache for the caching reader.
	if err := cached.Get(context.Background(), "/calendar/v1", nil, &out); err != nil {
		t.Fatal(err)
	}
	if inner.gets["/calendar/v1"] != 2 {
		t.Errorf("GETs = %d, want 2 (cached reader must hit the bypass-written entry)", inner.gets["/calendar/v1"])
	}
}

func TestCachedAPIInvalidate(t *testing.T) {
	inner := &fakeRawAPI{bodies: map[string]string{
		"/core/v4/addresses": `{"Addresses":[]}`,
	}}
	api := newCachedAPI(inner, newTestCache(t), false)

	var out json.RawMessage
	_ = api.Get(context.Background(), "/core/v4/addresses", nil, &out) // miss -> network
	_ = api.Get(context.Background(), "/core/v4/addresses", nil, &out) // hit
	if !api.servedAny("/core/v4/addresses") {
		t.Fatal("expected a cache hit before invalidation")
	}

	api.invalidate("/core/v4/addresses")
	if api.servedAny("/core/v4/addresses") {
		t.Error("invalidate must clear the served record")
	}
	_ = api.Get(context.Background(), "/core/v4/addresses", nil, &out) // must refetch
	if inner.gets["/core/v4/addresses"] != 2 {
		t.Errorf("GETs = %d, want 2 (invalidate must force a refetch)", inner.gets["/core/v4/addresses"])
	}
}

func TestCachedAPIExpiry(t *testing.T) {
	inner := &fakeRawAPI{bodies: map[string]string{
		"/core/v4/users": `{"User":{"ID":"u1"}}`,
	}}
	api := newCachedAPI(inner, newTestCache(t), false)

	var out json.RawMessage
	_ = api.Get(context.Background(), "/core/v4/users", nil, &out)
	_ = api.Get(context.Background(), "/core/v4/users", nil, &out)
	if inner.gets["/core/v4/users"] != 1 {
		t.Fatalf("GETs = %d, want 1 before expiry", inner.gets["/core/v4/users"])
	}

	api.now = func() time.Time { return time.Now().Add(keyMaterialTTL + time.Hour) }
	_ = api.Get(context.Background(), "/core/v4/users", nil, &out)
	if inner.gets["/core/v4/users"] != 2 {
		t.Errorf("GETs = %d, want 2 (expired entry must refetch)", inner.gets["/core/v4/users"])
	}
}

func TestCacheKeyHelpers(t *testing.T) {
	for _, k := range accountKeyCacheKeys() {
		if _, ok := cacheTTL(k); !ok {
			t.Errorf("account key %q is not cacheable per policy", k)
		}
	}
	for _, k := range calendarKeyCacheKeys("CAL") {
		if _, ok := cacheTTL(k); !ok {
			t.Errorf("calendar key %q is not cacheable per policy", k)
		}
	}
}
