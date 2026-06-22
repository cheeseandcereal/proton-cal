package mcpserver

import (
	"fmt"
	"strings"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/eventview"
)

// renderCalendars renders the list_calendars reply: name, type, ID and a
// default marker.
func renderCalendars(cals []calendar.Info, defaultSelector string) string {
	if len(cals) == 0 {
		return "No calendars found."
	}
	var b strings.Builder
	for i, c := range cals {
		if i > 0 {
			b.WriteByte('\n')
		}
		marker := ""
		if c.Matches(defaultSelector) {
			marker = "  [default]"
		}
		fmt.Fprintf(&b, "%s (%s)%s\n", c.Name, c.TypeString(), marker)
		fmt.Fprintf(&b, "  ID: %s", c.ID)
	}
	return b.String()
}

// renderOccurrence renders one listed occurrence as a text block in the
// list_events line format. set/cal resolve the effective reminders and color.
func renderOccurrence(l event.Listed, loc *time.Location, set calendar.Settings, cal calendar.Info) string {
	raw := l.Occurrence.Event
	ev := l.Event
	summary := eventview.SummaryOr(ev)

	start := time.Unix(l.Occurrence.Start, 0)
	end := time.Unix(l.Occurrence.End, 0)
	var timeStr string
	if ev.AllDay {
		timeStr = start.UTC().Format("Mon 02 Jan") + " (all day)"
	} else {
		timeStr = start.In(loc).Format("Mon 02 Jan 15:04") + " - " + end.In(loc).Format("15:04")
	}

	line := fmt.Sprintf("- %s  %s", timeStr, summary)
	recurring := raw.IsMaster()
	line += eventview.RecurrenceSuffix(raw)
	if ev.Location != "" {
		line += fmt.Sprintf("  [%s]", ev.Location)
	}
	if ev.Description != "" {
		line += "\n  " + ev.Description
	}
	if detail := renderEventExtras(ev, set, cal); detail != "" {
		line += "\n" + detail
	}
	line += "\n  ID: " + ev.EventID
	if recurring {
		line += "\n  occurrence start: " + calsvc.FormatOccurrenceStart(l.Occurrence.Start, ev.AllDay, loc)
	}
	return line
}

// renderEventExtras renders the enrichment lines (organizer, attendees,
// conferencing, reminders) shared by list_events and get_event, with the
// effective reminders resolved from the calendar defaults. Returns "" when
// the event has none. Each line is indented two spaces.
func renderEventExtras(ev *event.Event, set calendar.Settings, cal calendar.Info) string {
	var lines []string
	if ev.Organizer != nil {
		lines = append(lines, "  organizer: "+eventview.PersonOf(ev.Organizer))
	}
	for _, a := range ev.Attendees {
		lines = append(lines, "  attendee: "+eventview.AttendeeString(a))
	}
	if c := ev.Conference; c != nil && c.URL != "" {
		lines = append(lines, "  "+eventview.ConferenceProviderName(c.Provider)+": "+c.URL)
	}
	for _, n := range eventview.EffectiveReminders(ev, set) {
		lines = append(lines, "  reminder ("+eventview.ReminderKind(n.Type)+"): "+n.Trigger)
	}
	if c := eventview.EffectiveColor(ev, cal); c != "" {
		lines = append(lines, "  color: "+c)
	}
	return strings.Join(lines, "\n")
}

// renderEventDetail renders the get_event reply for a single fetched event.
func renderEventDetail(ev *event.Event, loc *time.Location, set calendar.Settings, cal calendar.Info) string {
	summary := eventview.SummaryOr(ev)
	var when string
	if ev.AllDay {
		when = ev.Start.UTC().Format("Mon 02 Jan") + " (all day)"
	} else {
		when = ev.Start.In(loc).Format("Mon 02 Jan 15:04") + " - " + ev.End.In(loc).Format("15:04")
	}
	line := fmt.Sprintf("%s\n  %s", summary, when)
	if ev.IsRecurring() {
		line += "  (recurring)"
	}
	if ev.Location != "" {
		line += "\n  location: " + ev.Location
	}
	if ev.Description != "" {
		line += "\n  description: " + ev.Description
	}
	if detail := renderEventExtras(ev, set, cal); detail != "" {
		line += "\n" + detail
	}
	line += "\n  ID: " + ev.EventID
	return line
}

// renderEvents renders the list_events reply for a window of expanded
// occurrences.
func renderEvents(listed []event.Listed, days int, loc *time.Location, set calendar.Settings, cal calendar.Info) string {
	if len(listed) == 0 {
		return fmt.Sprintf("No events in the next %d days.", days)
	}
	lines := make([]string, 0, len(listed)+1)
	lines = append(lines, fmt.Sprintf("Events in the next %d days:\n", days))
	for _, l := range listed {
		lines = append(lines, renderOccurrence(l, loc, set, cal))
	}
	return strings.Join(lines, "\n")
}

// renderCreated renders the create_event reply.
func renderCreated(created *calsvc.CreatedEvent) string {
	var when string
	if created.AllDay {
		when = created.Start.UTC().Format("Mon 02 Jan")
	} else {
		when = created.Start.Format("Mon 02 Jan 15:04") + " - " + created.End.Format("15:04")
	}
	out := fmt.Sprintf("Event created: %s\n  %s", created.Summary, when)
	if created.ID != "" {
		out += "\n  ID: " + created.ID
	}
	if created.RRule != "" {
		out += "\n  Repeats: " + created.RRule
	}
	return out
}

// renderDeleteResult renders the delete_event reply by result kind.
func renderDeleteResult(res *event.DeleteResult, eventID string) string {
	return eventview.DeleteResultMessage(res, eventID, true)
}
