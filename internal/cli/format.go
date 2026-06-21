package cli

import (
	"fmt"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
	"github.com/cheeseandcereal/proton-cal/internal/event"
)

// eventJSON is the machine-readable shape of one listed occurrence.
type eventJSON struct {
	ID                string             `json:"id"`
	UID               string             `json:"uid,omitempty"`
	Summary           string             `json:"summary,omitempty"`
	Description       string             `json:"description,omitempty"`
	Location          string             `json:"location,omitempty"`
	Start             string             `json:"start,omitempty"`
	End               string             `json:"end,omitempty"`
	AllDay            bool               `json:"all_day"`
	Recurring         bool               `json:"recurring"`
	EditedOccurrence  bool               `json:"edited_occurrence"`
	OccurrenceStartTS int64              `json:"occurrence_start_ts"`
	RRule             string             `json:"rrule,omitempty"`
	CalendarID        string             `json:"calendar_id,omitempty"`
	Color             string             `json:"color,omitempty"`
	IsOrganizer       bool               `json:"is_organizer,omitempty"`
	Organizer         *personJSON        `json:"organizer,omitempty"`
	Attendees         []attendeeJSON     `json:"attendees,omitempty"`
	MoreAttendees     bool               `json:"more_attendees,omitempty"`
	Conference        *conferenceJSON    `json:"conference,omitempty"`
	Notifications     []notificationJSON `json:"notifications,omitempty"`
}

type personJSON struct {
	Email string `json:"email,omitempty"`
	CN    string `json:"cn,omitempty"`
}

type attendeeJSON struct {
	Email    string `json:"email,omitempty"`
	CN       string `json:"cn,omitempty"`
	Role     string `json:"role,omitempty"`
	PartStat string `json:"partstat,omitempty"`
	RSVP     string `json:"rsvp,omitempty"`
	// Status is the live API RSVP: "needs-action", "tentative", "declined",
	// "accepted", or "" when unknown.
	Status string `json:"status,omitempty"`
}

type conferenceJSON struct {
	Provider string `json:"provider,omitempty"`
	ID       string `json:"id,omitempty"`
	URL      string `json:"url,omitempty"`
	Password string `json:"password,omitempty"`
	Host     string `json:"host,omitempty"`
}

type notificationJSON struct {
	Type    int    `json:"type"`
	Trigger string `json:"trigger"`
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

// enrichEventJSON copies the detail fields from a decrypted event onto an
// eventJSON. Shared by the list and get paths.
func enrichEventJSON(j *eventJSON, ev *event.Event) {
	j.Color = ev.Color
	j.IsOrganizer = ev.IsOrganizer
	j.MoreAttendees = ev.MoreAttendees
	if ev.Organizer != nil {
		j.Organizer = &personJSON{Email: ev.Organizer.Email, CN: ev.Organizer.CN}
	}
	for _, a := range ev.Attendees {
		j.Attendees = append(j.Attendees, attendeeJSON{
			Email: a.Email, CN: a.CN, Role: a.Role, PartStat: a.PartStat,
			RSVP: a.RSVP, Status: attendeeStatusName(a.Status),
		})
	}
	if ev.Conference != nil {
		j.Conference = &conferenceJSON{
			Provider: conferenceProviderName(ev.Conference.Provider),
			ID:       ev.Conference.ID, URL: ev.Conference.URL,
			Password: ev.Conference.Password, Host: ev.Conference.Host,
		}
	}
	for _, n := range ev.Notifications {
		j.Notifications = append(j.Notifications, notificationJSON{Type: n.Type, Trigger: n.Trigger})
	}
}

// detailLines renders the extra detail lines (attendees, conference,
// reminders) shared by list and get human output. Each line is already
// indented.
func detailLines(ev *event.Event, indent string) []string {
	var lines []string
	if ev.Organizer != nil {
		who := ev.Organizer.Email
		if ev.Organizer.CN != "" && ev.Organizer.CN != who {
			who = fmt.Sprintf("%s <%s>", ev.Organizer.CN, ev.Organizer.Email)
		}
		lines = append(lines, indent+"organizer: "+who)
	}
	for _, a := range ev.Attendees {
		who := a.Email
		if a.CN != "" && a.CN != who {
			who = fmt.Sprintf("%s <%s>", a.CN, a.Email)
		}
		st := attendeeStatusName(a.Status)
		if st != "" {
			who += " (" + st + ")"
		}
		lines = append(lines, indent+"attendee: "+who)
	}
	if c := ev.Conference; c != nil && c.URL != "" {
		lines = append(lines, indent+conferenceProviderName(c.Provider)+": "+c.URL)
	}
	for _, n := range ev.Notifications {
		kind := "notify"
		if n.Type == 0 {
			kind = "email"
		}
		lines = append(lines, indent+"reminder ("+kind+"): "+n.Trigger)
	}
	return lines
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
	lines = append(lines, detailLines(ev, "    ")...)
	lines = append(lines, "    ID: "+ev.EventID)
	if recurring {
		lines = append(lines, "    occurrence start: "+calsvc.FormatOccurrenceStart(l.Occurrence.Start, ev.AllDay, loc))
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
	j := eventJSON{
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
	enrichEventJSON(&j, ev)
	return j
}

// eventDetailJSON maps a single fetched event (not an expanded occurrence)
// to its machine-readable shape. Times are the event's own start/end.
func eventDetailJSON(ev *event.Event, loc *time.Location) eventJSON {
	renderLoc := loc
	if ev.AllDay {
		renderLoc = time.UTC
	}
	j := eventJSON{
		ID:          ev.EventID,
		UID:         ev.UID,
		Summary:     ev.Summary,
		Description: ev.Description,
		Location:    ev.Location,
		Start:       ev.Start.In(renderLoc).Format(time.RFC3339),
		End:         ev.End.In(renderLoc).Format(time.RFC3339),
		AllDay:      ev.AllDay,
		Recurring:   ev.IsRecurring(),
		RRule:       ev.RRule,
		CalendarID:  ev.CalendarID,
	}
	enrichEventJSON(&j, ev)
	return j
}

// eventDetailLines renders a single fetched event as human-readable lines.
func eventDetailLines(ev *event.Event, loc *time.Location) []string {
	summary := ev.Summary
	if summary == "" {
		summary = "(no title)"
	}
	var head string
	if ev.AllDay {
		head = fmt.Sprintf("%s (all day)  %s", ev.Start.UTC().Format("2006-01-02"), summary)
	} else {
		head = fmt.Sprintf("%s - %s  %s",
			ev.Start.In(loc).Format("2006-01-02 15:04"), ev.End.In(loc).Format("15:04"), summary)
	}
	if ev.IsRecurring() {
		head += "  (recurring)"
	}
	lines := []string{head}
	if ev.Location != "" {
		lines = append(lines, "  location: "+ev.Location)
	}
	if ev.Description != "" {
		lines = append(lines, "  description: "+ev.Description)
	}
	lines = append(lines, detailLines(ev, "  ")...)
	if ev.Color != "" {
		lines = append(lines, "  color: "+ev.Color)
	}
	lines = append(lines, "  ID: "+ev.EventID)
	return lines
}
