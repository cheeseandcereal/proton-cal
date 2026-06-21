package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
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

// personString renders an email/CN pair as "CN <email>" or just the email.
func personString(email, cn string) string {
	if cn != "" && cn != email {
		return fmt.Sprintf("%s <%s>", cn, email)
	}
	return email
}

// eventDetailRows builds the labeled body rows of an event (location,
// description, organizer, attendees, conference, reminders, color), in a
// consistent order, for whichever fields sel selects. It is shared by the
// events-list sub-lines and the get-event detail view so labels and ordering
// stay identical. It does NOT include the head fields (summary/start/end) or
// the --all-only technical fields (rrule/uid/calendar_id/id).
func eventDetailRows(ev *event.Event, sel fieldSet) []labeled {
	var rows []labeled
	if sel.has(fieldLocation) && ev.Location != "" {
		rows = append(rows, labeled{"Location", ev.Location})
	}
	if sel.has(fieldDescription) && ev.Description != "" {
		rows = append(rows, labeled{"Description", ev.Description})
	}
	if sel.has(fieldOrganizer) && ev.Organizer != nil {
		rows = append(rows, labeled{"Organizer", personString(ev.Organizer.Email, ev.Organizer.CN)})
	}
	if sel.has(fieldAttendees) {
		for _, a := range ev.Attendees {
			who := personString(a.Email, a.CN)
			if st := attendeeStatusName(a.Status); st != "" {
				who += " (" + st + ")"
			}
			rows = append(rows, labeled{"Attendee", who})
		}
	}
	if sel.has(fieldConference) {
		if c := ev.Conference; c != nil && c.URL != "" {
			rows = append(rows, labeled{"Conference (" + conferenceProviderName(c.Provider) + ")", c.URL})
		}
	}
	if sel.has(fieldReminders) {
		for _, n := range ev.Notifications {
			kind := "notify"
			if n.Type == 0 {
				kind = "email"
			}
			rows = append(rows, labeled{"Reminder (" + kind + ")", n.Trigger})
		}
	}
	if sel.has(fieldColor) && ev.Color != "" {
		rows = append(rows, labeled{"Color", swatch(ev.Color) + ev.Color})
	}
	return rows
}

// occurrenceLines renders one listed occurrence as human-readable output
// lines. The head line carries only the date/time (with timezone) and the
// summary; everything else (location, description, organizer, attendees,
// conference, reminders, color) goes on its own labeled, aligned sub-line.
// Times are rendered in loc (all-day dates in UTC, their canonical anchor
// zone). sel selects which sub-line fields appear.
func occurrenceLines(l event.Listed, loc *time.Location, sel fieldSet) []string {
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
			start.In(loc).Format("2006-01-02 15:04 MST"), end.In(loc).Format("15:04 MST"), summary)
	}

	rows := eventDetailRows(ev, sel)
	if recurring {
		rows = append(rows, labeled{"Occurrence start", calsvc.FormatOccurrenceStart(l.Occurrence.Start, ev.AllDay, loc)})
	}
	rows = append(rows, labeled{"ID", ev.EventID})

	lines := []string{head}
	for _, line := range alignLabeled(rows) {
		lines = append(lines, "    "+line)
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

// eventDetailLines renders a single fetched event as aligned "Label: value"
// lines. sel selects which fields appear (the default curated set, an
// explicit --fields subset, or everything with --all).
func eventDetailLines(ev *event.Event, loc *time.Location, sel fieldSet) []string {
	renderLoc := loc
	if ev.AllDay {
		renderLoc = time.UTC
	}

	var rows []labeled
	add := func(key fieldKey, label, value string) {
		if sel.has(key) && value != "" {
			rows = append(rows, labeled{label, value})
		}
	}

	summary := ev.Summary
	if summary == "" {
		summary = "(no title)"
	}
	add(fieldSummary, "Summary", summary)
	if ev.AllDay {
		add(fieldStart, "Date", ev.Start.UTC().Format("2006-01-02")+" (all day)")
		add(fieldEnd, "End", ev.End.UTC().Format("2006-01-02"))
	} else {
		add(fieldStart, "Start", ev.Start.In(renderLoc).Format("2006-01-02 15:04 MST"))
		add(fieldEnd, "End", ev.End.In(renderLoc).Format("2006-01-02 15:04 MST"))
	}
	if sel.has(fieldRecurring) && ev.IsRecurring() {
		rows = append(rows, labeled{"Recurring", "yes"})
	}

	// Body fields (location/description/organizer/attendees/conference/
	// reminders/color), shared with the events-list sub-lines.
	rows = append(rows, eventDetailRows(ev, sel)...)

	// --all-only fields.
	add(fieldRRule, "RRULE", ev.RRule)
	add(fieldUID, "UID", ev.UID)
	add(fieldCalendarID, "Calendar ID", ev.CalendarID)
	add(fieldID, "ID", ev.EventID)

	return alignLabeled(rows)
}

// labeled is one "Label: value" detail row before alignment.
type labeled struct {
	label string
	value string
}

// alignColumnCap caps the alignment column so one outlier label (e.g. a long
// "Conference (Provider)") does not push every value far to the right.
// Labels longer than the cap simply get a single space before their value.
const alignColumnCap = 14

// alignLabeled renders labeled rows as "Label: value" with the colons
// aligned to the widest label (up to alignColumnCap). Swatch escape
// sequences in values are not measured for width (they are zero visible
// columns at the value position).
func alignLabeled(rows []labeled) []string {
	width := 0
	for _, r := range rows {
		if n := len(r.label); n > width && n <= alignColumnCap {
			width = n
		}
	}
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, fmt.Sprintf("%-*s %s", width+1, r.label+":", r.value))
	}
	return lines
}

// fieldKey identifies a selectable display field. The string value is the
// token a user passes to --fields, and matches the JSON key for that field.
type fieldKey string

// Event field keys. The string values match the eventJSON tags so --fields
// shares one vocabulary with the JSON output.
const (
	fieldSummary     fieldKey = "summary"
	fieldStart       fieldKey = "start"
	fieldEnd         fieldKey = "end"
	fieldRecurring   fieldKey = "recurring"
	fieldLocation    fieldKey = "location"
	fieldDescription fieldKey = "description"
	fieldOrganizer   fieldKey = "organizer"
	fieldAttendees   fieldKey = "attendees"
	fieldConference  fieldKey = "conference"
	fieldReminders   fieldKey = "notifications"
	fieldColor       fieldKey = "color"
	fieldRRule       fieldKey = "rrule"
	fieldUID         fieldKey = "uid"
	fieldCalendarID  fieldKey = "calendar_id"
	fieldID          fieldKey = "id"
)

// Calendar field keys (match calendarJSON tags).
const (
	calFieldName        fieldKey = "name"
	calFieldType        fieldKey = "type"
	calFieldColor       fieldKey = "color"
	calFieldDescription fieldKey = "description"
	calFieldID          fieldKey = "id"
	calFieldIsDefault   fieldKey = "is_default"
	calFieldEmail       fieldKey = "email"
	calFieldMemberID    fieldKey = "member_id"
	calFieldAddressID   fieldKey = "address_id"
)

// fieldRow describes one field in a resource's registry.
type fieldRow struct {
	key       fieldKey
	inDefault bool // shown in the curated default view (without --all)
}

// eventFieldRegistry is the ordered set of selectable event fields. Every
// field is in the default view except the technical ones (rrule/uid/
// calendar_id), which require --all.
var eventFieldRegistry = []fieldRow{
	{fieldSummary, true},
	{fieldStart, true},
	{fieldEnd, true},
	{fieldRecurring, true},
	{fieldLocation, true},
	{fieldDescription, true},
	{fieldOrganizer, true},
	{fieldAttendees, true},
	{fieldConference, true},
	{fieldReminders, true},
	{fieldColor, true},
	{fieldRRule, false},
	{fieldUID, false},
	{fieldCalendarID, false},
	{fieldID, true},
}

// listDefaultFields is the curated sub-line set shown under each event in the
// events list when no --fields/--all is given: location, description,
// conference and color. The head line already carries date/time + summary,
// and the ID is always appended, so neither needs to be selected here.
func listDefaultFields() fieldSet {
	return fieldSet{
		fieldLocation:    true,
		fieldDescription: true,
		fieldConference:  true,
		fieldColor:       true,
	}
}

// calendarFieldRegistry is the ordered set of selectable calendar fields.
// The member/address identifiers and account email require --all.
var calendarFieldRegistry = []fieldRow{
	{calFieldName, true},
	{calFieldType, true},
	{calFieldColor, true},
	{calFieldDescription, true},
	{calFieldID, true},
	{calFieldIsDefault, true},
	{calFieldEmail, false},
	{calFieldMemberID, false},
	{calFieldAddressID, false},
}

// fieldSet is the resolved set of fields to render.
type fieldSet map[fieldKey]bool

func (s fieldSet) has(k fieldKey) bool { return s[k] }

// selectFields resolves a --fields/--all request against a registry. With no
// requested fields, it returns the default (or, with all, every) field. An
// explicit request is honored verbatim (and --all is ignored). Unknown field
// names produce an error listing the valid names.
func selectFields(registry []fieldRow, requested []string, all bool) (fieldSet, error) {
	valid := make(map[fieldKey]bool, len(registry))
	for _, f := range registry {
		valid[f.key] = true
	}

	if len(requested) > 0 {
		sel := fieldSet{}
		for _, raw := range requested {
			for _, name := range strings.Split(raw, ",") {
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				if !valid[fieldKey(name)] {
					return nil, fmt.Errorf("unknown field %q; valid: %s", name, strings.Join(fieldNames(registry), ", "))
				}
				sel[fieldKey(name)] = true
			}
		}
		return sel, nil
	}

	sel := fieldSet{}
	for _, f := range registry {
		if all || f.inDefault {
			sel[f.key] = true
		}
	}
	return sel, nil
}

// fieldNames lists the registry's field keys in order, for error messages.
func fieldNames(registry []fieldRow) []string {
	names := make([]string, 0, len(registry))
	for _, f := range registry {
		names = append(names, string(f.key))
	}
	return names
}

// calendarDetailLines renders a single calendar as aligned "Label: value"
// lines, with a color swatch on the Color row. sel selects which fields
// appear.
func calendarDetailLines(c calendar.Info, isDefault bool, sel fieldSet) []string {
	var rows []labeled
	add := func(key fieldKey, label, value string) {
		if sel.has(key) && value != "" {
			rows = append(rows, labeled{label, value})
		}
	}
	add(calFieldName, "Name", c.Name)
	add(calFieldType, "Type", c.TypeString())
	if sel.has(calFieldColor) && c.Color != "" {
		rows = append(rows, labeled{"Color", swatch(c.Color) + c.Color})
	}
	add(calFieldDescription, "Description", c.Description)
	add(calFieldID, "ID", c.ID)
	if sel.has(calFieldIsDefault) && isDefault {
		rows = append(rows, labeled{"Default", "yes"})
	}
	add(calFieldEmail, "Email", c.Email)
	add(calFieldMemberID, "Member ID", c.MemberID)
	add(calFieldAddressID, "Address ID", c.AddressID)
	return alignLabeled(rows)
}

// sortedFieldSet returns a stable, ordered slice for testing/debugging.
func sortedFieldSet(s fieldSet) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, string(k))
	}
	sort.Strings(out)
	return out
}
