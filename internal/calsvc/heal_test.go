package calsvc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
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

func TestStaleCalendarList(t *testing.T) {
	inner := &fakeRawAPI{bodies: map[string]string{
		"/calendar/v1": `{"Calendars":[{"ID":"c1"}]}`,
	}}
	s := healService(t, inner)

	notFound := &papi.Error{Status: 404, Code: 2061, Message: "Calendar does not exist"}

	if s.staleCalendarList(notFound) {
		t.Fatal("list never served from cache; must decline")
	}

	var out json.RawMessage
	_ = s.api.Get(context.Background(), "/calendar/v1", nil, &out) // miss
	_ = s.api.Get(context.Background(), "/calendar/v1", nil, &out) // hit
	s.cals = []calendar.Info{{ID: "c1"}}

	if s.staleCalendarList(errors.New("plain network error")) {
		t.Fatal("non-API errors must not trigger a list refresh")
	}
	if s.staleCalendarList(&papi.Error{Status: 500, Code: 0, Message: "server"}) {
		t.Fatal("5xx must not trigger a list refresh")
	}
	if !s.staleCalendarList(notFound) {
		t.Fatal("404 with a cache-served list must invalidate and allow a retry")
	}
	if s.cals != nil {
		t.Error("stale-list heal must drop the in-memory list")
	}
	if s.staleCalendarList(notFound) {
		t.Error("second heal without new cache hits must decline")
	}
}
