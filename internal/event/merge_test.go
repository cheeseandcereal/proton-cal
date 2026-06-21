package event

import (
	"testing"
	"time"
)

// Table-driven tests for the pure merge/seed logic (no HTTP/PGP fixtures
// needed).

func baseCurrent() *Event {
	return &Event{
		EventID:       "ev1",
		UID:           "uid1",
		CalendarID:    "cal1",
		Summary:       "Old",
		Description:   "Desc",
		Location:      "Loc",
		Status:        "TENTATIVE",
		Transp:        "TRANSPARENT",
		Comment:       "note",
		Created:       time.Date(2025, 1, 1, 8, 0, 0, 0, time.UTC),
		Start:         time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC),
		End:           time.Date(2026, 6, 15, 7, 30, 0, 0, time.UTC),
		StartTimezone: "Europe/Berlin",
		RRule:         "FREQ=DAILY;COUNT=5",
		Exdates:       []time.Time{time.Date(2026, 6, 16, 7, 0, 0, 0, time.UTC)},
		Sequence:      2,
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
