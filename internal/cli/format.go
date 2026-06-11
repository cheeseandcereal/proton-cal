package cli

import (
	"fmt"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/event"
)

// eventJSON is the machine-readable shape of one listed occurrence.
type eventJSON struct {
	ID                string `json:"id"`
	UID               string `json:"uid,omitempty"`
	Summary           string `json:"summary,omitempty"`
	Description       string `json:"description,omitempty"`
	Location          string `json:"location,omitempty"`
	Start             string `json:"start,omitempty"`
	End               string `json:"end,omitempty"`
	AllDay            bool   `json:"all_day"`
	Recurring         bool   `json:"recurring"`
	EditedOccurrence  bool   `json:"edited_occurrence"`
	OccurrenceStartTS int64  `json:"occurrence_start_ts"`
	RRule             string `json:"rrule,omitempty"`
	CalendarID        string `json:"calendar_id,omitempty"`
}

// formatOccurrenceStart renders an occurrence's original start in the form
// `--occurrence` accepts back: the date (all-day events are anchored at
// midnight UTC) or the wall time in loc.
func formatOccurrenceStart(ts int64, allDay bool, loc *time.Location) string {
	t := time.Unix(ts, 0)
	if allDay {
		return t.UTC().Format("2006-01-02")
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

// occurrenceLines renders one listed occurrence as human-readable output
// lines. Times are rendered in loc (all-day dates in UTC, their canonical
// anchor zone).
func occurrenceLines(l event.Listed, loc *time.Location) []string {
	raw := l.Occurrence.Event
	ev := l.Event
	summary := ev.Summary
	if summary == "" {
		summary = "(no title)"
	}
	recurring := raw.IsMaster()
	switch {
	case recurring:
		summary += "  (recurring)"
	case raw.IsException():
		summary += "  (edited occurrence)"
	}

	start := time.Unix(l.Occurrence.Start, 0)
	end := time.Unix(l.Occurrence.End, 0)
	var head string
	if ev.AllDay {
		head = fmt.Sprintf("  %s (all day)  %s", start.UTC().Format("2006-01-02"), summary)
	} else {
		head = fmt.Sprintf("  %s - %s  %s",
			start.In(loc).Format("2006-01-02 15:04"), end.In(loc).Format("15:04"), summary)
	}
	if ev.Location != "" {
		head += fmt.Sprintf("  [%s]", ev.Location)
	}

	lines := []string{head}
	if ev.Description != "" {
		lines = append(lines, "      "+ev.Description)
	}
	lines = append(lines, "    ID: "+ev.EventID)
	if recurring {
		lines = append(lines, "    occurrence start: "+formatOccurrenceStart(l.Occurrence.Start, ev.AllDay, loc))
	}
	return lines
}

// occurrenceJSON maps one listed occurrence to its machine-readable shape.
// Timed starts/ends are RFC3339 in loc; all-day ones in UTC (their canonical
// anchor zone, so the date is the event's date).
func occurrenceJSON(l event.Listed, loc *time.Location) eventJSON {
	raw := l.Occurrence.Event
	ev := l.Event
	renderLoc := loc
	if ev.AllDay {
		renderLoc = time.UTC
	}
	return eventJSON{
		ID:                ev.EventID,
		UID:               ev.UID,
		Summary:           ev.Summary,
		Description:       ev.Description,
		Location:          ev.Location,
		Start:             time.Unix(l.Occurrence.Start, 0).In(renderLoc).Format(time.RFC3339),
		End:               time.Unix(l.Occurrence.End, 0).In(renderLoc).Format(time.RFC3339),
		AllDay:            ev.AllDay,
		Recurring:         raw.IsMaster(),
		EditedOccurrence:  raw.IsException(),
		OccurrenceStartTS: l.Occurrence.Start,
		RRule:             ev.RRule,
		CalendarID:        ev.CalendarID,
	}
}
