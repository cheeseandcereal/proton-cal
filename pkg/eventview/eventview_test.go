package eventview

import (
	"reflect"
	"testing"

	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/event"
)

func settings() calendar.Settings {
	return calendar.Settings{
		DefaultPartDayNotifications: []caltypes.Notification{{Type: 1, Trigger: "-PT15M"}},
		DefaultFullDayNotifications: []caltypes.Notification{{Type: 1, Trigger: "-PT16H"}},
	}
}

func TestEffectiveRemindersInherit(t *testing.T) {
	// Not set -> inherit the calendar default for the event's kind.
	timed := &event.Event{NotificationsSet: false, AllDay: false}
	if got := EffectiveReminders(timed, settings()); len(got) != 1 || got[0].Trigger != "-PT15M" {
		t.Errorf("timed inherit = %+v, want part-day default", got)
	}
	allDay := &event.Event{NotificationsSet: false, AllDay: true}
	if got := EffectiveReminders(allDay, settings()); len(got) != 1 || got[0].Trigger != "-PT16H" {
		t.Errorf("all-day inherit = %+v, want full-day default", got)
	}
}

func TestEffectiveRemindersExplicit(t *testing.T) {
	// Explicitly none ([]): show nothing, NOT the default.
	none := &event.Event{NotificationsSet: true, Notifications: nil}
	if got := EffectiveReminders(none, settings()); len(got) != 0 {
		t.Errorf("explicit-none = %+v, want empty", got)
	}
	// Custom: passthrough.
	custom := &event.Event{NotificationsSet: true, Notifications: []caltypes.Notification{{Type: 0, Trigger: "-PT5M"}}}
	got := EffectiveReminders(custom, settings())
	if !reflect.DeepEqual(got, custom.Notifications) {
		t.Errorf("custom = %+v, want passthrough", got)
	}
}

func TestEffectiveColor(t *testing.T) {
	cal := calendar.Info{Color: "#415DF0"}
	own := &event.Event{Color: "#EC3E7C"}
	if got := EffectiveColor(own, cal); got != "#EC3E7C" {
		t.Errorf("own color = %q", got)
	}
	inherit := &event.Event{Color: ""}
	if got := EffectiveColor(inherit, cal); got != "#415DF0" {
		t.Errorf("inherited color = %q, want calendar color", got)
	}
}

func TestNameAndPersonHelpers(t *testing.T) {
	if AttendeeStatusName(3) != "accepted" || AttendeeStatusName(-1) != "" {
		t.Error("AttendeeStatusName")
	}
	if ConferenceProviderName("2") != "Proton Meet" || ConferenceProviderName("9") != "9" {
		t.Error("ConferenceProviderName")
	}
	if ReminderKind(0) != "email" || ReminderKind(1) != "notify" {
		t.Error("ReminderKind")
	}
	if PersonString("a@b", "A") != "A <a@b>" || PersonString("a@b", "") != "a@b" || PersonString("a@b", "a@b") != "a@b" {
		t.Error("PersonString")
	}
	a := event.Attendee{Email: "a@b", CN: "A", Status: 2}
	if AttendeeString(a) != "A <a@b> (declined)" {
		t.Errorf("AttendeeString = %q", AttendeeString(a))
	}
}

func TestUpdateOutcomeMessage(t *testing.T) {
	h, n := UpdateOutcomeMessage(&event.UpdateOutcome{})
	if h != "Event updated." || n != "" {
		t.Errorf("plain update = %q / %q", h, n)
	}
	h, n = UpdateOutcomeMessage(&event.UpdateOutcome{EditedOccurrence: true})
	if h != "Occurrence updated." || n != "" {
		t.Errorf("occurrence update = %q / %q", h, n)
	}
	h, n = UpdateOutcomeMessage(&event.UpdateOutcome{RemovedExceptions: 2})
	if h != "Event updated." || n != "Removed 2 edited occurrence(s) invalidated by the series change." {
		t.Errorf("removed-exceptions update = %q / %q", h, n)
	}
}

func TestDeleteResultMessage(t *testing.T) {
	tests := []struct {
		kind   event.DeleteKind
		rows   int
		withID bool
		want   string
	}{
		{event.DeletedOccurrence, 1, true, "Occurrence deleted."},
		{event.DeletedSeries, 3, true, "Recurring series deleted (3 row(s))."},
		{event.DeletedEvent, 1, true, "Event ev-9 deleted."},
		{event.DeletedEvent, 1, false, "Event deleted."},
		{event.DeleteKind("other"), 5, false, "Deleted (other, 5 row(s))."},
	}
	for _, tt := range tests {
		res := &event.DeleteResult{Kind: tt.kind, RowsDeleted: tt.rows}
		if got := DeleteResultMessage(res, "ev-9", tt.withID); got != tt.want {
			t.Errorf("kind %s withID=%v: got %q, want %q", tt.kind, tt.withID, got, tt.want)
		}
	}
}

func TestPersonOf(t *testing.T) {
	if PersonOf(nil) != "" {
		t.Error("nil person must be empty")
	}
	if got := PersonOf(&event.Person{Email: "a@b", CN: "Alice"}); got != "Alice <a@b>" {
		t.Errorf("PersonOf = %q", got)
	}
}

func TestSummaryOr(t *testing.T) {
	if got := SummaryOr(&event.Event{Summary: "Hi"}); got != "Hi" {
		t.Errorf("got %q", got)
	}
	if got := SummaryOr(&event.Event{}); got != "(no title)" {
		t.Errorf("empty summary = %q, want (no title)", got)
	}
}

func TestRecurrenceSuffix(t *testing.T) {
	if got := RecurrenceSuffix(&caltypes.RawEvent{RRule: "FREQ=DAILY"}); got != "  (recurring)" {
		t.Errorf("master = %q", got)
	}
	if got := RecurrenceSuffix(&caltypes.RawEvent{RecurrenceID: 123}); got != "  (edited occurrence)" {
		t.Errorf("exception = %q", got)
	}
	if got := RecurrenceSuffix(&caltypes.RawEvent{}); got != "" {
		t.Errorf("plain = %q, want empty", got)
	}
}

func TestCalendarHeaderLines(t *testing.T) {
	c := calendar.Info{ID: "cal1", Name: "Work", Type: 0}
	lines := CalendarHeaderLines(c, "cal1")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %v", lines)
	}
	if lines[0] != "Work (normal)  [default]" {
		t.Errorf("header = %q", lines[0])
	}
	if lines[1] != "  ID: cal1" {
		t.Errorf("id line = %q", lines[1])
	}
	// Non-default: no marker.
	if got := CalendarHeaderLines(c, "other")[0]; got != "Work (normal)" {
		t.Errorf("non-default header = %q", got)
	}
}
