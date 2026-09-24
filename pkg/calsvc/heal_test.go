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

	"github.com/cheeseandcereal/proton-cal/pkg/auth"
	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/config"
	"github.com/cheeseandcereal/proton-cal/pkg/event"
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

// errCalendarGone is the API's answer to listing a deleted calendar's events.
var errCalendarGone = &papi.Error{Status: 422, Code: papi.CodeNotFound, Message: "This resource was not found"}

// A deleted calendar's bootstrap fetch fails with not-found, which fresh key
// material cannot fix: access keeps the keychain and the cached account keys,
// and returns the calendar and the list's freshness for withAccess to act on.
func TestAccessNotFoundKeepsKeys(t *testing.T) {
	inner := &fakeRawAPI{
		bodies: map[string]string{
			calendar.APIPath: calListBody(t, [2]string{"id1", "Personal"}),
			auth.UsersPath:   `{"User":{"ID":"u1"}}`,
		},
		errs: map[string]error{calendar.BootstrapPath("id1"): errCalendarGone},
	}
	s := healService(t, inner)
	ctx := context.Background()

	if _, err := s.Calendars(ctx); err != nil { // held in memory from here on
		t.Fatalf("Calendars: %v", err)
	}
	var out json.RawMessage
	_ = s.api.Get(ctx, auth.UsersPath, nil, &out) // miss
	_ = s.api.Get(ctx, auth.UsersPath, nil, &out) // hit: a key heal would be allowed
	kc := calendar.NewKeychain(s.api, &auth.Unlocked{})
	s.keychain = kc

	info, _, fresh, err := s.access(ctx, "Personal")
	if !papi.IsCode(err, papi.CodeNotFound) {
		t.Fatalf("err = %v, want the not-found", err)
	}
	if info.ID != "id1" || fresh {
		t.Errorf("calendar = %q, fresh = %v; want id1 from the in-memory (not fresh) list", info.ID, fresh)
	}
	if s.keychain != kc || !s.cacheAPI.servedAny(auth.UsersPath) {
		t.Error("a not-found must not drop the keychain or the cached account keys")
	}
	if n := inner.gets[calendar.BootstrapPath("id1")]; n != 1 {
		t.Errorf("bootstrap GETs = %d, want 1 (no unlock retry)", n)
	}
}

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

// With no selector, the listed calendar came from the cached default calendar
// ID, which names the deleted calendar too: the retry refetches it with the
// list and lists the account's new default, not the first calendar.
func TestWithCalendarsRefetchesStaleDefault(t *testing.T) {
	inner := &fakeRawAPI{bodies: map[string]string{
		calendar.APIPath:          calListBody(t, [2]string{"id2", "Work"}, [2]string{"id3", "Family"}),
		calendar.UserSettingsPath: userSettingsBody("id1"),
	}}
	s := healService(t, inner)
	ctx := context.Background()

	if _, err := s.DefaultCalendarID(ctx); err != nil { // cached, naming id1
		t.Fatalf("DefaultCalendarID: %v", err)
	}
	inner.bodies[calendar.UserSettingsPath] = userSettingsBody("id3") // "Personal" deleted, default moved
	s.cals = []calendar.Info{{ID: "id1", Name: "Personal"}, {ID: "id2", Name: "Work"}, {ID: "id3", Name: "Family"}}

	cals, _, err := withCalendars(ctx, s, nil, false, func(cals []calendar.Info) (struct{}, error) {
		if slices.Contains(calendarIDs(cals), "id1") {
			return struct{}{}, errCalendarGone
		}
		return struct{}{}, nil
	})
	if err != nil {
		t.Fatalf("withCalendars: %v", err)
	}
	if got := calendarIDs(cals); !slices.Equal(got, []string{"id3"}) {
		t.Errorf("calendars = %v, want the new default [id3]", got)
	}
	if n := inner.gets[calendar.UserSettingsPath]; n != 2 {
		t.Errorf("default GETs = %d, want 2 (initial fetch + one refetch)", n)
	}
}

// fakeAccess stands in for Service.access, whose key unlock needs a session: it
// resolves the selector for real and unlocks every calendar except those in
// gone, whose bootstrap fetch fails as a deleted calendar's does.
func fakeAccess(s *Service, gone ...string) accessFunc {
	return func(ctx context.Context, selector string) (calendar.Info, *calendar.Access, bool, error) {
		info, fresh, err := s.resolveCalendar(ctx, selector)
		if err != nil {
			return calendar.Info{}, nil, false, err
		}
		if slices.Contains(gone, info.ID) {
			return info, nil, fresh, fmt.Errorf("unlocking calendar keys: %w", errCalendarGone)
		}
		return info, &calendar.Access{CalendarID: info.ID}, fresh, nil
	}
}

// The account's default calendar is deleted and the default moves to another
// calendar, but a list that did not come fresh off the network still resolves
// the empty selector to the deleted calendar. Its not-found, from the key
// unlock (bootstrap not cached) or from fn (bootstrap cached), drops the list
// and the cached default, and fn runs on the new default. A long-running
// process holds the stale list in memory; a new one reads it from disk.
func TestWithAccessRefetchesStaleDefault(t *testing.T) {
	for _, source := range []string{"memory", "disk"} {
		for _, failing := range []string{"unlock", "fn"} {
			t.Run(source+"/"+failing, func(t *testing.T) {
				inner := &fakeRawAPI{bodies: map[string]string{
					calendar.APIPath: calListBody(t,
						[2]string{"id1", "Personal"}, [2]string{"id2", "Work"}, [2]string{"id3", "Family"}),
					calendar.UserSettingsPath: userSettingsBody("id1"),
				}}
				s := healService(t, inner)
				var notices []string
				s.Notify = func(msg string) { notices = append(notices, msg) }
				ctx := context.Background()

				if _, err := s.Calendars(ctx); err != nil {
					t.Fatalf("Calendars: %v", err)
				}
				if _, err := s.DefaultCalendarID(ctx); err != nil {
					t.Fatalf("DefaultCalendarID: %v", err)
				}
				// "Personal" deleted; the new default is not the first calendar.
				inner.bodies[calendar.APIPath] = calListBody(t, [2]string{"id2", "Work"}, [2]string{"id3", "Family"})
				inner.bodies[calendar.UserSettingsPath] = userSettingsBody("id3")
				if source == "disk" {
					s.cals = nil
				}
				var gone []string
				want := []string{"id1", "id3"}
				if failing == "unlock" {
					gone, want = []string{"id1"}, []string{"id3"}
				}

				var attempts []string
				err := s.withAccess(ctx, fakeAccess(s, gone...), "", func(info calendar.Info, _ *calendar.Access) error {
					attempts = append(attempts, info.ID)
					if info.ID == "id1" {
						return fmt.Errorf("creating event: %w", errCalendarGone)
					}
					return nil
				})
				if err != nil {
					t.Fatalf("withAccess: %v", err)
				}
				if !slices.Equal(attempts, want) {
					t.Errorf("fn ran on %v, want %v", attempts, want)
				}
				if l, d := inner.gets[calendar.APIPath], inner.gets[calendar.UserSettingsPath]; l != 2 || d != 2 {
					t.Errorf("list GETs = %d, default GETs = %d; want 2, 2 (initial fetch + one refetch)", l, d)
				}
				if want := []string{"Using calendar: Family"}; !slices.Equal(notices, want) {
					t.Errorf("notices = %q, want %q (the deleted calendar is never announced)", notices, want)
				}
			})
		}
	}
}

// Without the disk cache (--no-cache), a long-running process still holds the
// list it fetched earlier in memory, and that list is not fresh either. "Work"
// was deleted and recreated, so after the refetch the name resolves to a new
// calendar and fn reruns there; a named selector leaves the default alone.
func TestWithAccessRefetchesStaleListWithoutCache(t *testing.T) {
	fake := &fakeRawAPI{bodies: map[string]string{
		calendar.APIPath: calListBody(t, [2]string{"id1", "Personal"}, [2]string{"id2", "Work"}),
	}}
	s := detachedWithAPI(config.Config{}, fake)
	ctx := context.Background()

	if _, err := s.Calendars(ctx); err != nil {
		t.Fatalf("Calendars: %v", err)
	}
	fake.bodies[calendar.APIPath] = calListBody(t, [2]string{"id1", "Personal"}, [2]string{"id4", "Work"})

	var attempts []string
	err := s.withAccess(ctx, fakeAccess(s), "Work", func(info calendar.Info, _ *calendar.Access) error {
		attempts = append(attempts, info.ID)
		if info.ID == "id2" {
			return errCalendarGone
		}
		return nil
	})
	if err != nil {
		t.Fatalf("withAccess: %v", err)
	}
	if !slices.Equal(attempts, []string{"id2", "id4"}) {
		t.Errorf("fn ran on %v, want [id2 id4]", attempts)
	}
	if l, d := fake.gets[calendar.APIPath], fake.gets[calendar.UserSettingsPath]; l != 2 || d != 0 {
		t.Errorf("list GETs = %d, default GETs = %d; want 2, 0", l, d)
	}
}

// Naming a deleted calendar resolves against the stale list too; after the
// refetch the selector fails resolution, which is the error reported, as with
// withCalendars.
func TestWithAccessStaleNamedCalendar(t *testing.T) {
	for _, failing := range []string{"unlock", "fn"} {
		t.Run(failing, func(t *testing.T) {
			fake := &fakeRawAPI{bodies: map[string]string{
				calendar.APIPath: calListBody(t, [2]string{"id2", "Work"}),
			}}
			s := detachedWithAPI(config.Config{}, fake)
			s.cals = []calendar.Info{{ID: "id1", Name: "Personal"}, {ID: "id2", Name: "Work"}}
			var gone []string
			wantAttempts := 1
			if failing == "unlock" {
				gone, wantAttempts = []string{"id1"}, 0
			}

			attempts := 0
			err := s.withAccess(context.Background(), fakeAccess(s, gone...), "Personal", func(calendar.Info, *calendar.Access) error {
				attempts++
				return errCalendarGone
			})
			if err == nil || !strings.Contains(err.Error(), `no calendar with ID or name "Personal"`) {
				t.Fatalf("err = %v, want a resolution error naming the deleted calendar", err)
			}
			if attempts != wantAttempts || fake.gets[calendar.APIPath] != 1 {
				t.Errorf("attempts = %d, list GETs = %d; want %d, 1", attempts, fake.gets[calendar.APIPath], wantAttempts)
			}
		})
	}
}

// The refetch needs both a list that did not come fresh off the network and a
// not-found error, happens at most once, and reruns fn only on a different
// calendar: when the selector still resolves to the calendar that failed (the
// not-found was about something else, e.g. an event), the original error stands.
func TestWithAccessRefetchIsBounded(t *testing.T) {
	stale := []calendar.Info{{ID: "id1", Name: "Personal"}, {ID: "id2", Name: "Work"}}
	tests := []struct {
		name         string
		cached       []calendar.Info // in-memory list; nil = fetched off the network in the call
		selector     string
		err          error // fn's error on every attempt
		wantAttempts []string
		wantGETs     int
	}{
		{name: "not-found on a fresh list is final", selector: "Work", err: errCalendarGone,
			wantAttempts: []string{"id2"}, wantGETs: 1},
		{name: "other errors are final", cached: stale, selector: "Work", err: &papi.Error{Status: 500, Message: "server"},
			wantAttempts: []string{"id2"}, wantGETs: 0},
		{name: "the same calendar keeps the original error", cached: stale, selector: "Work", err: errCalendarGone,
			wantAttempts: []string{"id2"}, wantGETs: 1},
		{name: "not-found after the retry is final", cached: stale, err: errCalendarGone,
			wantAttempts: []string{"id1", "id2"}, wantGETs: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeRawAPI{bodies: map[string]string{
				calendar.APIPath: calListBody(t, [2]string{"id2", "Work"}),
			}}
			s := detachedWithAPI(config.Config{}, fake)
			s.cals = tt.cached

			var attempts []string
			err := s.withAccess(context.Background(), fakeAccess(s), tt.selector, func(info calendar.Info, _ *calendar.Access) error {
				attempts = append(attempts, info.ID)
				return tt.err
			})
			if !errors.Is(err, tt.err) {
				t.Errorf("err = %v, want %v", err, tt.err)
			}
			if !slices.Equal(attempts, tt.wantAttempts) || fake.gets[calendar.APIPath] != tt.wantGETs {
				t.Errorf("fn ran on %v, list GETs = %d; want %v, %d",
					attempts, fake.gets[calendar.APIPath], tt.wantAttempts, tt.wantGETs)
			}
		})
	}
}

// ErrDecryptDegraded on cached key material drops the keys and reruns fn once
// on the same calendar; if access fails on the rerun, the degraded outcome
// stands (withAccessResult then returns the degraded result best-effort).
func TestWithAccessHealsDegradedKeys(t *testing.T) {
	tests := []struct {
		name         string
		accessFails  bool // access fails on the rerun
		wantErr      error
		wantAttempts []string
	}{
		{name: "fn reruns on the same calendar", wantAttempts: []string{"id1", "id1"}},
		{name: "a failed rerun keeps the degraded outcome", accessFails: true,
			wantErr: event.ErrDecryptDegraded, wantAttempts: []string{"id1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &fakeRawAPI{bodies: map[string]string{
				calendar.APIPath: calListBody(t, [2]string{"id1", "Personal"}),
				auth.UsersPath:   `{"User":{"ID":"u1"}}`,
			}}
			s := healService(t, inner)
			ctx := context.Background()
			var out json.RawMessage
			_ = s.api.Get(ctx, auth.UsersPath, nil, &out) // miss
			_ = s.api.Get(ctx, auth.UsersPath, nil, &out) // hit: key material came from cache
			s.keychain = &calendar.Keychain{}             // sentinel: the heal must drop it

			calls := 0
			access := func(ctx context.Context, selector string) (calendar.Info, *calendar.Access, bool, error) {
				calls++
				if calls > 1 && tt.accessFails {
					return calendar.Info{}, nil, false, errors.New("network down")
				}
				return fakeAccess(s)(ctx, selector)
			}
			var attempts []string
			err := s.withAccess(ctx, access, "", func(info calendar.Info, _ *calendar.Access) error {
				attempts = append(attempts, info.ID)
				if len(attempts) == 1 {
					return event.ErrDecryptDegraded
				}
				return nil
			})
			if !errors.Is(err, tt.wantErr) || !slices.Equal(attempts, tt.wantAttempts) {
				t.Errorf("err = %v, fn ran on %v; want %v, %v", err, attempts, tt.wantErr, tt.wantAttempts)
			}
			if s.keychain != nil {
				t.Error("the heal must drop the keychain")
			}
			if n := inner.gets[calendar.APIPath]; n != 1 {
				t.Errorf("list GETs = %d, want 1 (a key heal keeps the list)", n)
			}
		})
	}
}
