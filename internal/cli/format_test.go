package cli

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
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
	got := occurrenceLines(listedTimed(), time.UTC, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	// Head: date/time (with timezone) + summary only; no inline location.
	if got[0] != "  2026-06-12 09:00 UTC - 09:30 UTC  Standup" {
		t.Errorf("head line = %q", got[0])
	}
	if strings.Contains(got[0], "Zoom") {
		t.Errorf("location must not be inline on the head line: %q", got[0])
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
	got := occurrenceLines(listedTimed(), loc, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	if got[0] != "  2026-06-12 11:00 UTC+2 - 11:30 UTC+2  Standup" {
		t.Errorf("head line in UTC+2 = %q", got[0])
	}
}

func TestOccurrenceLinesNoTitleNoExtras(t *testing.T) {
	l := listedTimed()
	l.Event.Summary = ""
	l.Event.Description = ""
	l.Event.Location = ""
	got := occurrenceLines(l, time.UTC, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	want := []string{
		"  2026-06-12 09:00 UTC - 09:30 UTC  (no title)",
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
	got := occurrenceLines(l, loc, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	if got[0] != "  2026-06-12 (all day)  Standup" {
		t.Errorf("all-day head line = %q", got[0])
	}
}

func TestOccurrenceLinesRecurringMaster(t *testing.T) {
	l := listedTimed()
	l.Occurrence.Event.RRule = "FREQ=DAILY"
	l.Event.RRule = "FREQ=DAILY"
	got := occurrenceLines(l, time.UTC, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	if got[0] != "  2026-06-12 09:00 UTC - 09:30 UTC  Standup  (recurring)" {
		t.Errorf("head line = %q", got[0])
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
	got := occurrenceLines(l, loc, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	if !hasLine(got, "Occurrence start: 2026-06-12") {
		t.Errorf("missing all-day occurrence start sub-line:\n%q", got)
	}
}

func TestOccurrenceLinesEditedOccurrence(t *testing.T) {
	l := listedTimed()
	l.Occurrence.Event.RecurrenceID = ts(2026, 6, 12, 8, 0)
	l.Event.RecurrenceID = time.Unix(ts(2026, 6, 12, 8, 0), 0).UTC()
	got := occurrenceLines(l, time.UTC, listDefaultFields(), calendar.Settings{}, calendar.Info{})
	if got[0] != "  2026-06-12 09:00 UTC - 09:30 UTC  Standup  (edited occurrence)" {
		t.Errorf("edited occurrence head line = %q", got[0])
	}
	if hasLine(got, "Occurrence start: 2026-06-12 09:00") {
		t.Errorf("edited occurrence must not get an occurrence start line: %q", got)
	}
}

func TestOccurrenceJSONTimed(t *testing.T) {
	l := listedTimed()
	l.Occurrence.Event.RRule = "FREQ=DAILY"
	l.Event.RRule = "FREQ=DAILY"
	loc := time.FixedZone("UTC+2", 2*60*60)
	got := occurrenceJSON(l, loc, calendar.Settings{}, calendar.Info{})
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
	got := occurrenceJSON(l, loc, calendar.Settings{}, calendar.Info{})
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
	l.Event.RecurrenceID = time.Unix(ts(2026, 6, 12, 8, 0), 0).UTC()
	got := occurrenceJSON(l, time.UTC, calendar.Settings{}, calendar.Info{})
	if !got.EditedOccurrence || got.Recurring {
		t.Errorf("EditedOccurrence=%v Recurring=%v, want true/false", got.EditedOccurrence, got.Recurring)
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

	// JSON shape.
	j := eventDetailJSON(ev, time.UTC, calendar.Settings{}, calendar.Info{})
	if j.Color != "#EC3E7C" || !j.IsOrganizer {
		t.Errorf("color/isOrganizer = %q/%v", j.Color, j.IsOrganizer)
	}
	if j.Organizer == nil || j.Organizer.Email != "adam@adamcrowder.net" {
		t.Errorf("organizer = %+v", j.Organizer)
	}
	if len(j.Attendees) != 1 || j.Attendees[0].Status != "accepted" {
		t.Errorf("attendees = %+v", j.Attendees)
	}
	if j.Conference == nil || j.Conference.Provider != "Proton Meet" {
		t.Errorf("conference = %+v", j.Conference)
	}
	if len(j.Notifications) != 1 || j.Notifications[0].Trigger != "-PT1H" {
		t.Errorf("notifications = %+v", j.Notifications)
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
	}
	def, _ := selectFields(calendarFieldRegistry, nil, false)
	joined := strings.Join(calendarDetailLines(c, set, false, def), "\n")
	for _, want := range []string{
		"Default reminder (timed) (notify): -PT15M",
		"Default reminder (all-day) (notify): -PT16H",
		"Default duration: 30 min",
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
