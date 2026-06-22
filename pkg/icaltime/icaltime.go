// Package icaltime is the single codec for the iCalendar date/time forms
// produced by Proton clients, plus timezone-loading helpers shared by the
// ical, recurrence, event and frontend packages.
package icaltime

import (
	"fmt"
	"time"
)

// FormatUTC renders a time as an iCalendar UTC datetime: 20060102T150405Z.
func FormatUTC(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// Parse parses the three iCalendar forms Proton clients emit: "...T150405Z"
// (UTC), "...T150405" (wall time in loc), "20060102" (DATE, UTC midnight).
// Returns ok=false for anything malformed.
func Parse(v string, loc *time.Location) (time.Time, bool) {
	switch {
	case len(v) == 16 && v[15] == 'Z':
		t, err := time.Parse("20060102T150405Z", v)
		return t, err == nil
	case len(v) == 15:
		t, err := time.ParseInLocation("20060102T150405", v, loc)
		return t, err == nil
	case len(v) == 8:
		t, err := time.Parse("20060102", v)
		return t, err == nil
	}
	return time.Time{}, false
}

// OrUTC maps an empty zone name to "UTC" (Proton rows omit the zone for
// UTC-anchored times).
func OrUTC(name string) string {
	if name == "" {
		return "UTC"
	}
	return name
}

// LoadLocation loads an IANA zone, treating "" as UTC, with a consistent
// user-facing error message.
func LoadLocation(name string) (*time.Location, error) {
	name = OrUTC(name)
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("invalid timezone %q: %w", name, err)
	}
	return loc, nil
}
