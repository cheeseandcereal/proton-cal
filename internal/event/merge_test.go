package event

import (
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
)

// Table-driven tests for the pure merge/seed logic (no HTTP/PGP fixtures
// needed).

func baseCurrent() *Event {
	return &Event{
		EventID:          "ev1",
		UID:              "uid1",
		CalendarID:       "cal1",
		Summary:          "Old",
		Description:      "Desc",
		Location:         "Loc",
		Status:           "TENTATIVE",
		Transp:           "TRANSPARENT",
		Comment:          "note",
		Created:          time.Date(2025, 1, 1, 8, 0, 0, 0, time.UTC),
		Start:            time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC),
		End:              time.Date(2026, 6, 15, 7, 30, 0, 0, time.UTC),
		StartTimezone:    "Europe/Berlin",
		RRule:            "FREQ=DAILY;COUNT=5",
		Exdates:          []time.Time{time.Date(2026, 6, 16, 7, 0, 0, 0, time.UTC)},
		Sequence:         2,
		Color:            "#EC3E7C",
		Notifications:    []caltypes.Notification{{Type: 1, Trigger: "-PT15M"}},
		NotificationsSet: true,
	}
}

func TestSeedExceptionRow(t *testing.T) {
	master := baseCurrent()
	occTS := time.Date(2026, 6, 17, 7, 0, 0, 0, time.UTC).Unix()

	t.Run("defaults inherit master", func(t *testing.T) {
		got := seedExceptionRow(master, UpdateOptions{}, occTS)
		if got.Summary != "Old" || got.Description != "Desc" || got.Location != "Loc" {
			t.Errorf("text fields: %+v", got)
		}
		if got.Start.Unix() != occTS {
			t.Errorf("start = %d, want the occurrence start %d", got.Start.Unix(), occTS)
		}
		if got.End.Sub(got.Start) != 30*time.Minute {
			t.Errorf("duration = %v, want the master's 30m", got.End.Sub(got.Start))
		}
		if got.TZName != "Europe/Berlin" || got.UID != "uid1" || got.Sequence != 2 {
			t.Errorf("tz/uid/seq: %q %q %d", got.TZName, got.UID, got.Sequence)
		}
		if got.RecurrenceID == nil || got.RecurrenceID.Unix() != occTS {
			t.Errorf("RecurrenceID = %v, want %d", got.RecurrenceID, occTS)
		}
		if got.RRule != "" {
			t.Errorf("exception rows must not carry an RRULE, got %q", got.RRule)
		}
		// Reminders/color are seeded from the master so the fresh exception
		// keeps the series' effective reminders/color.
		if !got.RemindersSet || len(got.Reminders) != 1 || got.Reminders[0].Trigger != "-PT15M" {
			t.Errorf("reminders not inherited from master: set=%v %+v", got.RemindersSet, got.Reminders)
		}
		if got.Color != "#EC3E7C" {
			t.Errorf("color not inherited from master: %q", got.Color)
		}
	})

	t.Run("reminder/color overrides apply", func(t *testing.T) {
		got := seedExceptionRow(master, UpdateOptions{
			Reminders: &RemindersUpdate{List: []caltypes.Notification{{Type: 0, Trigger: "-PT1H"}}},
			Color:     &ColorUpdate{Value: "#112233"},
		}, occTS)
		if !got.RemindersSet || len(got.Reminders) != 1 || got.Reminders[0].Type != 0 || got.Reminders[0].Trigger != "-PT1H" {
			t.Errorf("reminder override: %+v", got.Reminders)
		}
		if got.Color != "#112233" {
			t.Errorf("color override: %q", got.Color)
		}
	})

	t.Run("reminder inherit override clears", func(t *testing.T) {
		got := seedExceptionRow(master, UpdateOptions{
			Reminders: &RemindersUpdate{Inherit: true},
		}, occTS)
		if got.RemindersSet {
			t.Errorf("inherit override should leave RemindersSet false, got set with %+v", got.Reminders)
		}
	})

	t.Run("overrides apply", func(t *testing.T) {
		s, l := "Moved", "Elsewhere"
		newStart := time.Date(2026, 6, 17, 9, 0, 0, 0, time.UTC)
		newEnd := newStart.Add(2 * time.Hour)
		got := seedExceptionRow(master, UpdateOptions{
			Summary: &s, Location: &l, Start: &newStart, End: &newEnd, TZName: "UTC",
		}, occTS)
		if got.Summary != "Moved" || got.Location != "Elsewhere" {
			t.Errorf("overrides: %+v", got)
		}
		if !got.Start.Equal(newStart) || !got.End.Equal(newEnd) || got.TZName != "UTC" {
			t.Errorf("times: %v %v %q", got.Start, got.End, got.TZName)
		}
		if got.RecurrenceID == nil || got.RecurrenceID.Unix() != occTS {
			t.Error("RecurrenceID must stay the ORIGINAL occurrence start even when moved")
		}
	})
}
