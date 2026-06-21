package mcpserver

import (
	"fmt"
	"strings"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
	"github.com/cheeseandcereal/proton-cal/internal/event"
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
// list_events line format.
func renderOccurrence(l event.Listed, loc *time.Location) string {
	raw := l.Occurrence.Event
	ev := l.Event
	summary := ev.Summary
	if summary == "" {
		summary = "(no title)"
	}

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
	switch {
	case recurring:
		line += "  (recurring)"
	case raw.IsException():
		line += "  (edited occurrence)"
	}
	if ev.Location != "" {
		line += fmt.Sprintf("  [%s]", ev.Location)
	}
	if ev.Description != "" {
		line += "\n  " + ev.Description
	}
	if detail := renderEventExtras(ev); detail != "" {
		line += "\n" + detail
	}
	line += "\n  ID: " + ev.EventID
	if recurring {
		line += "\n  occurrence start: " + calsvc.FormatOccurrenceStart(l.Occurrence.Start, ev.AllDay, loc)
	}
	return line
}

// attendeeStatusName maps the ATTENDEE_STATUS_API code to a label.
func attendeeStatusName(status int) string {
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

// conferenceProviderName maps the VIDEO_CONFERENCE_PROVIDER code to a label.
func conferenceProviderName(provider string) string {
	switch provider {
	case "1":
		return "Zoom"
	case "2":
		return "Proton Meet"
	default:
		return provider
	}
}

// renderEventExtras renders the enrichment lines (organizer, attendees,
// conferencing, reminders) shared by list_events and get_event. Returns ""
// when the event has none. Each line is indented two spaces.
func renderEventExtras(ev *event.Event) string {
	var lines []string
	if ev.Organizer != nil {
		who := ev.Organizer.Email
		if ev.Organizer.CN != "" && ev.Organizer.CN != who {
			who = fmt.Sprintf("%s <%s>", ev.Organizer.CN, ev.Organizer.Email)
		}
		lines = append(lines, "  organizer: "+who)
	}
	for _, a := range ev.Attendees {
		who := a.Email
		if a.CN != "" && a.CN != who {
			who = fmt.Sprintf("%s <%s>", a.CN, a.Email)
		}
		if st := attendeeStatusName(a.Status); st != "" {
			who += " (" + st + ")"
		}
		lines = append(lines, "  attendee: "+who)
	}
	if c := ev.Conference; c != nil && c.URL != "" {
		lines = append(lines, "  "+conferenceProviderName(c.Provider)+": "+c.URL)
	}
	for _, n := range ev.Notifications {
		kind := "notify"
		if n.Type == 0 {
			kind = "email"
		}
		lines = append(lines, "  reminder ("+kind+"): "+n.Trigger)
	}
	return strings.Join(lines, "\n")
}

// renderEventDetail renders the get_event reply for a single fetched event.
func renderEventDetail(ev *event.Event, loc *time.Location) string {
	summary := ev.Summary
	if summary == "" {
		summary = "(no title)"
	}
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
	if detail := renderEventExtras(ev); detail != "" {
		line += "\n" + detail
	}
	if ev.Color != "" {
		line += "\n  color: " + ev.Color
	}
	line += "\n  ID: " + ev.EventID
	return line
}

// renderEvents renders the list_events reply for a window of expanded
// occurrences.
func renderEvents(listed []event.Listed, days int, loc *time.Location) string {
	if len(listed) == 0 {
		return fmt.Sprintf("No events in the next %d days.", days)
	}
	lines := make([]string, 0, len(listed)+1)
	lines = append(lines, fmt.Sprintf("Events in the next %d days:\n", days))
	for _, l := range listed {
		lines = append(lines, renderOccurrence(l, loc))
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
	switch res.Kind {
	case event.DeletedOccurrence:
		return "Occurrence deleted."
	case event.DeletedSeries:
		return fmt.Sprintf("Recurring series deleted (%d row(s)).", res.RowsDeleted)
	case event.DeletedEvent:
		return fmt.Sprintf("Event %s deleted.", eventID)
	default:
		return fmt.Sprintf("Deleted (%s, %d row(s)).", res.Kind, res.RowsDeleted)
	}
}
