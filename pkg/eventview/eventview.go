// Package eventview holds the shared, surface-independent semantics for
// presenting a decrypted event: name lookups (attendee status, conference
// provider), identity formatting, and the resolution of effective reminders
// and color from the event plus its calendar defaults. The CLI and the MCP
// server both build their output from these helpers so the two surfaces
// cannot diverge in meaning (only in cosmetic styling).
package eventview

import (
	"fmt"

	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/event"
)

// AttendeeStatusName maps the ATTENDEE_STATUS_API code to a label ("" when
// unknown, e.g. status -1 for an attendee with no matching row token).
func AttendeeStatusName(status int) string {
	switch status {
	case 0:
		return "needs-action"
	case 1:
		return "tentative"
	case 2:
		return "declined"
	case 3:
		return "accepted"
	default:
		return ""
	}
}

// ConferenceProviderName maps the VIDEO_CONFERENCE_PROVIDER code to a label,
// falling back to the raw code for unknown providers.
func ConferenceProviderName(provider string) string {
	switch provider {
	case "1":
		return "Zoom"
	case "2":
		return "Proton Meet"
	default:
		return provider
	}
}

// PersonString renders an email/CN pair as "CN <email>", or just the email
// when there is no distinct common name.
func PersonString(email, cn string) string {
	if cn != "" && cn != email {
		return fmt.Sprintf("%s <%s>", cn, email)
	}
	return email
}

// PersonOf formats an event.Person (organizer/attendee identity).
func PersonOf(p *event.Person) string {
	if p == nil {
		return ""
	}
	return PersonString(p.Email, p.CN)
}

// AttendeeString renders an attendee with its RSVP status suffix.
func AttendeeString(a event.Attendee) string {
	who := PersonString(a.Email, a.CN)
	if st := AttendeeStatusName(a.Status); st != "" {
		who += " (" + st + ")"
	}
	return who
}

// SummaryOr returns the event's summary, or "(no title)" when it is empty.
func SummaryOr(ev *event.Event) string {
	if ev.Summary == "" {
		return "(no title)"
	}
	return ev.Summary
}

// RecurrenceSuffix returns a recurrence marker for a row ("  (recurring)" for a
// master, "  (edited occurrence)" for an exception, "" otherwise), space-prefixed.
func RecurrenceSuffix(raw *caltypes.RawEvent) string {
	switch {
	case raw.IsMaster():
		return "  (recurring)"
	case raw.IsException():
		return "  (edited occurrence)"
	default:
		return ""
	}
}

// ReminderKind maps a notification Type to a label (0 = email, else notify).
func ReminderKind(typ int) string {
	if typ == 0 {
		return "email"
	}
	return "notify"
}

// EffectiveReminders returns the reminders that apply: the event's own when set
// (explicitly none -> empty), else the calendar default for its all-day/timed kind.
func EffectiveReminders(ev *event.Event, set calendar.Settings) []caltypes.Notification {
	if ev.NotificationsSet {
		return ev.Notifications
	}
	return set.DefaultNotifications(ev.AllDay)
}

// EffectiveColor returns the color that applies to an event: its own when
// set, otherwise the calendar's color.
func EffectiveColor(ev *event.Event, cal calendar.Info) string {
	if ev.Color != "" {
		return ev.Color
	}
	return cal.Color
}

// CalendarHeaderLines renders the shared calendar listing lines: a
// "<name> (<type>)" header (with "  [default]" when c.ID == defaultID) and an
// "  ID: <id>" line. Pass "" for defaultID to suppress the marker.
func CalendarHeaderLines(c calendar.Info, defaultID string) []string {
	header := c.Name + " (" + c.TypeString() + ")"
	if defaultID != "" && c.ID == defaultID {
		header += "  [default]"
	}
	return []string{header, "  ID: " + c.ID}
}

// UpdateOutcomeMessage renders the update confirmation: a headline plus a
// follow-up note about removed exceptions ("" when none were removed).
func UpdateOutcomeMessage(outcome *event.UpdateOutcome) (headline, note string) {
	headline = "Event updated."
	if outcome.EditedOccurrence {
		headline = "Occurrence updated."
	}
	if outcome.RemovedExceptions > 0 {
		note = fmt.Sprintf("Removed %d edited occurrence(s) invalidated by the series change.", outcome.RemovedExceptions)
	}
	return headline, note
}

// DeleteResultMessage renders the delete confirmation. withID true names the
// event ID in the whole-event case (MCP form); false is bare "Event deleted." (CLI).
func DeleteResultMessage(res *event.DeleteResult, eventID string, withID bool) string {
	switch res.Kind {
	case event.DeletedOccurrence:
		return "Occurrence deleted."
	case event.DeletedSeries:
		return fmt.Sprintf("Recurring series deleted (%d row(s)).", res.RowsDeleted)
	case event.DeletedEvent:
		if withID {
			return "Event " + eventID + " deleted."
		}
		return "Event deleted."
	default:
		return fmt.Sprintf("Deleted (%s, %d row(s)).", res.Kind, res.RowsDeleted)
	}
}
