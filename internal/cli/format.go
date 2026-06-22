package cli

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/calcolor"
	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/eventview"
)

// eventDetailRows builds an event's labeled body rows for sel's fields, in a
// consistent order shared by the events-list and get-event views.
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
			rows = append(rows, labeled{"Color", swatch(c) + calcolor.Label(c)})
		}
	}
	return rows
}

// occurrenceHeaderRows builds the header and labeled sub-rows for one listed
// occurrence (times in loc, all-day dates in UTC) for the list renderer.
func occurrenceHeaderRows(l event.Listed, loc *time.Location, sel fieldSet, set calendar.Settings, cal calendar.Info) (header string, rows []labeled) {
	raw := l.Occurrence.Event
	ev := l.Event
	header = eventview.SummaryOr(ev) + eventview.RecurrenceSuffix(raw)

	start := time.Unix(l.Occurrence.Start, 0)
	end := time.Unix(l.Occurrence.End, 0)

	rows = []labeled{{"Time", eventTimeRange(start, end, ev.AllDay, loc)}}
	rows = append(rows, eventDetailRows(ev, sel, set, cal)...)
	if raw.IsMaster() {
		rows = append(rows, labeled{"Occurrence start", calsvc.FormatOccurrenceStart(l.Occurrence.Start, ev.AllDay, loc)})
	}
	rows = append(rows, labeled{"ID", ev.EventID})
	return header, rows
}

// occurrenceListLines renders a window of listed occurrences with one shared
// label-column width so every event's sub-field values align.
func occurrenceListLines(items []event.Listed, loc *time.Location, sel fieldSet, set calendar.Settings, cal calendar.Info) []string {
	headers := make([]string, len(items))
	rowsPer := make([][]labeled, len(items))
	width := 0
	for i, l := range items {
		headers[i], rowsPer[i] = occurrenceHeaderRows(l, loc, sel, set, cal)
		if w := labelWidth(rowsPer[i]); w > width {
			width = w
		}
	}

	var lines []string
	for i := range items {
		lines = append(lines, headers[i])
		for _, line := range alignLabeledWidth(rowsPer[i], width) {
			lines = append(lines, "  "+line)
		}
	}
	return lines
}

// eventTimeRange formats an event's start/end for display: timezone shown
// once at the end, end date only when it differs from the start. All-day uses
// UTC-anchored dates; the iCal end is exclusive, so last day is end-1day.
func eventTimeRange(start, end time.Time, allDay bool, loc *time.Location) string {
	if allDay {
		s := start.UTC()
		last := end.UTC().AddDate(0, 0, -1) // exclusive end -> inclusive last day
		if last.After(s) {
			return fmt.Sprintf("%s - %s (all day)", s.Format("2006-01-02"), last.Format("2006-01-02"))
		}
		return s.Format("2006-01-02") + " (all day)"
	}
	s := start.In(loc)
	e := end.In(loc)
	if s.Format("2006-01-02") == e.Format("2006-01-02") {
		// Same day: timezone once, at the end.
		return fmt.Sprintf("%s - %s %s", s.Format("2006-01-02 15:04"), e.Format("15:04"), e.Format("MST"))
	}
	// Spans days: the end carries its own date; timezone once, at the end.
	return fmt.Sprintf("%s - %s %s", s.Format("2006-01-02 15:04"), e.Format("2006-01-02 15:04"), e.Format("MST"))
}

// eventDetailLines renders a single event as aligned "Label: value" lines for
// the fields sel selects; set/cal resolve the effective reminders and color.
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
	if sel.has(fieldRecurring) && !ev.RecurrenceID.IsZero() {
		// Exception row: note which original occurrence of the series it edits.
		b.add("Edits occurrence", calsvc.FormatOccurrenceStart(ev.RecurrenceID.Unix(), ev.AllDay, loc))
	}

	// Body fields, shared with the events-list sub-lines.
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

// rowBuilder accumulates labeled rows gated by a fieldSet ("append iff
// selected and non-empty"), shared by the event and calendar renderers.
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

// alignColumnCap caps the alignment column so one outlier label doesn't push
// every value far right; labels over the cap get a single space before value.
const alignColumnCap = 14

// labelWidth returns the alignment column width for rows: the widest label
// not exceeding alignColumnCap.
func labelWidth(rows []labeled) int {
	width := 0
	for _, r := range rows {
		if n := len(r.label); n > width && n <= alignColumnCap {
			width = n
		}
	}
	return width
}

// alignLabeled renders labeled rows as "Label: value" with the colons aligned
// to the widest label (up to alignColumnCap).
func alignLabeled(rows []labeled) []string {
	return alignLabeledWidth(rows, labelWidth(rows))
}

// alignLabeledWidth renders labeled rows as "Label: value" aligning colons to
// the given width (so callers can share one across row groups). Swatch escape
// sequences in values are not measured for width.
func alignLabeledWidth(rows []labeled, width int) []string {
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		lines = append(lines, fmt.Sprintf("%-*s %s", width+1, r.label+":", r.value))
	}
	return lines
}

// fieldKey identifies a selectable display field. The string value is the
// token a user passes to --fields, and matches the JSON key for that field.
type fieldKey string

// Event field keys; string values match the eventJSON tags so --fields shares
// one vocabulary with JSON output.
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
	calFieldMakesBusy        fieldKey = "makes_busy"
	calFieldEmail            fieldKey = "email"
	calFieldMemberID         fieldKey = "member_id"
	calFieldAddressID        fieldKey = "address_id"
)

// fieldRow describes one field in a resource's registry.
type fieldRow struct {
	key       fieldKey
	inDefault bool // shown in the curated default view (without --all)
}

// eventFieldRegistry is the ordered set of selectable event fields. All are
// in the default view except rrule/uid/calendar_id, which require --all.
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

// listDefaultFields is the curated events-list sub-line set when no
// --fields/--all is given: location, description, conference (color omitted).
func listDefaultFields() fieldSet {
	return fieldSet{
		fieldLocation:    true,
		fieldDescription: true,
		fieldConference:  true,
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
	{calFieldMakesBusy, true},
	{calFieldID, true},
	{calFieldIsDefault, true},
	{calFieldEmail, false},
	{calFieldMemberID, false},
	{calFieldAddressID, false},
}

// fieldSet is the resolved set of fields to render.
type fieldSet map[fieldKey]bool

func (s fieldSet) has(k fieldKey) bool { return s[k] }

// selectFields resolves a --fields/--all request against a registry: no
// request yields the default (or every, with all) field; an explicit request
// is honored verbatim. Unknown names error, listing the valid names.
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
// lines for the fields sel selects, with a swatch on the Color row.
func calendarDetailLines(c calendar.Info, set calendar.Settings, isDefault bool, sel fieldSet) []string {
	b := rowBuilder{sel: sel}
	b.addIf(calFieldName, "Name", c.Name)
	b.addIf(calFieldType, "Type", c.TypeString())
	if sel.has(calFieldColor) && c.Color != "" {
		b.add("Color", swatch(c.Color)+calcolor.Label(c.Color))
	}
	b.addIf(calFieldDescription, "Description", c.Description)
	if sel.has(calFieldDefaultReminders) {
		b.addRows(defaultReminderRows("Default reminder (timed)", set.DefaultPartDayNotifications)...)
		b.addRows(defaultReminderRows("Default reminder (all-day)", set.DefaultFullDayNotifications)...)
	}
	if sel.has(calFieldDefaultDuration) && set.DefaultEventDuration > 0 {
		b.add("Default duration", fmt.Sprintf("%d min", set.DefaultEventDuration))
	}
	if sel.has(calFieldMakesBusy) {
		b.add("Makes busy", yesNo(set.MakesUserBusy != 0))
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

// yesNo renders a boolean as "yes"/"no" for labeled text rows.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
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
