// Package front holds small helpers shared by the CLI and MCP frontends:
// user-facing datetime parsing and recurrence flag combination.
package front

import (
	"errors"
	"fmt"
	"time"

	"github.com/cheeseandcereal/proton-cal-go/internal/recurrence"
)

// TimeFormatHint describes the accepted input formats in user messages.
const TimeFormatHint = "YYYY-MM-DD HH:MM (timed) or YYYY-MM-DD (all-day)"

// ParseLocalDateTime parses "YYYY-MM-DD HH:MM" as a wall time in the given
// IANA zone.
func ParseLocalDateTime(value, tzName string) (time.Time, error) {
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timezone %q: %w", tzName, err)
	}
	t, err := time.ParseInLocation("2006-01-02 15:04", value, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid datetime %q (expected YYYY-MM-DD HH:MM): %w", value, err)
	}
	return t, nil
}

// ParseDate parses "YYYY-MM-DD" as a UTC-midnight date (all-day events).
func ParseDate(value string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", value, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q (expected YYYY-MM-DD): %w", value, err)
	}
	return t, nil
}

// ParseWhen parses either form: timed "YYYY-MM-DD HH:MM" (in tzName) or
// all-day "YYYY-MM-DD" (UTC midnight).
func ParseWhen(value, tzName string) (time.Time, error) {
	if t, err := ParseLocalDateTime(value, tzName); err == nil {
		return t, nil
	}
	if t, err := ParseDate(value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid date/time %q; expected %s", value, TimeFormatHint)
}

// ParseOccurrence parses an --occurrence argument (the ORIGINAL start of one
// occurrence, as shown by listings) to a unix timestamp.
func ParseOccurrence(value, tzName string) (int64, error) {
	t, err := ParseWhen(value, tzName)
	if err != nil {
		return 0, err
	}
	return t.Unix(), nil
}

// RecurrenceFlags are the structured recurrence options shared by create
// and update frontends.
type RecurrenceFlags struct {
	Repeat   string // "", "daily", "weekly", "monthly", "yearly"
	Every    int    // interval; 1 = default
	Count    int    // 0 = unset
	Until    string // "" = unset; YYYY-MM-DD
	RawRRule string // raw RRULE value; replaces the structured flags
}

// Empty reports whether no recurrence options were given.
func (f RecurrenceFlags) Empty() bool {
	return f.Repeat == "" && f.RawRRule == "" && f.Count == 0 && f.Until == "" && (f.Every == 0 || f.Every == 1)
}

// BuildRRule combines the flags into an RRULE value ("" = none),
// validating the flag combinations:
//   - RawRRule is exclusive with the structured flags and is sanitized.
//   - Every/Count/Until require Repeat.
func (f RecurrenceFlags) BuildRRule(tzName string, allDay bool) (string, error) {
	if f.RawRRule != "" {
		if f.Repeat != "" || f.Count != 0 || f.Until != "" || (f.Every != 0 && f.Every != 1) {
			return "", errors.New("--rrule cannot be combined with --repeat/--every/--count/--until")
		}
		return recurrence.SanitizeRRule(f.RawRRule)
	}
	if f.Repeat == "" {
		if f.Count != 0 || f.Until != "" || (f.Every != 0 && f.Every != 1) {
			return "", errors.New("--every/--count/--until require --repeat")
		}
		return "", nil
	}
	every := f.Every
	if every == 0 {
		every = 1
	}
	return recurrence.BuildRRule(f.Repeat, every, f.Count, f.Until, tzName, allDay)
}
