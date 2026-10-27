package calsvc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/config"
	"github.com/cheeseandcereal/proton-cal/pkg/internal/papitest"
)

// calListBody is a GET /calendar/v1 response with the given calendars, each
// expressed as id/name pairs (Type 0) on a single member entry.
func calListBody(_ *testing.T, cals ...[2]string) string {
	specs := make([]papitest.CalSpec, len(cals))
	for i, c := range cals {
		specs[i] = papitest.CalSpec{ID: c[0], Name: c[1]}
	}
	return papitest.CalListBody(specs...)
}

// detachedWithAPI builds a Service backed by the given fake (no cache decorator)
// so resolution logic can be exercised without a session.
func detachedWithAPI(cfg config.Config, fake *fakeRawAPI) *Service {
	return &Service{cfg: cfg, api: fake, freshAPI: fake}
}

func TestServiceCalendars(t *testing.T) {
	fake := &fakeRawAPI{bodies: map[string]string{
		calendar.APIPath: calListBody(t, [2]string{"id1", "Personal"}, [2]string{"id2", "Work"}),
	}}
	s := detachedWithAPI(config.Config{Timezone: "UTC"}, fake)

	cals, err := s.Calendars(context.Background())
	if err != nil {
		t.Fatalf("Calendars: %v", err)
	}
	if len(cals) != 2 || cals[0].Name != "Personal" || cals[1].ID != "id2" {
		t.Fatalf("unexpected calendars: %+v", cals)
	}
	// The fetch also primes the in-memory cache.
	if s.cals == nil {
		t.Error("fetchCalendars did not populate the in-memory cache")
	}
}

// userSettingsBody is a GET /settings/calendar response naming the given
// default calendar ID.
func userSettingsBody(defaultID string) string {
	return `{"CalendarUserSettings":{"DefaultCalendarID":"` + defaultID + `"}}`
}

func TestServiceDefaultCalendarID(t *testing.T) {
	fake := &fakeRawAPI{bodies: map[string]string{
		calendar.UserSettingsPath: userSettingsBody("id2"),
	}}
	s := detachedWithAPI(config.Config{}, fake)
	got, err := s.DefaultCalendarID(context.Background())
	if err != nil {
		t.Fatalf("DefaultCalendarID: %v", err)
	}
	if got != "id2" {
		t.Errorf("DefaultCalendarID = %q, want id2", got)
	}
}

func TestResolveCalendarByNameAndDefault(t *testing.T) {
	fake := &fakeRawAPI{bodies: map[string]string{
		calendar.APIPath:          calListBody(t, [2]string{"id1", "Personal"}, [2]string{"id2", "Work"}),
		calendar.UserSettingsPath: userSettingsBody("id2"),
	}}
	s := detachedWithAPI(config.Config{}, fake)
	ctx := context.Background()

	// Explicit name (case-insensitive).
	info, err := s.resolveCalendar(ctx, "personal")
	if err != nil {
		t.Fatalf("resolve by name: %v", err)
	}
	if info.ID != "id1" {
		t.Errorf("got %s, want id1", info.ID)
	}

	// Empty selector -> server default (id2 / Work).
	info, err = s.resolveCalendar(ctx, "")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if info.ID != "id2" {
		t.Errorf("default resolved to %s, want id2 (Work)", info.ID)
	}

	// Exact ID.
	info, err = s.resolveCalendar(ctx, "id1")
	if err != nil {
		t.Fatalf("resolve by id: %v", err)
	}
	if info.Name != "Personal" {
		t.Errorf("got %q", info.Name)
	}
}

// With no server default set, an empty selector falls back to the first
// calendar in the list.
func TestResolveCalendarEmptyDefaultFallsBackToFirst(t *testing.T) {
	fake := &fakeRawAPI{bodies: map[string]string{
		calendar.APIPath:          calListBody(t, [2]string{"id1", "Personal"}, [2]string{"id2", "Work"}),
		calendar.UserSettingsPath: userSettingsBody(""),
	}}
	s := detachedWithAPI(config.Config{}, fake)
	info, err := s.resolveCalendar(context.Background(), "")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if info.ID != "id1" {
		t.Errorf("default resolved to %s, want id1 (first)", info.ID)
	}
}

func TestResolveCalendarNoMatch(t *testing.T) {
	fake := &fakeRawAPI{bodies: map[string]string{
		calendar.APIPath: calListBody(t, [2]string{"id1", "Personal"}),
	}}
	s := detachedWithAPI(config.Config{}, fake)
	if _, err := s.resolveCalendar(context.Background(), "Nonexistent"); err == nil {
		t.Fatal("want no-match error")
	}
}

func TestResolveCalendars(t *testing.T) {
	fake := &fakeRawAPI{bodies: map[string]string{
		calendar.APIPath: calListBody(t,
			[2]string{"id1", "Personal"}, [2]string{"id2", "Work"}, [2]string{"id3", "Holidays"}),
		calendar.UserSettingsPath: userSettingsBody("id2"),
	}}
	s := detachedWithAPI(config.Config{}, fake)
	ctx := context.Background()

	ids := func(infos []calendar.Info) []string {
		out := make([]string, len(infos))
		for i, c := range infos {
			out[i] = c.ID
		}
		return out
	}

	// Empty selectors + no "all": single default calendar.
	got, err := s.resolveCalendars(ctx, nil, false)
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if len(got) != 1 || got[0].ID != "id2" {
		t.Errorf("empty selectors = %v, want [id2]", ids(got))
	}

	// Explicit multi (by name + id), order preserved.
	got, err = s.resolveCalendars(ctx, []string{"Personal", "id3"}, false)
	if err != nil {
		t.Fatalf("multi: %v", err)
	}
	if g := ids(got); len(g) != 2 || g[0] != "id1" || g[1] != "id3" {
		t.Errorf("multi = %v, want [id1 id3]", g)
	}

	// All calendars.
	got, err = s.resolveCalendars(ctx, nil, true)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("all = %v, want 3 calendars", ids(got))
	}

	// Unknown selector fails the whole call.
	if _, err := s.resolveCalendars(ctx, []string{"Personal", "nope"}, false); err == nil {
		t.Error("unknown selector: want error")
	}
}

// When the in-memory list is stale and not marked fresh, resolveCalendar does
// one fresh fetch and then succeeds against the updated list.
func TestResolveCalendarStaleRefetch(t *testing.T) {
	fake := &fakeRawAPI{bodies: map[string]string{
		calendar.APIPath: calListBody(t, [2]string{"id1", "Personal"}, [2]string{"id2", "Work"}),
	}}
	s := detachedWithAPI(config.Config{}, fake)
	// Seed a stale in-memory list lacking "Work".
	s.cals = []calendar.Info{{ID: "id1", Name: "Personal"}}

	info, err := s.resolveCalendar(context.Background(), "Work")
	if err != nil {
		t.Fatalf("resolve after refetch: %v", err)
	}
	if info.ID != "id2" {
		t.Errorf("got %s, want id2 after fresh refetch", info.ID)
	}
	if fake.gets[calendar.APIPath] == 0 {
		t.Error("expected a fresh calendar list fetch")
	}
}

func TestCalendarsRefreshesDefault(t *testing.T) {
	fake := &fakeRawAPI{bodies: map[string]string{
		calendar.APIPath:          calListBody(t, [2]string{"first", "First"}, [2]string{"chosen", "Chosen"}),
		calendar.UserSettingsPath: userSettingsBody("chosen"),
	}}
	s := healService(t, fake)
	s.cacheAPI.cache.Put(calendar.UserSettingsPath, json.RawMessage(userSettingsBody("first")))
	ctx := context.Background()
	if _, err := s.Calendars(ctx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if id, err := s.DefaultCalendarID(ctx); err != nil || id != "chosen" {
			t.Fatalf("default = %q, %v; want chosen", id, err)
		}
	}
	if fake.gets[calendar.UserSettingsPath] != 1 {
		t.Fatalf("default GETs = %d, want 1", fake.gets[calendar.UserSettingsPath])
	}
}

func TestResolveUnlistedDefault(t *testing.T) {
	for _, source := range []string{"memory", "disk", "network", "no-cache"} {
		t.Run(source, func(t *testing.T) {
			fake := &fakeRawAPI{bodies: map[string]string{
				calendar.APIPath:          calListBody(t, [2]string{"first", "First"}, [2]string{"chosen", "Chosen"}),
				calendar.UserSettingsPath: userSettingsBody("chosen"),
			}}
			s := healService(t, fake)
			if source == "no-cache" {
				s.api, s.freshAPI, s.cacheAPI = fake, fake, nil
				// A network-read default can be absent from an older memory list.
				s.cals = []calendar.Info{{ID: "first", Name: "First"}, {ID: "gone", Name: "Deleted"}}
			} else {
				s.cacheAPI.cache.Put(calendar.UserSettingsPath, json.RawMessage(userSettingsBody("gone")))
				switch source {
				case "memory":
					s.cals = []calendar.Info{{ID: "first", Name: "First"}, {ID: "chosen", Name: "Chosen"}}
				case "disk":
					s.cacheAPI.cache.Put(calendar.APIPath, json.RawMessage(fake.bodies[calendar.APIPath]))
				}
			}
			info, err := s.resolveCalendar(context.Background(), "")
			if err != nil || info.ID != "chosen" {
				t.Fatalf("resolved = %q, %v; want chosen", info.ID, err)
			}
			if fake.gets[calendar.APIPath] != 1 {
				t.Errorf("list GETs = %d, want 1", fake.gets[calendar.APIPath])
			}
			wantDefaultGets := 1
			if source == "no-cache" {
				wantDefaultGets = 2
			}
			if fake.gets[calendar.UserSettingsPath] != wantDefaultGets {
				t.Errorf("default GETs = %d, want %d", fake.gets[calendar.UserSettingsPath], wantDefaultGets)
			}
		})
	}
}

func TestDefaultRefreshBounds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cached    string
		uncached  bool
		server    string
		readErr   error
		wantID    string
		wantReads int
	}{
		{name: "listed", cached: "chosen", server: "first", wantID: "chosen"},
		{name: "unset", wantID: "first"},
		{name: "still unlisted", cached: "gone", server: "gone", wantID: "first", wantReads: 1},
		{name: "failed refresh", cached: "gone", readErr: errors.New("offline"), wantID: "first", wantReads: 1},
		{name: "network default", uncached: true, server: "gone", wantID: "first", wantReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeRawAPI{
				bodies: map[string]string{
					calendar.APIPath:          calListBody(t, [2]string{"first", "First"}, [2]string{"chosen", "Chosen"}),
					calendar.UserSettingsPath: userSettingsBody(tc.server),
				},
				errs: map[string]error{calendar.UserSettingsPath: tc.readErr},
			}
			s := healService(t, fake)
			if !tc.uncached {
				s.cacheAPI.cache.Put(calendar.UserSettingsPath, json.RawMessage(userSettingsBody(tc.cached)))
			}
			info, err := s.resolveCalendar(context.Background(), "")
			if err != nil || info.ID != tc.wantID {
				t.Fatalf("resolved = %q, %v; want %s", info.ID, err, tc.wantID)
			}
			if fake.gets[calendar.UserSettingsPath] != tc.wantReads || fake.gets[calendar.APIPath] != 1 {
				t.Errorf("GETs = %v, want default %d and list 1", fake.gets, tc.wantReads)
			}
		})
	}
}
