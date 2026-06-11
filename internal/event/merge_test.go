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

func TestMergeUpdateFieldOnlyEdit(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	summary := "New"
	got := mergeUpdate(baseCurrent(), UpdateOptions{Summary: &summary}, now)

	if *got.Summary != "New" || *got.Description != "Desc" || *got.Location != "Loc" {
		t.Errorf("text merge: %q %q %q", *got.Summary, *got.Description, *got.Location)
	}
	if *got.Sequence != 2 {
		t.Errorf("field-only edit must keep SEQUENCE, got %d", *got.Sequence)
	}
	if got.TZName != "Europe/Berlin" {
		t.Errorf("must keep stored zone, got %q", got.TZName)
	}
	if got.RRule != "FREQ=DAILY;COUNT=5" || len(got.Exdates) != 1 {
		t.Errorf("recurrence must be preserved: %q %v", got.RRule, got.Exdates)
	}
	if *got.Status != "TENTATIVE" || *got.Transp != "TRANSPARENT" || *got.Comment != "note" {
		t.Errorf("web-client fields must be preserved: %v %v %v", *got.Status, *got.Transp, *got.Comment)
	}
	if got.Created == nil || !got.Created.Equal(time.Date(2025, 1, 1, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("CREATED must be preserved, got %v", got.Created)
	}
	if !got.DTStamp.Equal(now) {
		t.Errorf("DTStamp = %v, want %v", got.DTStamp, now)
	}
}

func TestMergeUpdateSignificantEdit(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	newStart := time.Date(2026, 6, 16, 8, 0, 0, 0, time.UTC)
	got := mergeUpdate(baseCurrent(), UpdateOptions{Start: &newStart}, now)
	if *got.Sequence != 3 {
		t.Errorf("time edit must bump SEQUENCE to 3, got %d", *got.Sequence)
	}
	if !got.Start.Equal(newStart) {
		t.Errorf("Start = %v, want %v", got.Start, newStart)
	}
	if !got.End.Equal(baseCurrent().End) {
		t.Errorf("End must keep current value, got %v", got.End)
	}
}

func TestMergeUpdateClearRRule(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	got := mergeUpdate(baseCurrent(), UpdateOptions{ClearRRule: true}, now)
	if got.RRule != "" || len(got.Exdates) != 0 {
		t.Errorf("ClearRRule must strip recurrence and exdates: %q %v", got.RRule, got.Exdates)
	}
	if *got.Sequence != 3 {
		t.Errorf("recurrence edit must bump SEQUENCE, got %d", *got.Sequence)
	}
}

func TestMergeUpdateAddExdatesAndRezone(t *testing.T) {
	now := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	ex := time.Date(2026, 6, 17, 7, 0, 0, 0, time.UTC)
	got := mergeUpdate(baseCurrent(), UpdateOptions{AddExdates: []time.Time{ex}, TZName: "America/New_York"}, now)
	if len(got.Exdates) != 2 || !got.Exdates[1].Equal(ex) {
		t.Errorf("exdates = %v", got.Exdates)
	}
	if got.TZName != "America/New_York" {
		t.Errorf("explicit re-zone ignored: %q", got.TZName)
	}
}

func TestMergeUpdateUTCFallback(t *testing.T) {
	cur := baseCurrent()
	cur.StartTimezone = ""
	got := mergeUpdate(cur, UpdateOptions{}, time.Now())
	if got.TZName != "UTC" {
		t.Errorf("empty stored zone must fall back to UTC, got %q", got.TZName)
	}
}

func TestMergeUpdateExceptionRowKeepsRecurrenceID(t *testing.T) {
	cur := baseCurrent()
	cur.RRule = ""
	cur.RecurrenceID = time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC)
	got := mergeUpdate(cur, UpdateOptions{}, time.Now())
	if got.RecurrenceID == nil || !got.RecurrenceID.Equal(cur.RecurrenceID) {
		t.Errorf("RECURRENCE-ID must be preserved, got %v", got.RecurrenceID)
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
