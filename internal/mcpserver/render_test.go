package mcpserver

import (
	"strings"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/recurrence"
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
	got := renderOccurrence(l, time.UTC, calendar.Settings{}, calendar.Info{})
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
	got := renderOccurrence(l, time.UTC, calendar.Settings{}, calendar.Info{})
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

	shown := calsvc.FormatOccurrenceStart(start.Unix(), false, loc)
	if shown != "2026-06-15 09:00" {
		t.Errorf("timed shown = %q, want the wall time in loc", shown)
	}

	// All-day: anchored at UTC midnight, shown as a plain date.
	allDay := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	shown = calsvc.FormatOccurrenceStart(allDay.Unix(), true, loc)
	if shown != "2026-06-15" {
		t.Errorf("all-day shown = %q", shown)
	}
}

func TestRenderOccurrenceEdited(t *testing.T) {
	start := time.Date(2026, 6, 16, 14, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	edited := listedAt(
		&caltypes.RawEvent{ID: "ev-3", RecurrenceID: start.Unix()},
		&event.Event{EventID: "ev-3", Summary: "Moved mtg", RecurrenceID: start},
		start, end,
	)
	got := renderOccurrence(edited, time.UTC, calendar.Settings{}, calendar.Info{})
	if !strings.Contains(got, "Moved mtg  (edited occurrence)") {
		t.Errorf("missing (edited occurrence) marker:\n%s", got)
	}
	if strings.Contains(got, "occurrence start:") {
		t.Errorf("edited occurrence must not print an occurrence start line:\n%s", got)
	}

	noTitle := listedAt(&caltypes.RawEvent{ID: "ev-4"}, &event.Event{EventID: "ev-4"}, start, end)
	if got := renderOccurrence(noTitle, time.UTC, calendar.Settings{}, calendar.Info{}); !strings.Contains(got, "(no title)") {
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
	got := renderOccurrence(l, loc, calendar.Settings{}, calendar.Info{})
	if !strings.HasPrefix(got, "- Mon 15 Jun (all day)  Midsummer") {
		t.Errorf("all-day line wrong:\n%s", got)
	}
}

func TestRenderEvents(t *testing.T) {
	if got := renderEvents(nil, 7, time.UTC, calendar.Settings{}, calendar.Info{}); got != "No events in the next 7 days." {
		t.Errorf("empty: got %q", got)
	}

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	l := listedAt(&caltypes.RawEvent{ID: "e"}, &event.Event{EventID: "e", Summary: "X"}, start, start.Add(time.Hour))
	got := renderEvents([]event.Listed{l}, 3, time.UTC, calendar.Settings{}, calendar.Info{})
	if !strings.HasPrefix(got, "Events in the next 3 days:\n") {
		t.Errorf("missing header:\n%s", got)
	}
	if !strings.Contains(got, "- Mon 15 Jun 09:00 - 10:00  X") {
		t.Errorf("missing event line:\n%s", got)
	}
}

func TestRenderCalendarDetail(t *testing.T) {
	c := calendar.Info{ID: "id-1", Name: "Personal", Type: 0, Color: "#415DF0"}
	set := calendar.Settings{
		DefaultEventDuration:        30,
		DefaultPartDayNotifications: []caltypes.Notification{{Type: 1, Trigger: "-PT15M"}},
		DefaultFullDayNotifications: []caltypes.Notification{{Type: 1, Trigger: "-PT16H"}},
	}
	got := renderCalendarDetail(c, set, true)
	for _, want := range []string{
		"Personal (normal)",
		"  ID: id-1",
		"  color: #415DF0",
		"  default reminder timed (notify): -PT15M",
		"  default reminder all-day (notify): -PT16H",
		"  default duration: 30 min",
		"  (default calendar)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("calendar detail missing %q:\n%s", want, got)
		}
	}
	// No default marker in the header line (the explicit note carries it).
	if strings.Contains(got, "[default]") {
		t.Errorf("header should not carry [default]:\n%s", got)
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

func TestRenderCreated(t *testing.T) {
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	c := &calsvc.CreatedEvent{
		ID: "ev1", Summary: "Lunch", Location: "Cafe", Description: "with team",
		Start: start, End: start.Add(time.Hour), RRule: "FREQ=DAILY",
	}
	got := renderCreated(c)
	for _, want := range []string{
		"Event created: Lunch",
		"Mon 15 Jun 09:00 - 10:00",
		"location: Cafe",
		"description: with team",
		"Repeats: FREQ=DAILY",
		"ID: ev1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderCreated missing %q:\n%s", want, got)
		}
	}
	// ID comes last (after Repeats).
	if strings.Index(got, "ID: ev1") < strings.Index(got, "Repeats:") {
		t.Errorf("ID should be the last line:\n%s", got)
	}

	// All-day omits the time range.
	allDay := &calsvc.CreatedEvent{Summary: "Holiday", Start: start, End: start.Add(24 * time.Hour), AllDay: true}
	if got := renderCreated(allDay); !strings.Contains(got, "Mon 15 Jun") || strings.Contains(got, "09:00") {
		t.Errorf("all-day render = %q", got)
	}
}

func TestRenderEventDetail(t *testing.T) {
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	ev := &event.Event{
		EventID:     "ev1",
		Summary:     "Standup",
		Location:    "Zoom",
		Description: "daily sync",
		Start:       start,
		End:         start.Add(30 * time.Minute),
		Organizer:   &event.Person{Email: "boss@co", CN: "Boss"},
		Attendees:   []event.Attendee{{Email: "me@co", CN: "Me", Status: 3}},
	}
	set := calendar.Settings{DefaultPartDayNotifications: []caltypes.Notification{{Type: 1, Trigger: "-PT10M"}}}
	cal := calendar.Info{Color: "#415DF0"}
	got := renderEventDetail(ev, time.UTC, set, cal)
	for _, want := range []string{
		"Standup",
		"Mon 15 Jun 09:00 - 09:30",
		"location: Zoom",
		"description: daily sync",
		"organizer: Boss <boss@co>",
		"attendee: Me <me@co> (accepted)",
		"reminder (notify): -PT10M", // inherited calendar default
		"color: #415DF0",
		"ID: ev1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderEventDetail missing %q:\n%s", want, got)
		}
	}
}
