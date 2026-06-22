package recurrence

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/icaltime"
	"github.com/teambition/rrule-go"
)

// Occurrence is one concrete occurrence of an event row within a window.
type Occurrence struct {
	Event *caltypes.RawEvent // the backing row (master, exception, or plain)
	Start int64              // occurrence start unix ts
	End   int64              // occurrence end unix ts
}

// masterLocation returns the timezone a timed master's occurrences are
// generated in (StartTimezone, falling back to UTC when empty).
func masterLocation(ev *caltypes.RawEvent) (*time.Location, error) {
	loc, err := icaltime.LoadLocation(ev.StartTimezone)
	if err != nil {
		return nil, fmt.Errorf("invalid start timezone: %w", err)
	}
	return loc, nil
}

// masterRule parses a master's RRULE anchored at its DTSTART, returning the
// rule, duration (seconds), and iteration location. Timed masters iterate in
// the start timezone (preserving wall-clock across DST); all-day masters
// anchor in UTC, matching the midnight-UTC instants Proton stores.
func masterRule(ev *caltypes.RawEvent) (*rrule.RRule, int64, *time.Location, error) {
	duration := ev.EndTime - ev.StartTime
	var loc *time.Location
	if ev.IsAllDay() {
		loc = time.UTC
	} else {
		var err error
		loc, err = masterLocation(ev)
		if err != nil {
			return nil, 0, nil, err
		}
	}
	dtstart := time.Unix(ev.StartTime, 0).In(loc)
	// Parse in loc so a DATE-form or local-form UNTIL is interpreted in
	// the event's zone (UTC for all-day events).
	opt, err := rrule.StrToROptionInLocation(ev.RRule, loc)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("parsing RRULE %q: %w", ev.RRule, err)
	}
	opt.Dtstart = dtstart
	rule, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("building RRULE %q: %w", ev.RRule, err)
	}
	return rule, duration, loc, nil
}

// windowDt converts a unix timestamp into the datetime space used by a
// master's rule (loc is UTC for all-day masters).
func windowDt(ts int64, loc *time.Location) time.Time {
	return time.Unix(ts, 0).In(loc)
}

// expandMaster expands one master into occurrences overlapping [start, end),
// skipping EXDATEd/shadowed starts, capped at maxOccurrencesPerMaster.
func expandMaster(ev *caltypes.RawEvent, start, end int64, shadowed map[int64]bool) ([]Occurrence, error) {
	rule, duration, loc, err := masterRule(ev)
	if err != nil {
		return nil, err
	}
	exdates := make(map[int64]bool, len(ev.Exdates))
	for _, ts := range ev.Exdates {
		exdates[ts] = true
	}
	// Widen the search start by duration so occurrences that START before
	// the window but OVERLAP it (long events) are still generated.
	winStart := windowDt(start-duration, loc)
	winEnd := windowDt(end, loc)
	var occurrences []Occurrence
	// Iterate lazily (rule.Between would materialize every occurrence in
	// the window before the cap could fire).
	next := rule.Iterator()
	for {
		occDt, ok := next()
		if !ok || occDt.After(winEnd) {
			break
		}
		if occDt.Before(winStart) {
			continue
		}
		occStart := occDt.Unix()
		if exdates[occStart] || shadowed[occStart] {
			continue
		}
		occEnd := occStart + duration
		if occStart >= end || occEnd <= start {
			continue
		}
		occurrences = append(occurrences, Occurrence{Event: ev, Start: occStart, End: occEnd})
		if len(occurrences) >= maxOccurrencesPerMaster {
			break
		}
	}
	return occurrences, nil
}

// appendIfOverlapping appends a row with its own StartTime/EndTime if it
// overlaps [start, end).
func appendIfOverlapping(results []Occurrence, ev *caltypes.RawEvent, start, end int64) []Occurrence {
	if ev.StartTime < end && ev.EndTime > start {
		results = append(results, Occurrence{Event: ev, Start: ev.StartTime, End: ev.EndTime})
	}
	return results
}

// ExpandOccurrences expands mixed raw rows over [start, end). Plain and
// exception rows (non-zero RecurrenceID) pass through on overlap; masters are
// expanded, skipping EXDATEd and same-UID-exception-shadowed starts (capped at
// maxOccurrencesPerMaster). A malformed RRULE passes through as a single row
// rather than crashing the listing. Results are sorted by start, then event ID.
func ExpandOccurrences(events []*caltypes.RawEvent, start, end int64) []Occurrence {
	shadowed := make(map[string]map[int64]bool)
	for _, ev := range events {
		if ev.RecurrenceID != 0 {
			m := shadowed[ev.UID]
			if m == nil {
				m = make(map[int64]bool)
				shadowed[ev.UID] = m
			}
			m[ev.RecurrenceID] = true
		}
	}

	var results []Occurrence
	for _, ev := range events {
		switch {
		case ev.RecurrenceID != 0:
			results = appendIfOverlapping(results, ev, start, end)
		case ev.RRule != "":
			occs, err := expandMaster(ev, start, end, shadowed[ev.UID])
			if err != nil {
				// Unexpandable RRULE: pass through as a single row.
				results = appendIfOverlapping(results, ev, start, end)
				continue
			}
			results = append(results, occs...)
		default:
			results = appendIfOverlapping(results, ev, start, end)
		}
	}

	slices.SortStableFunc(results, func(a, b Occurrence) int {
		return cmp.Or(
			cmp.Compare(a.Start, b.Start),
			cmp.Compare(a.Event.ID, b.Event.ID),
		)
	})
	return results
}

// ResolveOccurrence resolves an original-occurrence start timestamp for
// single-occurrence operations. related are same-UID rows. Returns the
// existing exception row if already single-edited, (nil, nil) if occurrenceTS
// is a live generated occurrence, or an error when the master is not recurring,
// the occurrence was EXDATE-deleted, or the timestamp matches none.
func ResolveOccurrence(master *caltypes.RawEvent, related []*caltypes.RawEvent, occurrenceTS int64) (*caltypes.RawEvent, error) {
	if master.RRule == "" {
		return nil, fmt.Errorf("event %s is not a recurring event", master.ID)
	}
	for _, row := range related {
		if row.RecurrenceID != 0 && row.RecurrenceID == occurrenceTS {
			return row, nil
		}
	}
	for _, ts := range master.Exdates {
		if ts == occurrenceTS {
			return nil, fmt.Errorf("occurrence %d was already deleted (EXDATE)", occurrenceTS)
		}
	}
	rule, _, loc, err := masterRule(master)
	if err != nil {
		return nil, fmt.Errorf("event %s: %w", master.ID, err)
	}
	if occ := rule.After(windowDt(occurrenceTS, loc), true); !occ.IsZero() && occ.Unix() == occurrenceTS {
		return nil, nil
	}
	return nil, fmt.Errorf("timestamp %d is not an occurrence of this event", occurrenceTS)
}
