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
// Parsing is hand-rolled rather than using github.com/emersion/go-ical:
// that module is not in this repo's go.mod/go.sum (which must not be
// modified), and its decoder is stricter than Proton's fragments, which
// lack VERSION/PRODID. The tolerant parser here never panics and skips
// anything it cannot understand.
package ical

import (
	"fmt"
	"strings"
	"time"
)

// EventFields is everything that goes into the four fragments.
type EventFields struct {
	UID          string
	DTStamp      time.Time
	Start, End   time.Time
	TZName       string // IANA zone for serializing times; ""/"UTC" → Z form
	AllDay       bool
	Summary      string
	Description  string
	Location     string
	Sequence     int
	RRule        string      // verbatim RRULE value ("" = none)
	Exdates      []time.Time // deleted occurrence starts
	RecurrenceID *time.Time  // original occurrence start (exception rows)
}

// Fragments are the four VCALENDAR-wrapped VEVENT fragments.
type Fragments struct {
	SharedSigned      string
	SharedEncrypted   string
	CalendarSigned    string
	CalendarEncrypted string
}

// FormatUTC renders a time as an iCalendar UTC datetime: 20060102T150405Z.
func FormatUTC(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// DTProp formats a date(-time) property line (DTSTART/DTEND/EXDATE/
// RECURRENCE-ID), no trailing CRLF, matching the Proton web client's
// three forms:
//
//	all-day:     NAME;VALUE=DATE:20260709            (t's own wall date; no zone conversion)
//	UTC timed:   NAME:20260709T160000Z               (tzName "" or "UTC")
//	zoned timed: NAME;TZID=America/Los_Angeles:20260709T090000  (t converted into tzName)
//
// All-day uses t's own calendar date because converting zones could
// shift the intended date across midnight.
func DTProp(name string, t time.Time, tzName string, allDay bool) (string, error) {
	if allDay {
		return fmt.Sprintf("%s;VALUE=DATE:%s", name, t.Format("20060102")), nil
	}
	if tzName == "" || tzName == "UTC" {
		return fmt.Sprintf("%s:%s", name, FormatUTC(t)), nil
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return "", fmt.Errorf("ical: invalid timezone %q: %w", tzName, err)
	}
	return fmt.Sprintf("%s;TZID=%s:%s", name, tzName, t.In(loc).Format("20060102T150405")), nil
}

// BuildFragments builds the four iCalendar fragments for an event
// with a fixed property order (property order matters;
// fragments are signed byte-for-byte):
//
//	shared signed:      UID, DTSTAMP, DTSTART, DTEND, [RECURRENCE-ID], [RRULE], EXDATEs, SEQUENCE
//	shared encrypted:   UID, DTSTAMP, CREATED, [SUMMARY], [DESCRIPTION], [LOCATION]
//	calendar signed:    UID, DTSTAMP, EXDATEs, STATUS:CONFIRMED, TRANSP:OPAQUE
//	calendar encrypted: UID, DTSTAMP, COMMENT:
//
// TEXT values are escaped with EscapeText and every content line is
// folded with FoldLine. The wrapper carries no VERSION/PRODID and no
// trailing CRLF — Proton's parts don't.
func BuildFragments(f EventFields) (Fragments, error) {
	dtstamp := FormatUTC(f.DTStamp)

	dtstart, err := DTProp("DTSTART", f.Start, f.TZName, f.AllDay)
	if err != nil {
		return Fragments{}, err
	}
	dtend, err := DTProp("DTEND", f.End, f.TZName, f.AllDay)
	if err != nil {
		return Fragments{}, err
	}
	exdateLines := make([]string, 0, len(f.Exdates))
	for _, d := range f.Exdates {
		line, err := DTProp("EXDATE", d, f.TZName, f.AllDay)
		if err != nil {
			return Fragments{}, err
		}
		exdateLines = append(exdateLines, line)
	}

	// Shared signed: uid, dtstamp, dtstart, dtend, recurrence-id, rrule, exdate, sequence.
	sharedSigned := []string{"UID:" + f.UID, "DTSTAMP:" + dtstamp, dtstart, dtend}
	if f.RecurrenceID != nil {
		recID, err := DTProp("RECURRENCE-ID", *f.RecurrenceID, f.TZName, f.AllDay)
		if err != nil {
			return Fragments{}, err
		}
		sharedSigned = append(sharedSigned, recID)
	}
	if f.RRule != "" {
		sharedSigned = append(sharedSigned, "RRULE:"+f.RRule)
	}
	sharedSigned = append(sharedSigned, exdateLines...)
	sharedSigned = append(sharedSigned, fmt.Sprintf("SEQUENCE:%d", f.Sequence))

	// Shared encrypted: uid, dtstamp, created, summary, description, location.
	sharedEncrypted := []string{"UID:" + f.UID, "DTSTAMP:" + dtstamp, "CREATED:" + dtstamp}
	if f.Summary != "" {
		sharedEncrypted = append(sharedEncrypted, "SUMMARY:"+EscapeText(f.Summary))
	}
	if f.Description != "" {
		sharedEncrypted = append(sharedEncrypted, "DESCRIPTION:"+EscapeText(f.Description))
	}
	if f.Location != "" {
		sharedEncrypted = append(sharedEncrypted, "LOCATION:"+EscapeText(f.Location))
	}

	// Calendar signed: uid, dtstamp, exdate, status, transp.
	calendarSigned := []string{"UID:" + f.UID, "DTSTAMP:" + dtstamp}
	calendarSigned = append(calendarSigned, exdateLines...)
	calendarSigned = append(calendarSigned, "STATUS:CONFIRMED", "TRANSP:OPAQUE")

	// Calendar encrypted: uid, dtstamp, empty comment.
	calendarEncrypted := []string{"UID:" + f.UID, "DTSTAMP:" + dtstamp, "COMMENT:"}

	return Fragments{
		SharedSigned:      wrap(sharedSigned),
		SharedEncrypted:   wrap(sharedEncrypted),
		CalendarSigned:    wrap(calendarSigned),
		CalendarEncrypted: wrap(calendarEncrypted),
	}, nil
}

// wrap folds each content line and joins everything into a VCALENDAR/
// VEVENT wrapper with CRLF separators and no trailing CRLF.
func wrap(lines []string) string {
	out := make([]string, 0, len(lines)+4)
	out = append(out, "BEGIN:VCALENDAR", "BEGIN:VEVENT")
	for _, l := range lines {
		out = append(out, FoldLine(l))
	}
	out = append(out, "END:VEVENT", "END:VCALENDAR")
	return strings.Join(out, "\r\n")
}
