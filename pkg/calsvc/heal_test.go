package calsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/config"
	"github.com/cheeseandcereal/proton-cal/pkg/papi"
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

// errCalendarGone is the API's answer to listing a deleted calendar's events.
var errCalendarGone = &papi.Error{Status: 422, Code: papi.CodeNotFound, Message: "This resource was not found"}

// A calendar deleted server-side still resolves from a cached list, so the
// listing fails with not-found; the list is refetched once (memory and disk)
// and the listing rerun on it. A long-running process holds the stale list in
// memory; a new one reads it from disk.
func TestWithCalendarsRefetchesStaleList(t *testing.T) {
	for _, source := range []string{"memory", "disk"} {
		t.Run(source, func(t *testing.T) {
			inner := &fakeRawAPI{bodies: map[string]string{
				calendar.APIPath: calListBody(t, [2]string{"id1", "Personal"}, [2]string{"id2", "Work"}),
			}}
			s := healService(t, inner)
			ctx := context.Background()

			if _, err := s.Calendars(ctx); err != nil {
				t.Fatalf("Calendars: %v", err)
			}
			inner.bodies[calendar.APIPath] = calListBody(t, [2]string{"id2", "Work"}) // "Personal" deleted
			if source == "disk" {
				s.cals = nil
			}

			var attempts [][]string
			cals, listed, err := withCalendars(ctx, s, nil, true, func(cals []calendar.Info) ([]string, error) {
				ids := calendarIDs(cals)
				attempts = append(attempts, ids)
				if slices.Contains(ids, "id1") {
					return nil, fmt.Errorf("listing events for calendar %q: %w", "Personal", errCalendarGone)
				}
				return ids, nil
			})
			if err != nil {
				t.Fatalf("withCalendars: %v", err)
			}
			if want := [][]string{{"id1", "id2"}, {"id2"}}; !reflect.DeepEqual(attempts, want) {
				t.Errorf("attempts = %v, want %v (stale list, then the refetched one)", attempts, want)
			}
			if got := calendarIDs(cals); !slices.Equal(got, []string{"id2"}) || !slices.Equal(listed, got) {
				t.Errorf("calendars = %v, result = %v; want both [id2]", got, listed)
			}
			if n := inner.gets[calendar.APIPath]; n != 2 {
				t.Errorf("list GETs = %d, want 2 (initial fetch + one refetch)", n)
			}
			if got := calendarIDs(s.cals); !slices.Equal(got, []string{"id2"}) {
				t.Errorf("in-memory list = %v, want the refetched [id2]", got)
			}
			if data, _, ok := s.cacheAPI.cache.Get(calendar.APIPath); !ok || strings.Contains(string(data), `"id1"`) {
				t.Errorf("disk cache = %s (present: %v), want the refetched list", data, ok)
			}
		})
	}
}

// Naming the deleted calendar also resolves against the stale list; after the
// refetch the selector fails resolution, which is the error reported.
func TestWithCalendarsStaleNamedCalendar(t *testing.T) {
	fake := &fakeRawAPI{bodies: map[string]string{
		calendar.APIPath: calListBody(t, [2]string{"id2", "Work"}),
	}}
	s := detachedWithAPI(config.Config{}, fake)
	s.cals = []calendar.Info{{ID: "id1", Name: "Personal"}, {ID: "id2", Name: "Work"}}

	attempts := 0
	_, _, err := withCalendars(context.Background(), s, []string{"Personal"}, false, func([]calendar.Info) (struct{}, error) {
		attempts++
		return struct{}{}, errCalendarGone
	})
	if err == nil || !strings.Contains(err.Error(), `no calendar with ID or name "Personal"`) {
		t.Fatalf("err = %v, want a resolution error naming the deleted calendar", err)
	}
	if attempts != 1 || fake.gets[calendar.APIPath] != 1 {
		t.Errorf("attempts = %d, list GETs = %d; want 1, 1", attempts, fake.gets[calendar.APIPath])
	}
}

// The refetch needs both a list that did not come fresh off the network and a
// not-found error, and happens at most once.
func TestWithCalendarsRefetchIsBounded(t *testing.T) {
	stale := []calendar.Info{{ID: "id1", Name: "Personal"}, {ID: "id2", Name: "Work"}}
	tests := []struct {
		name         string
		cached       []calendar.Info // in-memory list; nil = fetched off the network in the call
		err          error           // fn's error on every attempt
		wantAttempts int
		wantGETs     int
	}{
		{name: "not-found on a fresh list is final", err: errCalendarGone, wantAttempts: 1, wantGETs: 1},
		{name: "other errors are final", cached: stale, err: &papi.Error{Status: 500, Message: "server"}, wantAttempts: 1, wantGETs: 0},
		{name: "not-found after the refetch is final", cached: stale, err: errCalendarGone, wantAttempts: 2, wantGETs: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeRawAPI{bodies: map[string]string{
				calendar.APIPath: calListBody(t, [2]string{"id2", "Work"}),
			}}
			s := detachedWithAPI(config.Config{}, fake)
			s.cals = tt.cached

			attempts := 0
			_, _, err := withCalendars(context.Background(), s, nil, true, func([]calendar.Info) (struct{}, error) {
				attempts++
				return struct{}{}, tt.err
			})
			if !errors.Is(err, tt.err) {
				t.Errorf("err = %v, want %v", err, tt.err)
			}
			if attempts != tt.wantAttempts || fake.gets[calendar.APIPath] != tt.wantGETs {
				t.Errorf("attempts = %d, list GETs = %d; want %d, %d",
					attempts, fake.gets[calendar.APIPath], tt.wantAttempts, tt.wantGETs)
			}
		})
	}
}
