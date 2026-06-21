package calsvc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/icaltime"
)

// ListEventsInput addresses a listing window with user-shaped strings.
type ListEventsInput struct {
	Calendar string // calendar ID or name; "" = default, else first
	TZ       string // IANA zone override; "" = configured / system
	From     string // window start "YYYY-MM-DD [HH:MM]"; "" = now
	Days     int    // days to look ahead; <= 0 = 7
}

// EventList is a resolved, expanded, decrypted listing window.
type EventList struct {
	Calendar  calendar.Info
	Location  *time.Location // display zone the listing was resolved in
	From      time.Time
	FromGiven bool // false when the window starts "now"
	Days      int
	Items     []event.Listed // sorted by occurrence start, then row ID
}

// ListEvents lists all event occurrences in the addressed window.
func (s *Service) ListEvents(ctx context.Context, in ListEventsInput) (*EventList, error) {
	days := in.Days
	if days <= 0 {
		days = 7
	}
	tz := s.EffectiveTimezone(in.TZ)
	loc, err := icaltime.LoadLocation(tz)
	if err != nil {
		return nil, err
	}

	start := time.Now()
	if in.From != "" {
		start, err = parseWhen(in.From, tz)
		if err != nil {
			return nil, err
		}
	}
	end := start.AddDate(0, 0, days)

	var out *EventList
	attempt := 0
	werr := s.withAccess(ctx, in.Calendar, func(info calendar.Info, access *calendar.Access) error {
		attempt++
		listed, err := event.ListWindow(ctx, s.client, access.KR, info.ID, start.Unix(), end.Unix(), tz)
		if err != nil {
			return fmt.Errorf("listing events: %w", err)
		}
		sort.SliceStable(listed, func(i, j int) bool {
			if listed[i].Occurrence.Start != listed[j].Occurrence.Start {
				return listed[i].Occurrence.Start < listed[j].Occurrence.Start
			}
			return listed[i].Occurrence.Event.ID < listed[j].Occurrence.Event.ID
		})
		out = &EventList{
			Calendar:  info,
			Location:  loc,
			From:      start,
			FromGiven: in.From != "",
			Days:      days,
			Items:     listed,
		}
		// Stale cached calendar keys decrypt nothing but fail silently
		// (the listing renders blank). Surface the degradation once so
		// withAccess can refetch keys and relist; a second degraded pass
		// is accepted (lenient rendering of genuinely bad rows).
		if attempt == 1 && anyDecryptFailed(listed) {
			return fmt.Errorf("listing events: %w", event.ErrDecryptDegraded)
		}
		return nil
	})
	if werr != nil && (!errors.Is(werr, event.ErrDecryptDegraded) || out == nil) {
		return nil, werr
	}
	return out, nil
}

// CreateEventInput describes a new event with user-shaped strings.
type CreateEventInput struct {
	Summary     string
	Description string
	Location    string
	Start       string // "YYYY-MM-DD HH:MM"; with AllDay: "YYYY-MM-DD"
	End         string // required for timed events; all-day: inclusive end date, "" = Start
	AllDay      bool
	Recurrence  Recurrence
	Calendar    string
	TZ          string
}

// CreatedEvent reports a create outcome. ID/UID are empty when the server
// did not echo the created row; Start/End are the server-echoed times when
// available, else the requested ones.
type CreatedEvent struct {
	ID      string
	UID     string
	Summary string
	Start   time.Time
	End     time.Time
	AllDay  bool
	RRule   string
}

// CreateEvent validates, resolves and creates an event.
func (s *Service) CreateEvent(ctx context.Context, in CreateEventInput) (*CreatedEvent, error) {
	tz := s.EffectiveTimezone(in.TZ)

	start, end, err := resolveCreateTimes(in.Start, in.End, in.AllDay, tz)
	if err != nil {
		return nil, err
	}
	rrule, err := in.Recurrence.buildRRule(tz, in.AllDay)
	if err != nil {
		return nil, err
	}

	var out *CreatedEvent
	werr := s.withAccess(ctx, in.Calendar, func(_ calendar.Info, access *calendar.Access) error {
		raw, err := event.Create(ctx, s.client, access, event.CreateOptions{
			Summary:     in.Summary,
			Description: in.Description,
			Location:    in.Location,
			Start:       start,
			End:         end,
			TZName:      tz,
			AllDay:      in.AllDay,
			RRule:       rrule,
		})
		if err != nil {
			return fmt.Errorf("creating event: %w", err)
		}
		out = &CreatedEvent{Summary: in.Summary, Start: start, End: end, AllDay: in.AllDay, RRule: rrule}
		if raw != nil {
			out.ID = raw.ID
			out.UID = raw.UID
			out.Start = time.Unix(raw.StartTime, 0).UTC()
			out.End = time.Unix(raw.EndTime, 0).UTC()
		}
		return nil
	})
	if werr != nil {
		return nil, werr
	}
	return out, nil
}

// GetEventInput addresses a single event for detailed retrieval.
type GetEventInput struct {
	EventID  string
	Calendar string // calendar ID or name; "" = default, else first
	TZ       string
	WithICS  bool // also reconstruct the raw iCalendar (BuildICS)
}

// GotEvent is a single decrypted event with its raw row, the resolved
// calendar, the display zone, and (when requested) the reconstructed ICS.
type GotEvent struct {
	Calendar calendar.Info
	Location *time.Location
	Event    *event.Event
	Raw      *caltypes.RawEvent
	ICS      string // populated only when GetEventInput.WithICS
}

// GetEvent fetches, decrypts and (optionally) reconstructs the ICS of a
// single event in the addressed calendar. The event must live in the
// resolved calendar (default/first unless Calendar is given); a missing
// event returns an error advising --calendar.
func (s *Service) GetEvent(ctx context.Context, in GetEventInput) (*GotEvent, error) {
	if in.EventID == "" {
		return nil, fmt.Errorf("event ID is required")
	}
	tz := s.EffectiveTimezone(in.TZ)
	loc, err := icaltime.LoadLocation(tz)
	if err != nil {
		return nil, err
	}

	var out *GotEvent
	attempt := 0
	werr := s.withAccess(ctx, in.Calendar, func(info calendar.Info, access *calendar.Access) error {
		attempt++
		raw, err := event.Get(ctx, s.client, info.ID, in.EventID)
		if err != nil {
			return fmt.Errorf("fetching event: %w", err)
		}
		ev, err := event.Decrypt(raw, access.KR)
		if err != nil {
			return fmt.Errorf("decrypting event: %w", err)
		}
		got := &GotEvent{Calendar: info, Location: loc, Event: ev, Raw: raw}
		if in.WithICS {
			ics, ierr := event.BuildICS(raw, access.KR)
			if ierr != nil && !errors.Is(ierr, event.ErrDecryptDegraded) {
				return fmt.Errorf("building ICS: %w", ierr)
			}
			got.ICS = ics
		}
		out = got
		// Stale cached calendar keys decrypt nothing; surface the
		// degradation once so withAccess refreshes keys and retries.
		if attempt == 1 && ev.DecryptFailed {
			return fmt.Errorf("decrypting event: %w", event.ErrDecryptDegraded)
		}
		return nil
	})
	if werr != nil && (!errors.Is(werr, event.ErrDecryptDegraded) || out == nil) {
		return nil, werr
	}
	return out, nil
}

// GotCalendar is a single resolved calendar with a flag for whether it is
// the configured default.
type GotCalendar struct {
	Info      calendar.Info
	IsDefault bool
}

// GetCalendar resolves a single calendar by selector (ID or name; "" = the
// configured default, else first) and reports whether it is the configured
// default. It reuses the cached calendar list and adds no network calls
// beyond the existing list fetch resolveCalendar already performs.
func (s *Service) GetCalendar(ctx context.Context, selector string) (*GotCalendar, error) {
	info, err := s.resolveCalendar(ctx, selector)
	if err != nil {
		return nil, err
	}
	return &GotCalendar{Info: info, IsDefault: info.Matches(s.cfg.DefaultCalendar)}, nil
}

// anyDecryptFailed reports whether any listed event came back degraded.
func anyDecryptFailed(listed []event.Listed) bool {
	for _, l := range listed {
		if l.Event != nil && l.Event.DecryptFailed {
			return true
		}
	}
	return false
}

// UpdateEventInput describes a partial update with user-shaped strings.
// Text fields are pointers so frontends can choose their presence
// semantics: the CLI sets them on flag presence (an empty string clears the
// field), the MCP server only for non-empty values.
type UpdateEventInput struct {
	EventID     string
	Summary     *string
	Description *string
	Location    *string
	Start       string // "" = keep
	End         string // "" = keep
	Occurrence  string // "" = whole event/series; else the ORIGINAL start of one occurrence
	NoRepeat    bool
	Recurrence  Recurrence
	Calendar    string
	TZ          string
}

// validate rejects conflicting update option combinations.
func (in UpdateEventInput) validate() error {
	if in.NoRepeat && (in.Recurrence.Repeat != "" || in.Recurrence.RawRRule != "") {
		return errors.New("no-repeat cannot be combined with repeat/rrule")
	}
	if in.Occurrence != "" && (in.NoRepeat || in.Recurrence.Repeat != "" || in.Recurrence.RawRRule != "") {
		return errors.New("recurrence options cannot be combined with an occurrence edit (edit the series instead)")
	}
	return nil
}

// updateTZName picks the timezone to pass on updates: the explicit
// override when given, else the default timezone only when a new start or
// end is being set, else "" (keep the event's stored timezone).
func updateTZName(override, defaultTZ string, timesGiven bool) string {
	if override != "" {
		return override
	}
	if timesGiven {
		return defaultTZ
	}
	return ""
}

// UpdateEvent validates, resolves and applies a partial update (smart:
// series semantics and single-occurrence edits are handled per
// event.SmartUpdate).
func (s *Service) UpdateEvent(ctx context.Context, in UpdateEventInput) (*event.UpdateOutcome, error) {
	if err := in.validate(); err != nil {
		return nil, err
	}
	tz := s.EffectiveTimezone(in.TZ)

	opts := event.UpdateOptions{
		ClearRRule:  in.NoRepeat,
		Summary:     in.Summary,
		Description: in.Description,
		Location:    in.Location,
	}
	if in.Start != "" {
		t, err := parseWhen(in.Start, tz)
		if err != nil {
			return nil, err
		}
		opts.Start = &t
	}
	if in.End != "" {
		t, err := parseWhen(in.End, tz)
		if err != nil {
			return nil, err
		}
		opts.End = &t
	}
	opts.TZName = updateTZName(in.TZ, tz, opts.Start != nil || opts.End != nil)

	var occurrenceTS int64
	if in.Occurrence != "" {
		var err error
		occurrenceTS, err = parseOccurrence(in.Occurrence, tz)
		if err != nil {
			return nil, err
		}
	}

	var outcome *event.UpdateOutcome
	werr := s.withAccess(ctx, in.Calendar, func(info calendar.Info, access *calendar.Access) error {
		// Recurrence options need the event's all-day-ness for the UNTIL
		// form, so fetch the raw row first - only in that case (the
		// occurrence path never builds an RRULE).
		if in.Occurrence == "" && !in.Recurrence.Empty() {
			raw, err := event.Get(ctx, s.client, info.ID, in.EventID)
			if err != nil {
				return fmt.Errorf("fetching event: %w", err)
			}
			rrule, err := in.Recurrence.buildRRule(tz, raw.IsAllDay())
			if err != nil {
				return err
			}
			if rrule != "" {
				opts.RRule = &rrule
			}
		}

		o, err := event.SmartUpdate(ctx, s.client, access, in.EventID, opts, occurrenceTS)
		if err != nil {
			return fmt.Errorf("updating event: %w", err)
		}
		outcome = o
		return nil
	})
	if werr != nil {
		return nil, werr
	}
	return outcome, nil
}

// DeleteEventInput addresses a deletion with user-shaped strings.
type DeleteEventInput struct {
	EventID    string
	Occurrence string // "" = whole event/series; else the ORIGINAL start of one occurrence
	Calendar   string
	TZ         string
}

// DeleteEvent resolves and deletes an event, series or single occurrence
// (per event.SmartDelete).
func (s *Service) DeleteEvent(ctx context.Context, in DeleteEventInput) (*event.DeleteResult, error) {
	tz := s.EffectiveTimezone(in.TZ)

	var occurrenceTS int64
	if in.Occurrence != "" {
		var err error
		occurrenceTS, err = parseOccurrence(in.Occurrence, tz)
		if err != nil {
			return nil, err
		}
	}

	var res *event.DeleteResult
	werr := s.withAccess(ctx, in.Calendar, func(_ calendar.Info, access *calendar.Access) error {
		r, err := event.SmartDelete(ctx, s.client, access, in.EventID, occurrenceTS)
		if err != nil {
			// Occurrence deletion merges an EXDATE into the master and
			// so needs full decryption; ErrDecryptDegraded retries with
			// fresh key material via withAccess.
			return fmt.Errorf("deleting event: %w", err)
		}
		res = r
		return nil
	})
	if werr != nil {
		return nil, werr
	}
	return res, nil
}
