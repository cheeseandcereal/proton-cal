//go:build integration

package integration

import (
	"testing"

	"github.com/cheeseandcereal/proton-cal-go/internal/calendar"
)

// TestCalendars asserts calendar listing and that every configured selector
// resolves to a usable calendar. This is the only test that iterates over
// ALL configured calendars; the lifecycle tests use just the first one.
func TestCalendars(t *testing.T) {
	s := setup(t)

	if len(s.cals) < 1 {
		t.Fatalf("calendar.List returned %d calendars, want >= 1", len(s.cals))
	}

	for i, sel := range s.tc.Calendars {
		info, err := calendar.Resolve(s.cals, sel, "")
		if err != nil {
			t.Errorf("configured calendar %q does not resolve: %v", sel, err)
			continue
		}
		if info.ID == "" {
			t.Errorf("calendar %q resolved with an empty ID", sel)
		}
		if info.Name == "" {
			t.Errorf("calendar %q resolved with an empty Name", sel)
		}
		if info.MemberID == "" {
			t.Errorf("calendar %q resolved with an empty MemberID (no member entry?)", sel)
		}
		// Setup resolved the same selector; the two must agree.
		if got := s.resolved[i]; got.ID != info.ID {
			t.Errorf("calendar %q resolved inconsistently: setup %s, test %s", sel, got.ID, info.ID)
		}
	}
}
