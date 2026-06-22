// Package recurrence provides client-side recurrence handling for Proton
// Calendar events.
//
// Proton's server does NO occurrence expansion: the API returns "raw event"
// rows carrying plaintext recurrence metadata (RRule, RecurrenceID, Exdates),
// and clients are expected to expand recurring masters into concrete
// occurrences themselves. This package provides:
//
//   - BuildRRule / SanitizeRRule to construct and validate RRULE values
//     within Proton's server-side limits (FREQ restricted to
//     DAILY/WEEKLY/MONTHLY/YEARLY, COUNT <= 49, UNTIL <= 2037-12-31, COUNT
//     and UNTIL mutually exclusive),
//   - ExpandOccurrences to expand a mixed list of raw rows (masters,
//     single-edit "exception" rows, plain events) into the occurrences that
//     overlap a time window, honouring EXDATEs and exception shadowing,
//   - ResolveOccurrence to map a user-specified original-occurrence start
//     timestamp onto either an existing exception row or a generated
//     occurrence, for single-occurrence operations.
//
// The implementation is kept dependency-light on purpose: stdlib +
// github.com/teambition/rrule-go only.
package recurrence

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/teambition/rrule-go"

	"github.com/cheeseandcereal/proton-cal/internal/icaltime"
)

const (
	// maxCount is the largest COUNT the Proton API accepts.
	maxCount = 49
	// maxOccurrencesPerMaster caps occurrences generated per master per window.
	maxOccurrencesPerMaster = 1000
)

// maxUntil is the latest UNTIL date the Proton API accepts (2037-12-31).
var maxUntil = time.Date(2037, time.December, 31, 0, 0, 0, 0, time.UTC)

// frequencies are the RRULE frequencies accepted by the Proton API.
var frequencies = []string{"DAILY", "WEEKLY", "MONTHLY", "YEARLY"}

func isSupportedFreq(freq string) bool {
	for _, f := range frequencies {
		if f == freq {
			return true
		}
	}
	return false
}

// BuildRRule builds an RRULE string. repeat is a case-insensitive frequency;
// every becomes INTERVAL (omitted when 1); count and until ("YYYY-MM-DD") are
// mutually exclusive. Timed-event UNTIL is end-of-day in tzName (empty=UTC)
// converted to UTC (mirroring Proton web's getSupportedUntil); all-day is a
// floating YYYYMMDD. Errors on bad freq, every<1, count outside [1,maxCount],
// until past 2037-12-31, or both count and until.
func BuildRRule(repeat string, every int, count int, until string, tzName string, allDay bool) (string, error) {
	freq := strings.ToUpper(repeat)
	if !isSupportedFreq(freq) {
		choices := make([]string, len(frequencies))
		for i, f := range frequencies {
			choices[i] = strings.ToLower(f)
		}
		return "", fmt.Errorf("unsupported repeat frequency %q; choose one of: %s", repeat, strings.Join(choices, ", "))
	}
	if every < 1 {
		return "", errors.New("every must be at least 1")
	}
	if count != 0 && until != "" {
		return "", errors.New("count and until are mutually exclusive; specify at most one")
	}
	parts := []string{"FREQ=" + freq}
	if every > 1 {
		parts = append(parts, fmt.Sprintf("INTERVAL=%d", every))
	}
	if count != 0 {
		if count < 1 || count > maxCount {
			return "", fmt.Errorf("count must be between 1 and %d", maxCount)
		}
		parts = append(parts, fmt.Sprintf("COUNT=%d", count))
	}
	if until != "" {
		untilDate, err := time.ParseInLocation("2006-01-02", until, time.UTC)
		if err != nil {
			return "", fmt.Errorf("invalid until date %q: %w", until, err)
		}
		if untilDate.After(maxUntil) {
			return "", fmt.Errorf("until must be on or before %s", maxUntil.Format("2006-01-02"))
		}
		if allDay {
			parts = append(parts, "UNTIL="+untilDate.Format("20060102"))
		} else {
			loc, err := icaltime.LoadLocation(tzName)
			if err != nil {
				return "", err
			}
			localEnd := time.Date(untilDate.Year(), untilDate.Month(), untilDate.Day(), 23, 59, 59, 0, loc)
			parts = append(parts, "UNTIL="+icaltime.FormatUTC(localEnd))
		}
	}
	return strings.Join(parts, ";"), nil
}

// untilYear extracts the year from an RRULE UNTIL value (DATE, local
// DATE-TIME, or UTC DATE-TIME form).
func untilYear(value string) (int, error) {
	if t, ok := icaltime.Parse(value, time.UTC); ok {
		return t.Year(), nil
	}
	return 0, fmt.Errorf("invalid RRULE: bad UNTIL value %q", value)
}

// SanitizeRRule validates and canonicalizes a raw RRULE (optional "RRULE:"
// prefix stripped; keys/values uppercased, parts kept in order) for embedding
// into a signed iCal fragment, preserving DATE-form UNTIL exactly (not
// re-serialized as UTC). Rejects CR/LF (iCal injection), unparseable values,
// missing/unsupported FREQ, COUNT>maxCount, COUNT+UNTIL, and UNTIL past 2037;
// verifies the result parses with rrule-go so we never sign an unexpandable rule.
func SanitizeRRule(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if len(value) >= 6 && strings.EqualFold(value[:6], "RRULE:") {
		value = strings.TrimSpace(value[6:])
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", errors.New("RRULE must not contain newline characters")
	}
	if value == "" {
		return "", errors.New("invalid RRULE: empty value")
	}

	seen := make(map[string]bool)
	parts := make([]string, 0, 4)
	var (
		freq               string
		count              int
		hasCount, hasUntil bool
		untilValue         string
	)
	for _, part := range strings.Split(value, ";") {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			return "", fmt.Errorf("invalid RRULE: malformed part %q", part)
		}
		key = strings.ToUpper(key)
		val = strings.ToUpper(val)
		if key == "" || val == "" {
			return "", fmt.Errorf("invalid RRULE: empty key or value in part %q", part)
		}
		if seen[key] {
			return "", fmt.Errorf("invalid RRULE: duplicate part %s", key)
		}
		seen[key] = true
		switch key {
		case "FREQ":
			freq = val
		case "COUNT":
			n, err := strconv.Atoi(val)
			if err != nil {
				return "", fmt.Errorf("invalid RRULE: bad COUNT value %q", val)
			}
			count = n
			hasCount = true
		case "UNTIL":
			untilValue = val
			hasUntil = true
		}
		parts = append(parts, key+"="+val)
	}

	if freq == "" {
		return "", errors.New("RRULE must specify FREQ")
	}
	if !isSupportedFreq(freq) {
		return "", fmt.Errorf("unsupported FREQ %q; Proton supports: %s", freq, strings.Join(frequencies, ", "))
	}
	if hasCount && hasUntil {
		return "", errors.New("RRULE must not combine COUNT and UNTIL")
	}
	if hasCount && count > maxCount {
		return "", fmt.Errorf("COUNT must be at most %d", maxCount)
	}
	if hasUntil {
		year, err := untilYear(untilValue)
		if err != nil {
			return "", err
		}
		if year > maxUntil.Year() {
			return "", fmt.Errorf("UNTIL must be no later than %s", maxUntil.Format("2006-01-02"))
		}
	}

	canonical := strings.Join(parts, ";")
	// Verify rrule-go can parse/expand it, but return OUR canonical string:
	// rrule-go re-serializes UNTIL as a UTC datetime, corrupting DATE-form
	// (all-day) values like UNTIL=20371231.
	opt, err := rrule.StrToROptionInLocation(canonical, time.UTC)
	if err != nil {
		return "", fmt.Errorf("invalid RRULE: %w", err)
	}
	if _, err := rrule.NewRRule(*opt); err != nil {
		return "", fmt.Errorf("invalid RRULE: %w", err)
	}
	return canonical, nil
}
