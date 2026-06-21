// Package ical builds and parses the four iCalendar VEVENT fragments
// that Proton Calendar stores per event (shared-signed, shared-encrypted,
// calendar-signed, calendar-encrypted). Fragments are built client-side,
// signed/encrypted with PGP and round-tripped verbatim, so property order
// and line endings are byte-stable.
//
// Two safety/compatibility properties of the serializer:
//
//   - TEXT values are escaped per RFC 5545 §3.3.11, preventing
//     property-injection through user-supplied text.
//   - Content lines are folded at 75 octets per RFC 5545 §3.1, matching
//     what the Proton web client itself produces.
//
// Parsing is hand-rolled rather than using an iCalendar library: Proton's
// fragments lack VERSION/PRODID and a trailing CRLF, which strict decoders
// reject whole-input, and the signed cards must be reproduced byte-for-byte
// (detached signatures cover the exact bytes). The tolerant parser here
// never panics and skips anything it cannot understand.
package ical

import (
	"fmt"
	"strings"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/icaltime"
)

// VEvent is the structured content of one VEVENT: the input to
// BuildFragments and the output of ParseFragment. Pointer fields
// distinguish "absent" from "present but zero" (update logic needs
// keep-current semantics; a genuine 1970-01-01 start is not "missing").
type VEvent struct {
	UID     string
	DTStamp time.Time

	Start, End *time.Time
	TZName     string // IANA zone for serializing times; ""/"UTC" → Z form
	AllDay     bool

	Summary     *string
	Description *string
	Location    *string

	Status  *string    // build: nil → STATUS:CONFIRMED
	Transp  *string    // build: nil → TRANSP:OPAQUE
	Created *time.Time // build: nil → CREATED:<DTStamp>
	Comment *string    // build: nil → empty COMMENT:

	Sequence *int // build: nil → SEQUENCE:0

	RRule        string      // verbatim RRULE value ("" = none)
	Exdates      []time.Time // deleted occurrence starts
	RecurrenceID *time.Time  // original occurrence start (exception rows)

	// Read-only enrichment fields (populated by ParseFragment, ignored by
	// BuildFragments). The write path never emits these.
	Organizer *Organizer // ORGANIZER
	Attendees []Attendee // ATTENDEE (repeatable; appended in parse order)

	// Proton Meet/Zoom conferencing, split across the shared cards:
	// the ID/provider are in the signed card, the URL/host in the encrypted
	// card. Provider follows VIDEO_CONFERENCE_PROVIDER (1 = Zoom, 2 = Meet).
	ConferenceID       string // X-PM-CONFERENCE-ID value
	ConferenceProvider string // X-PM-CONFERENCE-ID's X-PM-PROVIDER param
	ConferenceURL      string // X-PM-CONFERENCE-URL value
	ConferenceHost     string // X-PM-CONFERENCE-URL's X-PM-HOST param
}

// Organizer is a parsed ORGANIZER property: the calendar address and its
// optional common name.
type Organizer struct {
	Email string // CAL-ADDRESS with any "mailto:" scheme stripped
	CN    string // CN parameter
}

// Attendee is a parsed ATTENDEE property. Email is the CAL-ADDRESS with any
// "mailto:" stripped; the parameters carry display name, role, RSVP and
// participation status, plus Proton's anonymized X-PM-TOKEN (which joins to
// the plaintext row attendee for live RSVP status).
type Attendee struct {
	Email    string // CAL-ADDRESS, "mailto:" stripped
	CN       string // CN parameter (display name)
	Role     string // ROLE parameter (REQ-PARTICIPANT, ...)
	PartStat string // PARTSTAT parameter (NEEDS-ACTION, ACCEPTED, ...)
	RSVP     string // RSVP parameter (TRUE/FALSE)
	Token    string // X-PM-TOKEN parameter
}

// Fragments are the four VCALENDAR-wrapped VEVENT fragments.
type Fragments struct {
	SharedSigned      string
	SharedEncrypted   string
	CalendarSigned    string
	CalendarEncrypted string
}

// dtProp formats a date(-time) property line (DTSTART/DTEND/EXDATE/
// RECURRENCE-ID), no trailing CRLF, matching the Proton web client's
// three forms:
//
//	all-day:     NAME;VALUE=DATE:20260709            (t's own wall date; no zone conversion)
//	UTC timed:   NAME:20260709T160000Z               (tzName "" or "UTC")
//	zoned timed: NAME;TZID=America/Los_Angeles:20260709T090000  (t converted into tzName)
//
// All-day uses t's own calendar date because converting zones could
// shift the intended date across midnight.
func dtProp(name string, t time.Time, tzName string, allDay bool) (string, error) {
	if allDay {
		return fmt.Sprintf("%s;VALUE=DATE:%s", name, t.Format("20060102")), nil
	}
	if tzName == "" || tzName == "UTC" {
		return fmt.Sprintf("%s:%s", name, icaltime.FormatUTC(t)), nil
	}
	loc, err := icaltime.LoadLocation(tzName)
	if err != nil {
		return "", fmt.Errorf("ical: %w", err)
	}
	return fmt.Sprintf("%s;TZID=%s:%s", name, tzName, t.In(loc).Format("20060102T150405")), nil
}

// BuildFragments builds the four iCalendar fragments for an event
// with a fixed property order (property order matters;
// fragments are signed byte-for-byte):
//
//	shared signed:      UID, DTSTAMP, DTSTART, DTEND, [RECURRENCE-ID], [RRULE], EXDATEs, SEQUENCE
//	shared encrypted:   UID, DTSTAMP, CREATED, [SUMMARY], [DESCRIPTION], [LOCATION]
//	calendar signed:    UID, DTSTAMP, EXDATEs, STATUS, TRANSP
//	calendar encrypted: UID, DTSTAMP, COMMENT
//
// TEXT values are escaped with escapeText and every content line is
// folded with foldLine. The wrapper carries no VERSION/PRODID and no
// trailing CRLF — Proton's parts don't.
func BuildFragments(v VEvent) (Fragments, error) {
	if v.Start == nil || v.End == nil {
		return Fragments{}, fmt.Errorf("ical: event must have start and end times")
	}

	sharedSigned, err := buildSharedSigned(v)
	if err != nil {
		return Fragments{}, err
	}
	calendarSigned, err := buildCalendarSigned(v)
	if err != nil {
		return Fragments{}, err
	}
	return Fragments{
		SharedSigned:      wrap(sharedSigned),
		SharedEncrypted:   wrap(buildSharedEncrypted(v)),
		CalendarSigned:    wrap(calendarSigned),
		CalendarEncrypted: wrap(buildCalendarEncrypted(v)),
	}, nil
}

// exdateLines renders one EXDATE line per deleted occurrence.
func exdateLines(v VEvent) ([]string, error) {
	lines := make([]string, 0, len(v.Exdates))
	for _, d := range v.Exdates {
		line, err := dtProp("EXDATE", d, v.TZName, v.AllDay)
		if err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// buildSharedSigned renders the plaintext-signed shared card: times and
// recurrence structure.
func buildSharedSigned(v VEvent) ([]string, error) {
	dtstart, err := dtProp("DTSTART", *v.Start, v.TZName, v.AllDay)
	if err != nil {
		return nil, err
	}
	dtend, err := dtProp("DTEND", *v.End, v.TZName, v.AllDay)
	if err != nil {
		return nil, err
	}

	lines := []string{"UID:" + v.UID, "DTSTAMP:" + icaltime.FormatUTC(v.DTStamp), dtstart, dtend}
	if v.RecurrenceID != nil {
		recID, err := dtProp("RECURRENCE-ID", *v.RecurrenceID, v.TZName, v.AllDay)
		if err != nil {
			return nil, err
		}
		lines = append(lines, recID)
	}
	if v.RRule != "" {
		lines = append(lines, "RRULE:"+v.RRule)
	}
	exdates, err := exdateLines(v)
	if err != nil {
		return nil, err
	}
	lines = append(lines, exdates...)
	sequence := 0
	if v.Sequence != nil {
		sequence = *v.Sequence
	}
	return append(lines, fmt.Sprintf("SEQUENCE:%d", sequence)), nil
}

// buildSharedEncrypted renders the encrypted shared card: creation time and
// the user-visible text fields.
func buildSharedEncrypted(v VEvent) []string {
	dtstamp := icaltime.FormatUTC(v.DTStamp)
	created := dtstamp
	if v.Created != nil {
		created = icaltime.FormatUTC(*v.Created)
	}
	lines := []string{"UID:" + v.UID, "DTSTAMP:" + dtstamp, "CREATED:" + created}
	if v.Summary != nil && *v.Summary != "" {
		lines = append(lines, "SUMMARY:"+escapeText(*v.Summary))
	}
	if v.Description != nil && *v.Description != "" {
		lines = append(lines, "DESCRIPTION:"+escapeText(*v.Description))
	}
	if v.Location != nil && *v.Location != "" {
		lines = append(lines, "LOCATION:"+escapeText(*v.Location))
	}
	return lines
}

// buildCalendarSigned renders the plaintext-signed calendar card: EXDATEs
// (mirrored), STATUS and TRANSP.
func buildCalendarSigned(v VEvent) ([]string, error) {
	lines := []string{"UID:" + v.UID, "DTSTAMP:" + icaltime.FormatUTC(v.DTStamp)}
	exdates, err := exdateLines(v)
	if err != nil {
		return nil, err
	}
	lines = append(lines, exdates...)
	status := "CONFIRMED"
	if v.Status != nil && *v.Status != "" {
		status = *v.Status
	}
	transp := "OPAQUE"
	if v.Transp != nil && *v.Transp != "" {
		transp = *v.Transp
	}
	return append(lines, "STATUS:"+escapeText(status), "TRANSP:"+escapeText(transp)), nil
}

// buildCalendarEncrypted renders the encrypted calendar card: the comment.
func buildCalendarEncrypted(v VEvent) []string {
	comment := ""
	if v.Comment != nil {
		comment = *v.Comment
	}
	return []string{"UID:" + v.UID, "DTSTAMP:" + icaltime.FormatUTC(v.DTStamp), "COMMENT:" + escapeText(comment)}
}

// wrap folds each content line and joins everything into a VCALENDAR/
// VEVENT wrapper with CRLF separators and no trailing CRLF.
func wrap(lines []string) string {
	out := make([]string, 0, len(lines)+4)
	out = append(out, "BEGIN:VCALENDAR", "BEGIN:VEVENT")
	for _, l := range lines {
		out = append(out, foldLine(l))
	}
	out = append(out, "END:VEVENT", "END:VCALENDAR")
	return strings.Join(out, "\r\n")
}
