package recurrence

import (
	"fmt"
	"sort"
	"time"

	"github.com/cheeseandcereal/proton-cal-go/internal/caltypes"
	"github.com/teambition/rrule-go"
)

// Occurrence is one concrete occurrence of an event row within a window.
type Occurrence struct {
	Event *caltypes.RawEvent // the backing row (master, exception, or plain)
	Start int64              // occurrence start unix ts
	End   int64              // occurrence end unix ts
}

// OccurrenceKind classifies the result of ResolveOccurrence.
type OccurrenceKind int

const (
	// KindException means an exception row already exists for the occurrence.
	KindException OccurrenceKind = iota
	// KindOccurrence means the timestamp is a live generated occurrence of
	// the master's rule (no exception row exists for it).
	KindOccurrence
)

// String returns a human-readable name for the kind.
func (k OccurrenceKind) String() string {
	switch k {
	case KindException:
		return "exception"
	case KindOccurrence:
		return "occurrence"
	default:
		return fmt.Sprintf("OccurrenceKind(%d)", int(k))
	}
}

// masterLocation returns the timezone a timed master's occurrences are
// generated in (StartTimezone, falling back to UTC when empty).
func masterLocation(ev *caltypes.RawEvent) (*time.Location, error) {
	name := ev.StartTimezone
	if name == "" {
		name = "UTC"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("invalid start timezone %q: %w", name, err)
	}
	return loc, nil
}

// masterRule parses a master row's RRULE anchored at its DTSTART.
//
// It returns the rule, the event duration in seconds, and the location the
// rule iterates in. Timed masters get a DTSTART in the event's start
// timezone, so rrule-go iterates in that zone and preserves the local
// wall-clock time across DST transitions (verified by the DST spike test).
// All-day masters get a DTSTART anchored in UTC: their RRULEs use floating
// DATE-form UNTIL values, which are then interpreted at midnight UTC,
// matching the instants Proton stores for all-day events (midnight UTC).
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

// expandMaster expands one master row into occurrences overlapping
// [start, end), skipping EXDATEd and shadowed starts, capped at
// MaxOccurrencesPerMaster.
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
	for _, occDt := range rule.Between(winStart, winEnd, true) {
		occStart := occDt.Unix()
		if exdates[occStart] || shadowed[occStart] {
			continue
		}
		occEnd := occStart + duration
		if !(occStart < end && occEnd > start) {
			continue
		}
		occurrences = append(occurrences, Occurrence{Event: ev, Start: occStart, End: occEnd})
		if len(occurrences) >= MaxOccurrencesPerMaster {
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

// ExpandOccurrences expands a mixed list of raw event rows over the window
// [start, end).
//
// Plain rows and exception rows (non-zero RecurrenceID) pass through with
// their own times when they overlap the window. Master rows (non-empty
// RRule) are expanded, skipping EXDATEd starts and starts shadowed by a
// same-UID exception row, capped at MaxOccurrencesPerMaster. A malformed
// RRULE never crashes the listing: the master falls back to passing through
// as a single row.
//
// Results are sorted by occurrence start, then event ID.
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
				// A master whose RRULE
				// cannot be expanded falls back to passing through
				// as a single row instead of failing the listing.
				results = appendIfOverlapping(results, ev, start, end)
				continue
			}
			results = append(results, occs...)
		default:
			results = appendIfOverlapping(results, ev, start, end)
		}
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Start != results[j].Start {
			return results[i].Start < results[j].Start
		}
		return results[i].Event.ID < results[j].Event.ID
	})
	return results
}

// ResolveOccurrence resolves an original-occurrence start timestamp for
// single-occurrence operations.
//
// related are raw rows sharing the master's UID (the master itself may be
// included; rows without a RecurrenceID are skipped). It returns
// (KindException, row, nil) when an exception row already exists for that
// occurrence, or (KindOccurrence, nil, nil) when occurrenceTS is a live
// generated occurrence of the master's RRULE. It returns an error when the
// master is not recurring, the occurrence was already deleted (EXDATE), or
// the timestamp matches no occurrence. The returned kind is meaningless
// when the error is non-nil.
func ResolveOccurrence(master *caltypes.RawEvent, related []*caltypes.RawEvent, occurrenceTS int64) (OccurrenceKind, *caltypes.RawEvent, error) {
	if master.RRule == "" {
		return 0, nil, fmt.Errorf("event %s is not a recurring event", master.ID)
	}
	for _, row := range related {
		if row.RecurrenceID != 0 && row.RecurrenceID == occurrenceTS {
			return KindException, row, nil
		}
	}
	for _, ts := range master.Exdates {
		if ts == occurrenceTS {
			return 0, nil, fmt.Errorf("occurrence %d was already deleted (EXDATE)", occurrenceTS)
		}
	}
	rule, _, loc, err := masterRule(master)
	if err != nil {
		return 0, nil, fmt.Errorf("event %s: %w", master.ID, err)
	}
	probeStart := windowDt(occurrenceTS-1, loc)
	probeEnd := windowDt(occurrenceTS+1, loc)
	for _, occDt := range rule.Between(probeStart, probeEnd, true) {
		if occDt.Unix() == occurrenceTS {
			return KindOccurrence, nil, nil
		}
	}
	return 0, nil, fmt.Errorf("timestamp %d is not an occurrence of this event", occurrenceTS)
}
