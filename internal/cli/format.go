package cli

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/eventview"
)

// eventDetailRows builds the labeled body rows of an event (location,
// description, organizer, attendees, conference, reminders, color), in a
// consistent order, for whichever fields sel selects. It is shared by the
// events-list sub-lines and the get-event detail view so labels and ordering
// stay identical. It does NOT include the head fields (summary/start/end) or
// the --all-only technical fields (rrule/uid/calendar_id/id).
func eventDetailRows(ev *event.Event, sel fieldSet, set calendar.Settings, cal calendar.Info) []labeled {
	var rows []labeled
	if sel.has(fieldLocation) && ev.Location != "" {
		rows = append(rows, labeled{"Location", ev.Location})
	}
	if sel.has(fieldDescription) && ev.Description != "" {
		rows = append(rows, labeled{"Description", ev.Description})
	}
	if sel.has(fieldOrganizer) && ev.Organizer != nil {
		rows = append(rows, labeled{"Organizer", eventview.PersonOf(ev.Organizer)})
	}
	if sel.has(fieldAttendees) {
		for _, a := range ev.Attendees {
			rows = append(rows, labeled{"Attendee", eventview.AttendeeString(a)})
		}
	}
	if sel.has(fieldConference) {
		if c := ev.Conference; c != nil && c.URL != "" {
			rows = append(rows, labeled{"Conference (" + eventview.ConferenceProviderName(c.Provider) + ")", c.URL})
		}
	}
	if sel.has(fieldReminders) {
		for _, n := range eventview.EffectiveReminders(ev, set) {
			rows = append(rows, labeled{"Reminder (" + eventview.ReminderKind(n.Type) + ")", n.Trigger})
		}
	}
	if sel.has(fieldColor) {
		if c := eventview.EffectiveColor(ev, cal); c != "" {
			rows = append(rows, labeled{"Color", swatch(c) + c})
		}
	}
	return rows
}

// occurrenceLines renders one listed occurrence as human-readable output
// lines. The head line carries only the date/time (with timezone) and the
// summary; everything else (location, description, organizer, attendees,
// conference, reminders, color) goes on its own labeled, aligned sub-line.
// Times are rendered in loc (all-day dates in UTC, their canonical anchor
// zone). sel selects which sub-line fields appear; set/cal resolve the
// effective reminders and color.
func occurrenceLines(l event.Listed, loc *time.Location, sel fieldSet, set calendar.Settings, cal calendar.Info) []string {
	raw := l.Occurrence.Event
	ev := l.Event
	recurring := raw.IsMaster()
	summary := eventview.SummaryOr(ev) + eventview.RecurrenceSuffix(raw)

	start := time.Unix(l.Occurrence.Start, 0)
	end := time.Unix(l.Occurrence.End, 0)
	var head string
	if ev.AllDay {
		head = fmt.Sprintf("  %s (all day)  %s", start.UTC().Format("2006-01-02"), summary)
	} else {
		head = fmt.Sprintf("  %s - %s  %s",
			start.In(loc).Format("2006-01-02 15:04 MST"), end.In(loc).Format("15:04 MST"), summary)
	}

	rows := eventDetailRows(ev, sel, set, cal)
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

// eventDetailLines renders a single fetched event as aligned "Label: value"
// lines. sel selects which fields appear (the default curated set, an
// explicit --fields subset, or everything with --all); set/cal resolve the
// effective reminders and color.
func eventDetailLines(ev *event.Event, loc *time.Location, sel fieldSet, set calendar.Settings, cal calendar.Info) []string {
	b := rowBuilder{sel: sel}
	b.addIf(fieldSummary, "Summary", eventview.SummaryOr(ev))
	if ev.AllDay {
		b.addIf(fieldStart, "Date", ev.Start.UTC().Format("2006-01-02")+" (all day)")
		b.addIf(fieldEnd, "End", ev.End.UTC().Format("2006-01-02"))
	} else {
		// Timed events: format in loc (all-day handled above).
		b.addIf(fieldStart, "Start", ev.Start.In(loc).Format("2006-01-02 15:04 MST"))
		b.addIf(fieldEnd, "End", ev.End.In(loc).Format("2006-01-02 15:04 MST"))
	}
	if sel.has(fieldRecurring) && ev.IsRecurring() {
		b.add("Recurring", "yes")
	}

	// Body fields (location/description/organizer/attendees/conference/
	// reminders/color), shared with the events-list sub-lines.
	b.addRows(eventDetailRows(ev, sel, set, cal)...)

	// --all-only fields.
	b.addIf(fieldRRule, "RRULE", ev.RRule)
	b.addIf(fieldUID, "UID", ev.UID)
	b.addIf(fieldCalendarID, "Calendar ID", ev.CalendarID)
	b.addIf(fieldID, "ID", ev.EventID)

	return alignLabeled(b.rows)
}

// labeled is one "Label: value" detail row before alignment.
type labeled struct {
	label string
	value string
}

// rowBuilder accumulates labeled rows gated by a fieldSet, factoring out the
// "append iff the field is selected and the value is non-empty" pattern shared
// by the event and calendar detail renderers.
type rowBuilder struct {
	sel  fieldSet
	rows []labeled
}

// addIf appends a row when key is selected and value is non-empty.
func (b *rowBuilder) addIf(key fieldKey, label, value string) {
	if b.sel.has(key) && value != "" {
		b.rows = append(b.rows, labeled{label, value})
	}
}

// add appends a row unconditionally (callers that already checked selection).
func (b *rowBuilder) add(label, value string) {
	b.rows = append(b.rows, labeled{label, value})
}

// addRows appends pre-built rows.
func (b *rowBuilder) addRows(rows ...labeled) {
	b.rows = append(b.rows, rows...)
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
	calFieldName             fieldKey = "name"
	calFieldType             fieldKey = "type"
	calFieldColor            fieldKey = "color"
	calFieldDescription      fieldKey = "description"
	calFieldID               fieldKey = "id"
	calFieldIsDefault        fieldKey = "is_default"
	calFieldDefaultReminders fieldKey = "default_reminders"
	calFieldDefaultDuration  fieldKey = "default_duration"
	calFieldEmail            fieldKey = "email"
	calFieldMemberID         fieldKey = "member_id"
	calFieldAddressID        fieldKey = "address_id"
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
	{calFieldDefaultReminders, true},
	{calFieldDefaultDuration, true},
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
// lines, with a color swatch on the Color row and the default reminders
// resolved from the calendar settings. sel selects which fields appear.
func calendarDetailLines(c calendar.Info, set calendar.Settings, isDefault bool, sel fieldSet) []string {
	b := rowBuilder{sel: sel}
	b.addIf(calFieldName, "Name", c.Name)
	b.addIf(calFieldType, "Type", c.TypeString())
	if sel.has(calFieldColor) && c.Color != "" {
		b.add("Color", swatch(c.Color)+c.Color)
	}
	b.addIf(calFieldDescription, "Description", c.Description)
	if sel.has(calFieldDefaultReminders) {
		b.addRows(defaultReminderRows("Default reminder (timed)", set.DefaultPartDayNotifications)...)
		b.addRows(defaultReminderRows("Default reminder (all-day)", set.DefaultFullDayNotifications)...)
	}
	if sel.has(calFieldDefaultDuration) && set.DefaultEventDuration > 0 {
		b.add("Default duration", fmt.Sprintf("%d min", set.DefaultEventDuration))
	}
	b.addIf(calFieldID, "ID", c.ID)
	if sel.has(calFieldIsDefault) && isDefault {
		b.add("Default", "yes")
	}
	b.addIf(calFieldEmail, "Email", c.Email)
	b.addIf(calFieldMemberID, "Member ID", c.MemberID)
	b.addIf(calFieldAddressID, "Address ID", c.AddressID)
	return alignLabeled(b.rows)
}

// defaultReminderRows renders one labeled row per default notification.
func defaultReminderRows(label string, ns []caltypes.Notification) []labeled {
	rows := make([]labeled, 0, len(ns))
	for _, n := range ns {
		rows = append(rows, labeled{label + " (" + eventview.ReminderKind(n.Type) + ")", n.Trigger})
	}
	return rows
}

// sortedFieldSet returns a stable, ordered slice for testing/debugging.
func sortedFieldSet(s fieldSet) []string {
	out := make([]string, 0, len(s))
	for k := range s {
		out = append(out, string(k))
	}
	slices.Sort(out)
	return out
}
