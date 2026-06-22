package cli

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/calsvc"
	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/event"
	"github.com/cheeseandcereal/proton-cal/pkg/recurrence"
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
			Start:       time.Unix(ts(2026, 6, 12, 9, 0), 0).UTC(),
			End:         time.Unix(ts(2026, 6, 12, 9, 30), 0).UTC(),
		},
	}
}

// hasLine reports whether any line matches want after trimming leading
// whitespace and collapsing the alignment padding (runs of spaces) to one.
func hasLine(lines []string, want string) bool {
	collapse := func(s string) string {
		return strings.Join(strings.Fields(s), " ")
	}
	for _, l := range lines {
		if collapse(l) == collapse(want) {
			return true
		}
	}
	return false
}

func TestOccurrenceLinesTimed(t *testing.T) {
	got := occurrenceListLines([]event.Listed{listedTimed()}, time.UTC, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	// Header: summary only, un-indented; no inline time/location.
	if got[0] != "Standup" {
		t.Errorf("header line = %q", got[0])
	}
	// Time on its own labeled sub-line; timezone once, at the end.
	if !hasLine(got, "Time: 2026-06-12 09:00 - 09:30 UTC") {
		t.Errorf("missing Time sub-line:\n%q", got)
	}
	// Location and description on their own labeled sub-lines.
	if !hasLine(got, "Location: Zoom") {
		t.Errorf("missing Location sub-line:\n%q", got)
	}
	if !hasLine(got, "Description: Weekly sync") {
		t.Errorf("missing Description sub-line:\n%q", got)
	}
	if !hasLine(got, "ID: evt1") {
		t.Errorf("missing ID sub-line:\n%q", got)
	}
}

func TestOccurrenceLinesTimedInZone(t *testing.T) {
	loc := time.FixedZone("UTC+2", 2*60*60)
	got := occurrenceListLines([]event.Listed{listedTimed()}, loc, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	if !hasLine(got, "Time: 2026-06-12 11:00 - 11:30 UTC+2") {
		t.Errorf("Time sub-line in UTC+2 missing:\n%q", got)
	}
}

func TestOccurrenceLinesTimedSpansDays(t *testing.T) {
	l := listedTimed()
	// End on the next day: the end carries its own date; tz once at the end.
	l.Occurrence.End = ts(2026, 6, 13, 1, 0)
	l.Event.End = time.Unix(ts(2026, 6, 13, 1, 0), 0).UTC()
	got := occurrenceListLines([]event.Listed{l}, time.UTC, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	if !hasLine(got, "Time: 2026-06-12 09:00 - 2026-06-13 01:00 UTC") {
		t.Errorf("cross-day Time sub-line missing:\n%q", got)
	}
}

func TestOccurrenceLinesNoTitleNoExtras(t *testing.T) {
	l := listedTimed()
	l.Event.Summary = ""
	l.Event.Description = ""
	l.Event.Location = ""
	got := occurrenceListLines([]event.Listed{l}, time.UTC, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	want := []string{
		"(no title)",
		"  Time: 2026-06-12 09:00 - 09:30 UTC",
		"  ID:   evt1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("occurrenceLines() = %q, want %q", got, want)
	}
}

func TestOccurrenceLinesAllDay(t *testing.T) {
	l := listedTimed()
	l.Event.AllDay = true
	// Single-day all-day (exclusive end is the next day): no end date shown.
	l.Occurrence.End = ts(2026, 6, 13, 0, 0)
	// Render in a negative-offset zone: the all-day date must stay the
	// UTC-anchored date, not shift to the previous local day.
	loc := time.FixedZone("UTC-7", -7*60*60)
	got := occurrenceListLines([]event.Listed{l}, loc, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	if got[0] != "Standup" {
		t.Errorf("all-day header line = %q", got[0])
	}
	if !hasLine(got, "Time: 2026-06-12 (all day)") {
		t.Errorf("single all-day Time sub-line missing:\n%q", got)
	}
}

func TestOccurrenceLinesAllDayMultiDay(t *testing.T) {
	l := listedTimed()
	l.Event.AllDay = true
	l.Occurrence.Start = ts(2026, 6, 12, 0, 0)
	l.Occurrence.End = ts(2026, 6, 15, 0, 0) // exclusive end -> last day 06-14
	loc := time.FixedZone("UTC-7", -7*60*60)
	got := occurrenceListLines([]event.Listed{l}, loc, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	if !hasLine(got, "Time: 2026-06-12 - 2026-06-14 (all day)") {
		t.Errorf("multi-day all-day Time sub-line missing:\n%q", got)
	}
}

func TestOccurrenceLinesRecurringMaster(t *testing.T) {
	l := listedTimed()
	l.Occurrence.Event.RRule = "FREQ=DAILY"
	l.Event.RRule = "FREQ=DAILY"
	got := occurrenceListLines([]event.Listed{l}, time.UTC, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	if got[0] != "Standup  (recurring)" {
		t.Errorf("header line = %q", got[0])
	}
	if !hasLine(got, "Occurrence start: 2026-06-12 09:00") {
		t.Errorf("missing occurrence start sub-line:\n%q", got)
	}
}

func TestOccurrenceLinesRecurringAllDayMaster(t *testing.T) {
	l := listedTimed()
	l.Occurrence.Event.RRule = "FREQ=WEEKLY"
	l.Occurrence.Start = ts(2026, 6, 12, 0, 0)
	l.Event.AllDay = true
	loc := time.FixedZone("UTC-7", -7*60*60)
	got := occurrenceListLines([]event.Listed{l}, loc, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	if !hasLine(got, "Occurrence start: 2026-06-12") {
		t.Errorf("missing all-day occurrence start sub-line:\n%q", got)
	}
}

func TestOccurrenceLinesEditedOccurrence(t *testing.T) {
	l := listedTimed()
	l.Occurrence.Event.RecurrenceID = ts(2026, 6, 12, 8, 0)
	l.Event.RecurrenceID = time.Unix(ts(2026, 6, 12, 8, 0), 0).UTC()
	got := occurrenceListLines([]event.Listed{l}, time.UTC, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	if got[0] != "Standup  (edited occurrence)" {
		t.Errorf("edited occurrence header line = %q", got[0])
	}
	if hasLine(got, "Occurrence start: 2026-06-12 09:00") {
		t.Errorf("edited occurrence must not get an occurrence start line: %q", got)
	}
}

func TestListDefaultFieldsExcludesColor(t *testing.T) {
	sel := listDefaultFields()
	if sel.has(fieldColor) {
		t.Error("color must not be in the events-list default field set")
	}
	// Color is suppressed in the default list even when the event has one.
	l := listedTimed()
	l.Event.Color = "#EC3E7C"
	got := occurrenceListLines([]event.Listed{l}, time.UTC, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	for _, line := range got {
		if strings.Contains(line, "Color:") {
			t.Errorf("color must be hidden by default, got line %q", line)
		}
	}
	// ... but appears when explicitly requested via --fields color.
	sel2, err := selectFields(eventFieldRegistry, []string{"color"}, false)
	if err != nil {
		t.Fatal(err)
	}
	got = occurrenceListLines([]event.Listed{l}, time.UTC, sel2, calendar.Settings{}, calendar.Info{})
	if !hasLine(got, "Color: strawberry (#EC3E7C)") {
		t.Errorf("--fields color should show the color:\n%q", got)
	}
}

// TestOccurrenceListConsistentAlignment verifies the label column aligns across
// events with different fields (short labels pad to a sibling's longer width).
func TestOccurrenceListConsistentAlignment(t *testing.T) {
	withDesc := listedTimed() // has Location + Description (long labels)
	bare := listedTimed()
	bare.Event.EventID = "evt2"
	bare.Occurrence.Event = &caltypes.RawEvent{ID: "evt2"}
	bare.Event.Summary = "Bare"
	bare.Event.Location = ""
	bare.Event.Description = ""

	got := occurrenceListLines([]event.Listed{withDesc, bare}, time.UTC, listDefaultFields(), calendar.Settings{}, calendar.Info{})

	// Collect the byte offset of the ':' in every "Time:" line; they must all
	// match (one shared column), proving alignment is list-wide, not per-event.
	var colonCols []int
	for _, line := range got {
		trimmed := strings.TrimPrefix(line, "  ")
		if strings.HasPrefix(trimmed, "Time:") {
			colonCols = append(colonCols, strings.IndexByte(line, ':'))
		}
	}
	if len(colonCols) != 2 {
		t.Fatalf("expected 2 Time lines, got %d (\n%s\n)", len(colonCols), strings.Join(got, "\n"))
	}
	if colonCols[0] != colonCols[1] {
		t.Errorf("Time label colons not aligned across events: %v\n%s", colonCols, strings.Join(got, "\n"))
	}
	// The shared width is driven by the longest label present anywhere
	// (Description = 11), so even the bare event's Time value starts past it.
	for _, line := range got {
		if strings.HasPrefix(strings.TrimPrefix(line, "  "), "Time:") {
			if !strings.Contains(line, "Time:        ") { // padded to Description width
				t.Errorf("Time line not padded to the shared column: %q", line)
			}
		}
	}
}

func TestEventDetailRendersEnrichment(t *testing.T) {
	ev := &event.Event{
		EventID: "evt1", UID: "uid1", CalendarID: "cal1",
		Summary:     "Test Event",
		Location:    "Some Test Location",
		Start:       time.Unix(ts(2026, 6, 24, 8, 0), 0).UTC(),
		End:         time.Unix(ts(2026, 6, 24, 8, 30), 0).UTC(),
		Color:       "#EC3E7C",
		IsOrganizer: true,
		Organizer:   &event.Person{Email: "adam@adamcrowder.net", CN: "adam"},
		Attendees: []event.Attendee{
			{Email: "adacrowd@amazon.com", CN: "adacrowd", Role: "REQ-PARTICIPANT", Status: 3},
		},
		Conference: &event.Conference{
			Provider: "2", ID: "MQYTXG4HKC",
			URL: "https://meet.proton.me/join/id-MQYTXG4HKC#pwd-x", Password: "x",
		},
		Notifications:    []caltypes.Notification{{Type: 1, Trigger: "-PT1H"}},
		NotificationsSet: true,
	}

	// Human lines contain the key facts with Title-Case labels.
	sel, err := selectFields(eventFieldRegistry, nil, true)
	if err != nil {
		t.Fatalf("selectFields: %v", err)
	}
	lines := eventDetailLines(ev, time.UTC, sel, calendar.Settings{}, calendar.Info{})
	joined := strings.Join(lines, "\n") + "\n"
	for _, want := range []string{
		"Summary:", "Test Event",
		"Organizer:", "adam <adam@adamcrowder.net>",
		"Attendee:", "adacrowd <adacrowd@amazon.com> (accepted)",
		"Conference (Proton Meet):", "https://meet.proton.me/join/id-MQYTXG4HKC#pwd-x",
		"Reminder (notify):", "-PT1H",
		"Color:", "#EC3E7C",
		"ID:", "evt1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("detail lines missing %q\n%s", want, joined)
		}
	}
	// Color swatch is absent when color is disabled (no TTY in tests).
	if strings.Contains(joined, "\x1b[") {
		t.Errorf("unexpected ANSI escape in non-TTY output:\n%q", joined)
	}
}

func TestSelectFieldsDefaultAllSubsetUnknown(t *testing.T) {
	// Default: curated set excludes uid/calendar_id/rrule.
	def, err := selectFields(eventFieldRegistry, nil, false)
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if !def.has(fieldSummary) || def.has(fieldUID) || def.has(fieldRRule) || def.has(fieldCalendarID) {
		t.Errorf("default set = %v", sortedFieldSet(def))
	}
	// --all: includes the technical fields.
	all, err := selectFields(eventFieldRegistry, nil, true)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if !all.has(fieldUID) || !all.has(fieldRRule) || !all.has(fieldCalendarID) {
		t.Errorf("all set = %v", sortedFieldSet(all))
	}
	// Explicit subset (comma-joined) is honored verbatim; --all ignored.
	sub, err := selectFields(eventFieldRegistry, []string{"summary,location"}, true)
	if err != nil {
		t.Fatalf("subset: %v", err)
	}
	if got := sortedFieldSet(sub); !reflect.DeepEqual(got, []string{"location", "summary"}) {
		t.Errorf("subset = %v", got)
	}
	// Unknown field errors with the valid list.
	if _, err := selectFields(eventFieldRegistry, []string{"bogus"}, false); err == nil ||
		!strings.Contains(err.Error(), "unknown field") || !strings.Contains(err.Error(), "summary") {
		t.Errorf("unknown field error = %v", err)
	}
}

func TestCalendarDetailLines(t *testing.T) {
	c := calendar.Info{
		ID: "cal1", Name: "Personal", Description: "My stuff", Color: "#415DF0",
		Type: 0, MemberID: "mem1", AddressID: "addr1", Email: "me@example.com",
	}
	// Curated default hides email/member/address.
	def, _ := selectFields(calendarFieldRegistry, nil, false)
	joined := strings.Join(calendarDetailLines(c, calendar.Settings{}, true, def), "\n")
	for _, want := range []string{"Name:", "Personal", "Type:", "normal", "Color:", "#415DF0", "ID:", "cal1", "Default:"} {
		if !strings.Contains(joined, want) {
			t.Errorf("default calendar detail missing %q\n%s", want, joined)
		}
	}
	for _, unwanted := range []string{"Email:", "Member ID:", "Address ID:"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("default calendar detail should hide %q\n%s", unwanted, joined)
		}
	}
	// --all reveals them.
	all, _ := selectFields(calendarFieldRegistry, nil, true)
	joinedAll := strings.Join(calendarDetailLines(c, calendar.Settings{}, false, all), "\n")
	for _, want := range []string{"Email:", "me@example.com", "Member ID:", "mem1", "Address ID:", "addr1"} {
		if !strings.Contains(joinedAll, want) {
			t.Errorf("--all calendar detail missing %q\n%s", want, joinedAll)
		}
	}
	// Not-default calendar omits the Default row.
	if strings.Contains(joinedAll, "Default:") {
		t.Errorf("non-default calendar should omit Default row\n%s", joinedAll)
	}
}

func TestCalendarDetailDefaultReminders(t *testing.T) {
	c := calendar.Info{ID: "cal1", Name: "Personal", Color: "#415DF0"}
	set := calendar.Settings{
		DefaultEventDuration:        30,
		DefaultPartDayNotifications: []caltypes.Notification{{Type: 1, Trigger: "-PT15M"}},
		DefaultFullDayNotifications: []caltypes.Notification{{Type: 1, Trigger: "-PT16H"}},
		MakesUserBusy:               1,
	}
	def, _ := selectFields(calendarFieldRegistry, nil, false)
	joined := strings.Join(calendarDetailLines(c, set, false, def), "\n")
	for _, want := range []string{
		"Default reminder (timed) (notify): -PT15M",
		"Default reminder (all-day) (notify): -PT16H",
		"Default duration: 30 min",
		"Makes busy: yes",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("calendar detail missing %q\n%s", want, joined)
		}
	}
}

func TestEventDetailInheritsCalendarDefaults(t *testing.T) {
	cal := calendar.Info{Color: "#415DF0"}
	set := calendar.Settings{DefaultPartDayNotifications: []caltypes.Notification{{Type: 1, Trigger: "-PT30M"}}}
	// Event with no own color/notifications: shows the calendar's.
	ev := &event.Event{
		EventID: "e1", Summary: "Untagged",
		Start: time.Unix(ts(2026, 6, 24, 8, 0), 0).UTC(),
		End:   time.Unix(ts(2026, 6, 24, 8, 30), 0).UTC(),
	}
	sel, _ := selectFields(eventFieldRegistry, nil, true)
	joined := strings.Join(eventDetailLines(ev, time.UTC, sel, set, cal), "\n")
	if !strings.Contains(joined, "Reminder (notify): -PT30M") {
		t.Errorf("inherited reminder missing:\n%s", joined)
	}
	if !strings.Contains(joined, "#415DF0") {
		t.Errorf("inherited color missing:\n%s", joined)
	}

	// Explicitly no reminders: the default must NOT appear.
	ev.NotificationsSet = true
	joined = strings.Join(eventDetailLines(ev, time.UTC, sel, set, cal), "\n")
	if strings.Contains(joined, "-PT30M") {
		t.Errorf("removed-reminder event must not show the default:\n%s", joined)
	}
}

func TestEventDetailShowsEditedOccurrence(t *testing.T) {
	// An exception row (RecurrenceID set, no RRULE) notes which original
	// occurrence it edits.
	ev := &event.Event{
		EventID: "exc1", Summary: "Edited",
		Start:        time.Unix(ts(2026, 6, 29, 10, 0), 0).UTC(),
		End:          time.Unix(ts(2026, 6, 29, 10, 30), 0).UTC(),
		RecurrenceID: time.Unix(ts(2026, 6, 29, 10, 0), 0).UTC(),
	}
	sel, _ := selectFields(eventFieldRegistry, nil, true)
	joined := strings.Join(eventDetailLines(ev, time.UTC, sel, calendar.Settings{}, calendar.Info{}), "\n")
	if !strings.Contains(joined, "Edits occurrence: 2026-06-29 10:00") {
		t.Errorf("edited-occurrence detail missing the original occurrence:\n%s", joined)
	}
	// A plain event must not get the line.
	plain := &event.Event{EventID: "p1", Summary: "Plain", Start: ev.Start, End: ev.End}
	joined = strings.Join(eventDetailLines(plain, time.UTC, sel, calendar.Settings{}, calendar.Info{}), "\n")
	if strings.Contains(joined, "Edits occurrence") {
		t.Errorf("plain event must not show an edits-occurrence line:\n%s", joined)
	}
}

func TestParseHexColor(t *testing.T) {
	r, g, b, ok := parseHexColor("#EC3E7C")
	if !ok || r != 0xEC || g != 0x3E || b != 0x7C {
		t.Errorf("parseHexColor(#EC3E7C) = %d,%d,%d,%v", r, g, b, ok)
	}
	if _, _, _, ok := parseHexColor("nope"); ok {
		t.Error("parseHexColor(nope) ok = true, want false")
	}
	if _, _, _, ok := parseHexColor("#FFF"); ok {
		t.Error("parseHexColor(#FFF) ok = true, want false (only 6-digit supported)")
	}
}

func TestFormatOccurrenceStart(t *testing.T) {
	loc := time.FixedZone("UTC+2", 2*60*60)
	if got := calsvc.FormatOccurrenceStart(ts(2026, 6, 12, 9, 0), false, loc); got != "2026-06-12 11:00" {
		t.Errorf("timed = %q", got)
	}
	if got := calsvc.FormatOccurrenceStart(ts(2026, 6, 12, 0, 0), true, loc); got != "2026-06-12" {
		t.Errorf("all-day = %q", got)
	}
}
