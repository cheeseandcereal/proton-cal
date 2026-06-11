package cli

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/recurrence"
)

// ts returns the unix timestamp of a UTC wall time.
func ts(year int, month time.Month, day, hour, minute int) int64 {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC).Unix()
}

func listedTimed() event.Listed {
	raw := &caltypes.RawEvent{ID: "evt1"}
	return event.Listed{
		Occurrence: recurrence.Occurrence{
			Event: raw,
			Start: ts(2026, 6, 12, 9, 0),
			End:   ts(2026, 6, 12, 9, 30),
		},
		Event: &event.Event{
			EventID:     "evt1",
			UID:         "uid1",
			CalendarID:  "cal1",
			Summary:     "Standup",
			Description: "Weekly sync",
			Location:    "Zoom",
			StartTime:   ts(2026, 6, 12, 9, 0),
			EndTime:     ts(2026, 6, 12, 9, 30),
		},
	}
}

func TestOccurrenceLinesTimed(t *testing.T) {
	got := occurrenceLines(listedTimed(), time.UTC)
	want := []string{
		"  2026-06-12 09:00 - 09:30  Standup  [Zoom]",
		"      Weekly sync",
		"    ID: evt1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("occurrenceLines() = %q, want %q", got, want)
	}
}

func TestOccurrenceLinesTimedInZone(t *testing.T) {
	loc := time.FixedZone("UTC+2", 2*60*60)
	got := occurrenceLines(listedTimed(), loc)
	if got[0] != "  2026-06-12 11:00 - 11:30  Standup  [Zoom]" {
		t.Errorf("head line in UTC+2 = %q", got[0])
	}
}

func TestOccurrenceLinesNoTitleNoExtras(t *testing.T) {
	l := listedTimed()
	l.Event.Summary = ""
	l.Event.Description = ""
	l.Event.Location = ""
	got := occurrenceLines(l, time.UTC)
	want := []string{
		"  2026-06-12 09:00 - 09:30  (no title)",
		"    ID: evt1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("occurrenceLines() = %q, want %q", got, want)
	}
}

func TestOccurrenceLinesAllDay(t *testing.T) {
	l := listedTimed()
	l.Event.AllDay = true
	// Render in a negative-offset zone: the all-day date must stay the
	// UTC-anchored date, not shift to the previous local day.
	loc := time.FixedZone("UTC-7", -7*60*60)
	got := occurrenceLines(l, loc)
	if got[0] != "  2026-06-12 (all day)  Standup  [Zoom]" {
		t.Errorf("all-day head line = %q", got[0])
	}
}

func TestOccurrenceLinesRecurringMaster(t *testing.T) {
	l := listedTimed()
	l.Occurrence.Event.RRule = "FREQ=DAILY"
	l.Event.RRule = "FREQ=DAILY"
	got := occurrenceLines(l, time.UTC)
	want := []string{
		"  2026-06-12 09:00 - 09:30  Standup  (recurring)  [Zoom]",
		"      Weekly sync",
		"    ID: evt1",
		"    occurrence start: 2026-06-12 09:00",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("occurrenceLines() = %q, want %q", got, want)
	}
}

func TestOccurrenceLinesRecurringAllDayMaster(t *testing.T) {
	l := listedTimed()
	l.Occurrence.Event.RRule = "FREQ=WEEKLY"
	l.Occurrence.Start = ts(2026, 6, 12, 0, 0)
	l.Event.AllDay = true
	loc := time.FixedZone("UTC-7", -7*60*60)
	got := occurrenceLines(l, loc)
	last := got[len(got)-1]
	if last != "    occurrence start: 2026-06-12" {
		t.Errorf("all-day occurrence start line = %q", last)
	}
}

func TestOccurrenceLinesEditedOccurrence(t *testing.T) {
	l := listedTimed()
	l.Occurrence.Event.RecurrenceID = ts(2026, 6, 12, 8, 0)
	l.Event.RecurrenceID = ts(2026, 6, 12, 8, 0)
	got := occurrenceLines(l, time.UTC)
	if got[0] != "  2026-06-12 09:00 - 09:30  Standup  (edited occurrence)  [Zoom]" {
		t.Errorf("edited occurrence head line = %q", got[0])
	}
	for _, line := range got {
		if line == "    occurrence start: 2026-06-12 09:00" {
			t.Errorf("edited occurrence must not get an occurrence start line: %q", got)
		}
	}
}

func TestOccurrenceLinesDecryptError(t *testing.T) {
	l := event.Listed{
		Occurrence: recurrence.Occurrence{
			Event: &caltypes.RawEvent{ID: "evt9"},
			Start: ts(2026, 6, 12, 9, 0),
			End:   ts(2026, 6, 12, 9, 30),
		},
		Err: errors.New("bad key"),
	}
	got := occurrenceLines(l, time.UTC)
	want := []string{
		"  (decrypt error: bad key)",
		"    ID: evt9",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("occurrenceLines() = %q, want %q", got, want)
	}
}

func TestOccurrenceJSONTimed(t *testing.T) {
	l := listedTimed()
	l.Occurrence.Event.RRule = "FREQ=DAILY"
	l.Event.RRule = "FREQ=DAILY"
	loc := time.FixedZone("UTC+2", 2*60*60)
	got := occurrenceJSON(l, loc)
	want := eventJSON{
		ID:                "evt1",
		UID:               "uid1",
		Summary:           "Standup",
		Description:       "Weekly sync",
		Location:          "Zoom",
		Start:             "2026-06-12T11:00:00+02:00",
		End:               "2026-06-12T11:30:00+02:00",
		AllDay:            false,
		Recurring:         true,
		EditedOccurrence:  false,
		OccurrenceStartTS: ts(2026, 6, 12, 9, 0),
		RRule:             "FREQ=DAILY",
		CalendarID:        "cal1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("occurrenceJSON() = %+v, want %+v", got, want)
	}
}

func TestOccurrenceJSONAllDayUsesUTC(t *testing.T) {
	l := listedTimed()
	l.Event.AllDay = true
	l.Occurrence.Start = ts(2026, 6, 12, 0, 0)
	l.Occurrence.End = ts(2026, 6, 13, 0, 0)
	loc := time.FixedZone("UTC-7", -7*60*60)
	got := occurrenceJSON(l, loc)
	if got.Start != "2026-06-12T00:00:00Z" || got.End != "2026-06-13T00:00:00Z" {
		t.Errorf("all-day start/end = %q / %q, want UTC-anchored dates", got.Start, got.End)
	}
	if !got.AllDay {
		t.Error("AllDay = false, want true")
	}
}

func TestOccurrenceJSONEditedOccurrence(t *testing.T) {
	l := listedTimed()
	l.Occurrence.Event.RecurrenceID = ts(2026, 6, 12, 8, 0)
	l.Event.RecurrenceID = ts(2026, 6, 12, 8, 0)
	got := occurrenceJSON(l, time.UTC)
	if !got.EditedOccurrence || got.Recurring {
		t.Errorf("EditedOccurrence=%v Recurring=%v, want true/false", got.EditedOccurrence, got.Recurring)
	}
}

func TestOccurrenceJSONDecryptError(t *testing.T) {
	l := event.Listed{
		Occurrence: recurrence.Occurrence{
			Event: &caltypes.RawEvent{ID: "evt9"},
			Start: ts(2026, 6, 12, 9, 0),
		},
		Err: errors.New("bad key"),
	}
	got := occurrenceJSON(l, time.UTC)
	want := eventJSON{
		ID:                "evt9",
		OccurrenceStartTS: ts(2026, 6, 12, 9, 0),
		Error:             "bad key",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("occurrenceJSON() = %+v, want %+v", got, want)
	}
}

func TestFormatOccurrenceStart(t *testing.T) {
	loc := time.FixedZone("UTC+2", 2*60*60)
	if got := formatOccurrenceStart(ts(2026, 6, 12, 9, 0), false, loc); got != "2026-06-12 11:00" {
		t.Errorf("timed = %q", got)
	}
	if got := formatOccurrenceStart(ts(2026, 6, 12, 0, 0), true, loc); got != "2026-06-12" {
		t.Errorf("all-day = %q", got)
	}
}
