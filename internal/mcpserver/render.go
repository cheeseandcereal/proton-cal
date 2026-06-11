package mcpserver

import (
	"fmt"
	"strings"
	"time"

	"github.com/cheeseandcereal/proton-cal-go/internal/calendar"
	"github.com/cheeseandcereal/proton-cal-go/internal/event"
)

// calTypeString maps a calendar type to its display name.
func calTypeString(t int) string {
	switch t {
	case 0:
		return "normal"
	case 1:
		return "subscribed"
	case 2:
		return "holidays"
	default:
		return fmt.Sprintf("type %d", t)
	}
}

// isDefaultCalendar reports whether cal matches the configured default
// calendar selector (by ID or case-insensitive name).
func isDefaultCalendar(cal calendar.Info, defaultSelector string) bool {
	if defaultSelector == "" {
		return false
	}
	return cal.ID == defaultSelector || strings.EqualFold(cal.Name, defaultSelector)
}

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
		if isDefaultCalendar(c, defaultSelector) {
			marker = "  [default]"
		}
		fmt.Fprintf(&b, "%s (%s)%s\n", c.Name, calTypeString(c.Type), marker)
		fmt.Fprintf(&b, "  ID: %s", c.ID)
	}
	return b.String()
}

// formatOccurrenceStart renders an occurrence's original start in the form
// the occurrence argument accepts back: the date for all-day events
// (anchored at midnight UTC) or the wall time in loc.
func formatOccurrenceStart(ts int64, allDay bool, loc *time.Location) string {
	t := time.Unix(ts, 0)
	if allDay {
		return t.UTC().Format("2006-01-02")
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

// renderOccurrence renders one listed occurrence as a text block (port of
// the Python list_events line format). Decrypt failures degrade to an error
// line; they never kill the listing.
func renderOccurrence(l event.Listed, loc *time.Location) string {
	raw := l.Occurrence.Event
	if l.Err != nil {
		return fmt.Sprintf("- (decrypt error for %s: %v)", raw.ID, l.Err)
	}

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
	line += "\n  ID: " + ev.EventID
	if recurring {
		line += "\n  occurrence start: " + formatOccurrenceStart(l.Occurrence.Start, ev.AllDay, loc)
	}
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

// renderDeleteResult renders the delete_event reply by result kind.
func renderDeleteResult(res *event.DeleteResult, eventID string) string {
	switch res.Kind {
	case "occurrence":
		return "Occurrence deleted."
	case "series":
		return fmt.Sprintf("Recurring series deleted (%d row(s)).", res.RowsDeleted)
	case "event":
		return fmt.Sprintf("Event %s deleted.", eventID)
	default:
		return fmt.Sprintf("Deleted (%s, %d row(s)).", res.Kind, res.RowsDeleted)
	}
}
