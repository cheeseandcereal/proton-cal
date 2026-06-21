package ical

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/icaltime"
)

// errNoContent is returned by ParseFragment when the input contains no
// parseable iCalendar content lines at all.
var errNoContent = errors.New("ical: no parseable content")

// ParseFragment parses an iCal fragment (full VCALENDAR or bare VEVENT,
// folded or unfolded lines, escaped TEXT values) tolerantly: unknown
// properties are ignored, malformed datetimes are skipped, and it never
// panics. Garbage input yields a zero VEvent and an error.
//
// All VEVENT components are scanned (later values win); properties of
// nested components (e.g. VALARM) and of non-VEVENT components (e.g.
// VTIMEZONE) are skipped. Input without any BEGIN:VEVENT is treated as a
// bare property list.
func ParseFragment(data string) (VEvent, error) {
	var ev VEvent

	lines := unfoldLines(data)
	if len(lines) == 0 {
		return ev, errNoContent
	}

	parsedAny := false
	for _, line := range eventContentLines(lines) {
		name, params, value, ok := splitContentLine(line)
		if !ok {
			continue
		}
		if applyProperty(&ev, name, params, value) {
			parsedAny = true
		}
	}

	if !parsedAny && !hasVEventComponent(lines) {
		return VEvent{}, errNoContent
	}
	return ev, nil
}

// hasVEventComponent reports whether any line opens a VEVENT component.
func hasVEventComponent(lines []string) bool {
	for _, l := range lines {
		if strings.EqualFold(strings.TrimSpace(l), "BEGIN:VEVENT") {
			return true
		}
	}
	return false
}

// eventContentLines returns the content lines belonging directly to VEVENT
// components: component-boundary lines and the contents of nested (VALARM)
// or non-VEVENT (VTIMEZONE) components are dropped. Input without any
// BEGIN:VEVENT is treated as a bare property list, minus any structured
// non-VEVENT components it contains.
func eventContentLines(lines []string) []string {
	hasVEvent := hasVEventComponent(lines)

	inEvent := !hasVEvent // bare property list: treat everything as event content
	depth := 0            // nesting depth of non-VEVENT components

	out := make([]string, 0, len(lines))
	for _, line := range lines {
		name, _, value, ok := splitContentLine(line)
		if ok {
			switch name {
			case "BEGIN":
				if hasVEvent {
					if strings.EqualFold(value, "VEVENT") && !inEvent && depth == 0 {
						inEvent = true
					} else if inEvent {
						depth++ // nested component inside VEVENT (e.g. VALARM)
					}
				} else {
					depth++ // structured non-VEVENT content (e.g. VTIMEZONE)
				}
				continue
			case "END":
				if hasVEvent {
					if inEvent && depth > 0 {
						depth--
					} else if inEvent && strings.EqualFold(value, "VEVENT") {
						inEvent = false
					}
				} else if depth > 0 {
					depth--
				}
				continue
			}
		}
		if inEvent && depth == 0 {
			out = append(out, line)
		}
	}
	return out
}

// applyProperty applies one recognized content line to ev, reporting
// whether anything was extracted. Unknown properties and malformed values
// are skipped (tolerant parsing).
func applyProperty(ev *VEvent, name string, params map[string]string, value string) bool {
	setText := func(dst **string) bool {
		s := unescapeText(value)
		*dst = &s
		return true
	}
	setTime := func(dst **time.Time) bool {
		t, ok := parseDateTime(value, params)
		if !ok {
			return false
		}
		*dst = &t
		return true
	}

	switch name {
	case "UID":
		ev.UID = unescapeText(value)
		return true
	case "SUMMARY":
		return setText(&ev.Summary)
	case "DESCRIPTION":
		return setText(&ev.Description)
	case "LOCATION":
		return setText(&ev.Location)
	case "STATUS":
		return setText(&ev.Status)
	case "TRANSP":
		return setText(&ev.Transp)
	case "COMMENT":
		return setText(&ev.Comment)
	case "DTSTART":
		return setTime(&ev.Start)
	case "DTEND":
		return setTime(&ev.End)
	case "CREATED":
		return setTime(&ev.Created)
	case "SEQUENCE":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return false
		}
		ev.Sequence = &n
		return true
	case "ORGANIZER":
		ev.Organizer = &Organizer{
			Email: stripMailto(unescapeText(value)),
			CN:    params["CN"],
		}
		return true
	case "ATTENDEE":
		ev.Attendees = append(ev.Attendees, Attendee{
			Email:    stripMailto(unescapeText(value)),
			CN:       params["CN"],
			Role:     params["ROLE"],
			PartStat: params["PARTSTAT"],
			RSVP:     params["RSVP"],
			Token:    params["X-PM-TOKEN"],
		})
		return true
	case "X-PM-CONFERENCE-ID":
		ev.ConferenceID = unescapeText(value)
		ev.ConferenceProvider = params["X-PM-PROVIDER"]
		return true
	case "X-PM-CONFERENCE-URL":
		ev.ConferenceURL = unescapeText(value)
		ev.ConferenceHost = params["X-PM-HOST"]
		return true
	}
	return false
}

// stripMailto removes a leading "mailto:" scheme (case-insensitive) from a
// calendar address value.
func stripMailto(v string) string {
	if len(v) >= 7 && strings.EqualFold(v[:7], "mailto:") {
		return v[7:]
	}
	return v
}

// splitContentLine splits a content line into upper-cased name, parameter
// map (keys upper-cased, surrounding quotes stripped) and raw value. The
// value separator is the first ':' outside double quotes. Returns
// ok=false for lines that are not NAME[;PARAMS]:VALUE shaped.
func splitContentLine(line string) (name string, params map[string]string, value string, ok bool) {
	sep := -1
	inQuotes := false
	for i := range len(line) {
		switch line[i] {
		case '"':
			inQuotes = !inQuotes
		case ':':
			if !inQuotes {
				sep = i
			}
		}
		if sep >= 0 {
			break
		}
	}
	if sep <= 0 {
		return "", nil, "", false
	}
	value = line[sep+1:]

	head := line[:sep]
	parts := splitOutsideQuotes(head, ';')
	name = strings.ToUpper(strings.TrimSpace(parts[0]))
	if name == "" || strings.ContainsAny(name, " \t") {
		return "", nil, "", false
	}
	params = make(map[string]string, len(parts)-1)
	for _, p := range parts[1:] {
		k, v, found := strings.Cut(p, "=")
		if !found {
			continue
		}
		params[strings.ToUpper(strings.TrimSpace(k))] = strings.Trim(v, `"`)
	}
	return name, params, value, true
}

// splitOutsideQuotes splits s on sep, ignoring separators inside
// double-quoted sections.
func splitOutsideQuotes(s string, sep byte) []string {
	var parts []string
	start := 0
	inQuotes := false
	for i := range len(s) {
		switch s[i] {
		case '"':
			inQuotes = !inQuotes
		case sep:
			if !inQuotes {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}

// parseDateTime parses an iCalendar date(-time) property value via the
// shared icaltime codec, resolving local-form values in the TZID param's
// zone (UTC when absent). Returns ok=false for anything malformed
// (tolerant parsing).
func parseDateTime(value string, params map[string]string) (time.Time, bool) {
	v := strings.TrimSpace(value)
	loc, err := icaltime.LoadLocation(params["TZID"])
	if err != nil {
		return time.Time{}, false
	}
	return icaltime.Parse(v, loc)
}
