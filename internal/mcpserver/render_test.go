package mcpserver

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal-go/internal/calendar"
	"github.com/cheeseandcereal/proton-cal-go/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal-go/internal/event"
	"github.com/cheeseandcereal/proton-cal-go/internal/front"
	"github.com/cheeseandcereal/proton-cal-go/internal/recurrence"
)

func TestRenderCalendars(t *testing.T) {
	cals := []calendar.Info{
		{ID: "id-1", Name: "Personal", Type: 0},
		{ID: "id-2", Name: "Holidays in Sweden", Type: 2},
		{ID: "id-3", Name: "Feeds", Type: 1},
	}
	got := renderCalendars(cals, "personal")
	for _, want := range []string{
		"Personal (normal)  [default]",
		"  ID: id-1",
		"Holidays in Sweden (holidays)",
		"Feeds (subscribed)",
		"  ID: id-3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "[default]") != 1 {
		t.Errorf("want exactly one default marker:\n%s", got)
	}

	if got := renderCalendars(nil, ""); got != "No calendars found." {
		t.Errorf("empty list: got %q", got)
	}
}

// listedAt builds a Listed occurrence for tests. The raw row carries the
// recurrence metadata (master/exception detection).
func listedAt(raw *caltypes.RawEvent, ev *event.Event, start, end time.Time) event.Listed {
	return event.Listed{
		Occurrence: recurrence.Occurrence{Event: raw, Start: start.Unix(), End: end.Unix()},
		Event:      ev,
	}
}

func TestRenderOccurrenceTimed(t *testing.T) {
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC) // a Monday
	end := start.Add(30 * time.Minute)
	l := listedAt(
		&caltypes.RawEvent{ID: "ev-1"},
		&event.Event{EventID: "ev-1", Summary: "Standup", Location: "Room 1", Description: "Daily sync"},
		start, end,
	)
	got := renderOccurrence(l, time.UTC)
	want := "- Mon 15 Jun 09:00 - 09:30  Standup  [Room 1]\n  Daily sync\n  ID: ev-1"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderOccurrenceRecurringMaster(t *testing.T) {
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	l := listedAt(
		&caltypes.RawEvent{ID: "ev-2", RRule: "FREQ=WEEKLY"},
		&event.Event{EventID: "ev-2", Summary: "Weekly", RRule: "FREQ=WEEKLY"},
		start, end,
	)
	got := renderOccurrence(l, time.UTC)
	if !strings.Contains(got, "Weekly  (recurring)") {
		t.Errorf("missing (recurring) marker:\n%s", got)
	}
	if !strings.Contains(got, "\n  occurrence start: 2026-06-15 09:00") {
		t.Errorf("missing occurrence start line:\n%s", got)
	}
}

func TestRenderOccurrenceStartRoundTripsThroughParseOccurrence(t *testing.T) {
	// The shown "occurrence start" value must parse back to the exact
	// original timestamp (it feeds update_event/delete_event).
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, loc)

	shown := formatOccurrenceStart(start.Unix(), false, loc)
	ts, err := front.ParseOccurrence(shown, "Europe/Stockholm")
	if err != nil {
		t.Fatal(err)
	}
	if ts != start.Unix() {
		t.Errorf("round trip: got %d, want %d (shown %q)", ts, start.Unix(), shown)
	}

	// All-day: anchored at UTC midnight, shown as a plain date.
	allDay := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	shown = formatOccurrenceStart(allDay.Unix(), true, loc)
	if shown != "2026-06-15" {
		t.Errorf("all-day shown = %q", shown)
	}
}

func TestRenderOccurrenceEditedAndErrors(t *testing.T) {
	start := time.Date(2026, 6, 16, 14, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	edited := listedAt(
		&caltypes.RawEvent{ID: "ev-3", RecurrenceID: start.Unix()},
		&event.Event{EventID: "ev-3", Summary: "Moved mtg", RecurrenceID: start.Unix()},
		start, end,
	)
	got := renderOccurrence(edited, time.UTC)
	if !strings.Contains(got, "Moved mtg  (edited occurrence)") {
		t.Errorf("missing (edited occurrence) marker:\n%s", got)
	}
	if strings.Contains(got, "occurrence start:") {
		t.Errorf("edited occurrence must not print an occurrence start line:\n%s", got)
	}

	failed := event.Listed{
		Occurrence: recurrence.Occurrence{Event: &caltypes.RawEvent{ID: "ev-bad"}, Start: start.Unix(), End: end.Unix()},
		Err:        errors.New("no key packet"),
	}
	got = renderOccurrence(failed, time.UTC)
	if got != "- (decrypt error for ev-bad: no key packet)" {
		t.Errorf("decrypt error line = %q", got)
	}

	noTitle := listedAt(&caltypes.RawEvent{ID: "ev-4"}, &event.Event{EventID: "ev-4"}, start, end)
	if got := renderOccurrence(noTitle, time.UTC); !strings.Contains(got, "(no title)") {
		t.Errorf("missing (no title):\n%s", got)
	}
}

func TestRenderOccurrenceAllDay(t *testing.T) {
	start := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	l := listedAt(
		&caltypes.RawEvent{ID: "ev-5", FullDay: 1},
		&event.Event{EventID: "ev-5", Summary: "Midsummer", AllDay: true},
		start, end,
	)
	// Render in a zone west of UTC: the date must not shift.
	loc := time.FixedZone("UTC-8", -8*3600)
	got := renderOccurrence(l, loc)
	if !strings.HasPrefix(got, "- Mon 15 Jun (all day)  Midsummer") {
		t.Errorf("all-day line wrong:\n%s", got)
	}
}

func TestRenderEvents(t *testing.T) {
	if got := renderEvents(nil, 7, time.UTC); got != "No events in the next 7 days." {
		t.Errorf("empty: got %q", got)
	}

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	l := listedAt(&caltypes.RawEvent{ID: "e"}, &event.Event{EventID: "e", Summary: "X"}, start, start.Add(time.Hour))
	got := renderEvents([]event.Listed{l}, 3, time.UTC)
	if !strings.HasPrefix(got, "Events in the next 3 days:\n") {
		t.Errorf("missing header:\n%s", got)
	}
	if !strings.Contains(got, "- Mon 15 Jun 09:00 - 10:00  X") {
		t.Errorf("missing event line:\n%s", got)
	}
}

func TestRenderDeleteResult(t *testing.T) {
	tests := []struct {
		res  event.DeleteResult
		want string
	}{
		{event.DeleteResult{Kind: "occurrence", RowsDeleted: 1}, "Occurrence deleted."},
		{event.DeleteResult{Kind: "series", RowsDeleted: 3}, "Recurring series deleted (3 row(s))."},
		{event.DeleteResult{Kind: "event", RowsDeleted: 1}, "Event ev-9 deleted."},
	}
	for _, tt := range tests {
		if got := renderDeleteResult(&tt.res, "ev-9"); got != tt.want {
			t.Errorf("kind %s: got %q, want %q", tt.res.Kind, got, tt.want)
		}
	}
}
