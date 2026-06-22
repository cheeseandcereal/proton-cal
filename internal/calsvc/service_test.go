package calsvc

import (
	"context"
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/config"
)

// calListBody is a GET /calendar/v1 response with the given calendars, each
// expressed as id/name pairs on a single member entry.
func calListBody(t *testing.T, cals ...[2]string) string {
	t.Helper()
	var b []byte
	b = append(b, []byte(`{"Calendars":[`)...)
	for i, c := range cals {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, []byte(`{"ID":"`+c[0]+`","Type":0,"Members":[{"ID":"m-`+c[0]+`","Name":"`+c[1]+`","Color":"#112233"}]}`)...)
	}
	b = append(b, []byte(`]}`)...)
	return string(b)
}

// detachedWithAPI builds a Service whose calendar-list endpoints are served
// by the given fake (no cache decorator: api == freshAPI == fake), so the
// resolution logic can be exercised without a session.
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

func TestServiceDefaultCalendarSelector(t *testing.T) {
	s := NewDetached(config.Config{DefaultCalendar: "Work"})
	if got := s.DefaultCalendarSelector(); got != "Work" {
		t.Errorf("DefaultCalendarSelector = %q, want Work", got)
	}
	if got := NewDetached(config.Config{}).DefaultCalendarSelector(); got != "" {
		t.Errorf("empty default = %q, want empty", got)
	}
}

func TestResolveCalendarByNameAndDefault(t *testing.T) {
	fake := &fakeRawAPI{bodies: map[string]string{
		calendar.APIPath: calListBody(t, [2]string{"id1", "Personal"}, [2]string{"id2", "Work"}),
	}}
	s := detachedWithAPI(config.Config{DefaultCalendar: "Work"}, fake)
	ctx := context.Background()

	// Explicit name (case-insensitive).
	info, err := s.resolveCalendar(ctx, "personal")
	if err != nil {
		t.Fatalf("resolve by name: %v", err)
	}
	if info.ID != "id1" {
		t.Errorf("got %s, want id1", info.ID)
	}

	// Empty selector -> configured default ("Work").
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

func TestResolveCalendarNoMatch(t *testing.T) {
	fake := &fakeRawAPI{bodies: map[string]string{
		calendar.APIPath: calListBody(t, [2]string{"id1", "Personal"}),
	}}
	s := detachedWithAPI(config.Config{}, fake)
	if _, err := s.resolveCalendar(context.Background(), "Nonexistent"); err == nil {
		t.Fatal("want no-match error")
	}
}

// When the in-memory list is stale (missing a calendar) and no cache
// decorator marks it fresh, resolveCalendar performs one fresh fetch and
// then succeeds against the updated list.
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
