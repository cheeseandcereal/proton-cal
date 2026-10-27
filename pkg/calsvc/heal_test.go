package calsvc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
)

// healService builds a Service wired to a cachedAPI over a fake transport,
// good enough to exercise the self-healing decision logic.
func healService(t *testing.T, inner *fakeRawAPI) *Service {
	t.Helper()
	cacheAPI := newCachedAPI(inner, newTestCache(t), false)
	return &Service{api: cacheAPI, cacheAPI: cacheAPI, freshAPI: newCachedAPI(inner, cacheAPI.cache, true)}
}

func TestHealCalendarKeys(t *testing.T) {
	inner := &fakeRawAPI{bodies: map[string]string{
		"/core/v4/users": `{"User":{"ID":"u1"}}`,
	}}
	s := healService(t, inner)

	if s.healCalendarKeys("cal1") {
		t.Fatal("nothing served from cache yet; heal must decline")
	}

	// Serve users twice: miss then hit -> cache participated.
	var out json.RawMessage
	_ = s.api.Get(context.Background(), "/core/v4/users", nil, &out)
	_ = s.api.Get(context.Background(), "/core/v4/users", nil, &out)

	s.keychain = &calendar.Keychain{} // sentinel: heal must drop it
	if !s.healCalendarKeys("cal1") {
		t.Fatal("cached key material was served; heal must invalidate and retry")
	}
	if s.keychain != nil {
		t.Error("heal must drop the keychain so it rebuilds from fresh data")
	}
	if s.cacheAPI.servedAny(accountKeyCacheKeys()...) {
		t.Error("heal must clear the served record")
	}
	if s.healCalendarKeys("cal1") {
		t.Error("second heal without new cache hits must decline (no retry loops)")
	}
}

func TestHealCalendarKeysWithoutCache(t *testing.T) {
	s := &Service{} // cache disabled
	if s.healCalendarKeys("cal1") {
		t.Fatal("heal must decline when caching is disabled")
	}
}
