package ical

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// ParsedEvent holds fields extracted from one decrypted/plaintext
// fragment. Pointer fields distinguish "absent" from "empty" (update
// logic needs keep-current semantics).
type ParsedEvent struct {
	UID         string
	Summary     *string
	Description *string
	Location    *string
	Status      *string
	StartTS     int64 // 0 when absent; unix ts (all-day DATE values anchor at UTC midnight)
	EndTS       int64
	Sequence    int // 0 when absent
	HasSequence bool
}

// ErrNoContent is returned by ParseFragment when the input contains no
// parseable iCalendar content lines at all.
var ErrNoContent = errors.New("ical: no parseable content")

// ParseFragment parses an iCal fragment (full VCALENDAR or bare VEVENT,
// folded or unfolded lines, escaped TEXT values) tolerantly: unknown
// properties are ignored, malformed datetimes are skipped, and it never
// panics. Garbage input yields a zero ParsedEvent and an error.
//
// It extracts the event fields plus UID and SEQUENCE. All VEVENT
// components are scanned (later values win); properties of nested
// components (e.g. VALARM) and of non-VEVENT components (e.g. VTIMEZONE)
// are skipped.
// Input without any BEGIN:VEVENT is treated as a bare property list.
func ParseFragment(data string) (ParsedEvent, error) {
	var ev ParsedEvent

	lines := unfoldLines(data)
	if len(lines) == 0 {
		return ev, ErrNoContent
	}

	hasVEvent := false
	for _, l := range lines {
		if strings.EqualFold(strings.TrimSpace(l), "BEGIN:VEVENT") {
			hasVEvent = true
			break
		}
	}

	parsedAny := false
	inEvent := !hasVEvent // bare property list: treat everything as event content
	depth := 0            // nesting depth of non-VEVENT components

	for _, line := range lines {
		name, params, value, ok := splitContentLine(line)
		if !ok {
			continue
		}
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
		if !inEvent || depth > 0 {
			continue
		}

		switch name {
		case "UID":
			ev.UID = unescapeText(value)
			parsedAny = true
		case "SUMMARY":
			s := unescapeText(value)
			ev.Summary = &s
			parsedAny = true
		case "DESCRIPTION":
			s := unescapeText(value)
			ev.Description = &s
			parsedAny = true
		case "LOCATION":
			s := unescapeText(value)
			ev.Location = &s
			parsedAny = true
		case "STATUS":
			s := unescapeText(value)
			ev.Status = &s
			parsedAny = true
		case "DTSTART":
			if t, ok := parseDateTime(value, params); ok {
				ev.StartTS = t.Unix()
				parsedAny = true
			}
		case "DTEND":
			if t, ok := parseDateTime(value, params); ok {
				ev.EndTS = t.Unix()
				parsedAny = true
			}
		case "SEQUENCE":
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				ev.Sequence = n
				ev.HasSequence = true
				parsedAny = true
			}
		}
	}

	if !parsedAny && !hasVEvent {
		return ParsedEvent{}, ErrNoContent
	}
	return ev, nil
}

// SequenceFromFragment extracts SEQUENCE from a shared-signed fragment,
// returning 0 on absence or any parse failure.
func SequenceFromFragment(data string) int {
	ev, err := ParseFragment(data)
	if err != nil || !ev.HasSequence {
		return 0
	}
	return ev.Sequence
}

// splitContentLine splits a content line into upper-cased name, parameter
// map (keys upper-cased, surrounding quotes stripped) and raw value. The
// value separator is the first ':' outside double quotes. Returns
// ok=false for lines that are not NAME[;PARAMS]:VALUE shaped.
func splitContentLine(line string) (name string, params map[string]string, value string, ok bool) {
	sep := -1
	inQuotes := false
	for i := 0; i < len(line); i++ {
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
	for i := 0; i < len(s); i++ {
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

// parseDateTime parses the iCalendar date(-time) forms produced by
// Proton clients:
//
//	20060102T150405Z  — UTC
//	20060102T150405   — local in the TZID param's zone (UTC when absent)
//	20060102          — DATE (VALUE=DATE or naked), anchored at UTC midnight
//
// Returns ok=false for anything malformed (tolerant parsing).
func parseDateTime(value string, params map[string]string) (time.Time, bool) {
	v := strings.TrimSpace(value)
	// EXDATE-style multi-value lists: take the first value.
	if i := strings.IndexByte(v, ','); i >= 0 {
		v = v[:i]
	}
	switch {
	case len(v) == 16 && v[15] == 'Z':
		t, err := time.Parse("20060102T150405Z", v)
		if err != nil {
			return time.Time{}, false
		}
		return t, true
	case len(v) == 15:
		loc := time.UTC
		if tzid := params["TZID"]; tzid != "" && tzid != "UTC" {
			l, err := time.LoadLocation(tzid)
			if err != nil {
				return time.Time{}, false
			}
			loc = l
		}
		t, err := time.ParseInLocation("20060102T150405", v, loc)
		if err != nil {
			return time.Time{}, false
		}
		return t, true
	case len(v) == 8:
		t, err := time.Parse("20060102", v)
		if err != nil {
			return time.Time{}, false
		}
		return t, true
	}
	return time.Time{}, false
}
