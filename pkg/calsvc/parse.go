package calsvc

import (
	"errors"
	"fmt"
	"time"

	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/icaltime"
	"github.com/cheeseandcereal/proton-cal/pkg/recurrence"
)

// timeFormatHint describes the accepted input formats in user messages.
const timeFormatHint = "YYYY-MM-DD HH:MM (timed) or YYYY-MM-DD (all-day)"

// parseLocalDateTime parses "YYYY-MM-DD HH:MM" as a wall time in the given
// IANA zone.
func parseLocalDateTime(value, tzName string) (time.Time, error) {
	loc, err := icaltime.LoadLocation(tzName)
	if err != nil {
		return time.Time{}, err
	}
	t, err := time.ParseInLocation("2006-01-02 15:04", value, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid datetime %q (expected YYYY-MM-DD HH:MM): %w", value, err)
	}
	return t, nil
}

// parseDate parses "YYYY-MM-DD" as a UTC-midnight date (all-day events).
func parseDate(value string) (time.Time, error) {
	t, err := time.ParseInLocation("2006-01-02", value, time.UTC)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid date %q (expected YYYY-MM-DD): %w", value, err)
	}
	return t, nil
}

// parseWhen parses either form: timed "YYYY-MM-DD HH:MM" (in tzName) or
// all-day "YYYY-MM-DD" (UTC midnight).
func parseWhen(value, tzName string) (time.Time, error) {
	if t, err := parseLocalDateTime(value, tzName); err == nil {
		return t, nil
	}
	if t, err := parseDate(value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid date/time %q; expected %s", value, timeFormatHint)
}

// parseOccurrence parses an occurrence argument (the ORIGINAL start of one
// occurrence, as shown by listings) to a unix timestamp.
func parseOccurrence(value, tzName string) (int64, error) {
	t, err := parseWhen(value, tzName)
	if err != nil {
		return 0, err
	}
	return t.Unix(), nil
}

// FormatOccurrenceStart renders an occurrence's original start in the form the
// occurrence arguments accept back: the date (all-day, midnight UTC) or wall time in loc.
func FormatOccurrenceStart(ts int64, allDay bool, loc *time.Location) string {
	t := time.Unix(ts, 0)
	if allDay {
		return t.UTC().Format("2006-01-02")
	}
	return t.In(loc).Format("2006-01-02 15:04")
}

// resolveCreateTimes resolves create start/end times. All-day: dates, INCLUSIVE
// end (default = start) converted to exclusive iCal end by +24h (endProvided
// always true). Timed: wall times in tzName; empty endStr leaves end zero and
// endProvided false so the caller defaults it (see applyDefaultDuration).
func resolveCreateTimes(startStr, endStr string, allDay bool, tzName string) (start, end time.Time, endProvided bool, err error) {
	if allDay {
		start, err = parseDate(startStr)
		if err != nil {
			return time.Time{}, time.Time{}, false, err
		}
		end = start
		if endStr != "" {
			end, err = parseDate(endStr)
			if err != nil {
				return time.Time{}, time.Time{}, false, err
			}
		}
		end = end.Add(24 * time.Hour) // exclusive iCal end
		if !end.After(start) {
			return time.Time{}, time.Time{}, false, errors.New("end date must not be before start date")
		}
		return start, end, true, nil
	}

	start, err = parseLocalDateTime(startStr, tzName)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	if endStr == "" {
		return start, time.Time{}, false, nil
	}
	end, err = parseLocalDateTime(endStr, tzName)
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	return start, end, true, nil
}

// applyDefaultDuration returns the end for a timed event with omitted end, using
// the calendar's default duration; errors when no usable default exists.
func applyDefaultDuration(start time.Time, set calendar.Settings) (time.Time, error) {
	dur, ok := set.DefaultDuration()
	if !ok {
		return time.Time{}, errors.New("end is required for timed events")
	}
	return start.Add(dur), nil
}

// Recurrence holds the structured recurrence options shared by the create
// and update operations.
type Recurrence struct {
	Repeat   string // "", "daily", "weekly", "monthly", "yearly"
	Every    int    // interval; 0 or 1 = default
	Count    int    // 0 = unset
	Until    string // "" = unset; YYYY-MM-DD
	RawRRule string // raw RRULE value; replaces the structured options
}

// Empty reports whether no recurrence options were given.
func (r Recurrence) Empty() bool {
	return r.Repeat == "" && r.RawRRule == "" && r.Count == 0 && r.Until == "" && (r.Every == 0 || r.Every == 1)
}

// buildRRule combines the options into an RRULE value ("" = none): RawRRule is
// exclusive with (and sanitizes) the structured options; Every/Count/Until require Repeat.
func (r Recurrence) buildRRule(tzName string, allDay bool) (string, error) {
	if r.RawRRule != "" {
		if r.Repeat != "" || r.Count != 0 || r.Until != "" || (r.Every != 0 && r.Every != 1) {
			return "", errors.New("rrule cannot be combined with repeat/every/count/until")
		}
		return recurrence.SanitizeRRule(r.RawRRule)
	}
	if r.Repeat == "" {
		if r.Count != 0 || r.Until != "" || (r.Every != 0 && r.Every != 1) {
			return "", errors.New("every/count/until require repeat")
		}
		return "", nil
	}
	every := r.Every
	if every == 0 {
		every = 1
	}
	return recurrence.BuildRRule(r.Repeat, every, r.Count, r.Until, tzName, allDay)
}
