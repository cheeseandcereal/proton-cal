// Package caljson holds the machine-readable JSON shapes for calendars and
// events, shared by the CLI's -o json output and the MCP server's structured
// tool results so both surfaces emit one schema. Effective color and
// reminders (resolved from the calendar defaults) are baked in via eventview.
package caljson

import (
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/eventview"
)

// Event is the JSON shape of one event (an expanded occurrence or a single
// fetched event).
type Event struct {
	ID               string `json:"id"`
	UID              string `json:"uid,omitempty"`
	Summary          string `json:"summary,omitempty"`
	Description      string `json:"description,omitempty"`
	Location         string `json:"location,omitempty"`
	Start            string `json:"start,omitempty"`
	End              string `json:"end,omitempty"`
	AllDay           bool   `json:"all_day"`
	Recurring        bool   `json:"recurring"`
	EditedOccurrence bool   `json:"edited_occurrence"`
	// OccurrenceStartTS is the unix start of ONE expanded occurrence - the
	// value to pass back as the occurrence selector to update/delete a single
	// occurrence. It is only meaningful for the expanded listing (`events`);
	// the single-event view (`get event`) renders the stored row itself, not
	// an occurrence, so the field is omitted there (omitempty).
	OccurrenceStartTS int64          `json:"occurrence_start_ts,omitempty"`
	RRule             string         `json:"rrule,omitempty"`
	CalendarID        string         `json:"calendar_id,omitempty"`
	Color             string         `json:"color,omitempty"`
	IsOrganizer       bool           `json:"is_organizer,omitempty"`
	Organizer         *Person        `json:"organizer,omitempty"`
	Attendees         []Attendee     `json:"attendees,omitempty"`
	MoreAttendees     bool           `json:"more_attendees,omitempty"`
	Conference        *Conference    `json:"conference,omitempty"`
	Notifications     []Notification `json:"notifications,omitempty"`
}

// Person is the JSON shape of an organizer identity.
type Person struct {
	Email string `json:"email,omitempty"`
	CN    string `json:"cn,omitempty"`
}

// Attendee is the JSON shape of one attendee.
type Attendee struct {
	Email    string `json:"email,omitempty"`
	CN       string `json:"cn,omitempty"`
	Role     string `json:"role,omitempty"`
	PartStat string `json:"partstat,omitempty"`
	RSVP     string `json:"rsvp,omitempty"`
	// Status is the live API RSVP: "needs-action", "tentative", "declined",
	// "accepted", or "" when unknown.
	Status string `json:"status,omitempty"`
}

// Conference is the JSON shape of an event's video conference.
type Conference struct {
	Provider string `json:"provider,omitempty"`
	ID       string `json:"id,omitempty"`
	URL      string `json:"url,omitempty"`
	Password string `json:"password,omitempty"`
	Host     string `json:"host,omitempty"`
}

// Notification is the JSON shape of one reminder.
type Notification struct {
	Type    int    `json:"type"`
	Trigger string `json:"trigger"`
}

// Calendar is the JSON shape of one calendar (list entry or detail).
type Calendar struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Color       string `json:"color"`
	Type        int    `json:"type"`
	IsDefault   bool   `json:"is_default"`
	Email       string `json:"email,omitempty"`
	MemberID    string `json:"member_id,omitempty"`
	AddressID   string `json:"address_id,omitempty"`
}

// Created is the JSON shape of a create-event outcome. ID/UID are empty when
// the server did not echo the created row.
type Created struct {
	ID            string         `json:"id,omitempty"`
	UID           string         `json:"uid,omitempty"`
	Summary       string         `json:"summary"`
	StartTS       int64          `json:"start_ts"`
	EndTS         int64          `json:"end_ts"`
	AllDay        bool           `json:"all_day"`
	RRule         string         `json:"rrule,omitempty"`
	Color         string         `json:"color,omitempty"`
	Notifications []Notification `json:"notifications,omitempty"`
}

// Updated is the JSON shape of an update-event outcome.
type Updated struct {
	Updated           bool `json:"updated"`
	EditedOccurrence  bool `json:"edited_occurrence"`
	RemovedExceptions int  `json:"removed_exceptions"`
}

// renderZone is the zone an event's times are formatted in: loc for timed
// events, UTC for all-day ones (their canonical anchor zone).
func renderZone(ev *event.Event, loc *time.Location) *time.Location {
	if ev.AllDay {
		return time.UTC
	}
	return loc
}

// enrich fills the color/organizer/attendees/conference/reminders fields,
// resolving effective color and reminders from the calendar defaults.
func enrich(j *Event, ev *event.Event, set calendar.Settings, cal calendar.Info) {
	j.Color = eventview.EffectiveColor(ev, cal)
	j.IsOrganizer = ev.IsOrganizer
	j.MoreAttendees = ev.MoreAttendees
	if ev.Organizer != nil {
		j.Organizer = &Person{Email: ev.Organizer.Email, CN: ev.Organizer.CN}
	}
	for _, a := range ev.Attendees {
		j.Attendees = append(j.Attendees, Attendee{
			Email: a.Email, CN: a.CN, Role: a.Role, PartStat: a.PartStat,
			RSVP: a.RSVP, Status: eventview.AttendeeStatusName(a.Status),
		})
	}
	if ev.Conference != nil {
		j.Conference = &Conference{
			Provider: eventview.ConferenceProviderName(ev.Conference.Provider),
			ID:       ev.Conference.ID, URL: ev.Conference.URL,
			Password: ev.Conference.Password, Host: ev.Conference.Host,
		}
	}
	for _, n := range eventview.EffectiveReminders(ev, set) {
		j.Notifications = append(j.Notifications, Notification{Type: n.Type, Trigger: n.Trigger})
	}
}

// base fills the identity/content fields plus enrichment shared by the
// occurrence and single-event builders. Callers set the time/recurrence fields.
func base(ev *event.Event, set calendar.Settings, cal calendar.Info) Event {
	j := Event{
		ID:          ev.EventID,
		UID:         ev.UID,
		Summary:     ev.Summary,
		Description: ev.Description,
		Location:    ev.Location,
		AllDay:      ev.AllDay,
		RRule:       ev.RRule,
		CalendarID:  ev.CalendarID,
	}
	enrich(&j, ev, set, cal)
	return j
}

// Occurrence maps one listed occurrence to its JSON shape. Timed starts/ends
// are RFC3339 in loc; all-day ones in UTC.
func Occurrence(l event.Listed, loc *time.Location, set calendar.Settings, cal calendar.Info) Event {
	raw := l.Occurrence.Event
	ev := l.Event
	z := renderZone(ev, loc)
	j := base(ev, set, cal)
	j.Start = time.Unix(l.Occurrence.Start, 0).In(z).Format(time.RFC3339)
	j.End = time.Unix(l.Occurrence.End, 0).In(z).Format(time.RFC3339)
	j.Recurring = raw.IsMaster()
	j.EditedOccurrence = raw.IsException()
	j.OccurrenceStartTS = l.Occurrence.Start
	return j
}

// EventDetail maps a single fetched event (not an expanded occurrence) to its
// JSON shape. Times are the event's own start/end.
func EventDetail(ev *event.Event, loc *time.Location, set calendar.Settings, cal calendar.Info) Event {
	z := renderZone(ev, loc)
	j := base(ev, set, cal)
	j.Start = ev.Start.In(z).Format(time.RFC3339)
	j.End = ev.End.In(z).Format(time.RFC3339)
	j.Recurring = ev.IsRecurring()
	return j
}

// CalendarOf maps a calendar.Info to its JSON shape.
func CalendarOf(c calendar.Info, isDefault bool) Calendar {
	return Calendar{
		ID:          c.ID,
		Name:        c.Name,
		Description: c.Description,
		Color:       c.Color,
		Type:        c.Type,
		IsDefault:   isDefault,
		Email:       c.Email,
		MemberID:    c.MemberID,
		AddressID:   c.AddressID,
	}
}

// Calendars maps a calendar list to JSON, marking the default by selector.
func Calendars(cals []calendar.Info, defaultSel string) []Calendar {
	rows := make([]Calendar, 0, len(cals))
	for _, c := range cals {
		rows = append(rows, CalendarOf(c, c.Matches(defaultSel)))
	}
	return rows
}

// CreatedOf maps a create outcome to its JSON shape.
func CreatedOf(c *calsvc.CreatedEvent) Created {
	out := Created{
		ID:      c.ID,
		UID:     c.UID,
		Summary: c.Summary,
		StartTS: c.Start.Unix(),
		EndTS:   c.End.Unix(),
		AllDay:  c.AllDay,
		RRule:   c.RRule,
		Color:   c.Color,
	}
	for _, n := range c.Reminders {
		out.Notifications = append(out.Notifications, Notification{Type: n.Type, Trigger: n.Trigger})
	}
	return out
}

// UpdatedOf maps an update outcome to its JSON shape.
func UpdatedOf(o *event.UpdateOutcome) Updated {
	return Updated{
		Updated:           true,
		EditedOccurrence:  o.EditedOccurrence,
		RemovedExceptions: o.RemovedExceptions,
	}
}
