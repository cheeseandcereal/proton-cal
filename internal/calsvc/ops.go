package calsvc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
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

// resolved is the calendar context shared by every read result: the resolved
// calendar, its default settings (for reminder/color inheritance) and the
// display zone the read was resolved in. It is embedded so callers can reach
// the fields directly (e.g. list.Calendar, got.Settings).
type resolved struct {
	Calendar calendar.Info
	Settings calendar.Settings // calendar default reminders (for inheritance)
	Location *time.Location    // display zone the read was resolved in
}

// EventList is a resolved, expanded, decrypted listing window.
type EventList struct {
	resolved
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

	// Stale cached calendar keys decrypt nothing but fail silently (the
	// listing renders blank); the degraded flag makes withAccessResult refetch
	// keys and relist once, then accept a still-degraded pass (lenient
	// rendering of genuinely bad rows).
	return withAccessResult(ctx, s, in.Calendar, func(info calendar.Info, access *calendar.Access) (*EventList, bool, error) {
		listed, err := event.ListWindow(ctx, s.client, access.KR, info.ID, start.Unix(), end.Unix(), tz)
		if err != nil {
			return nil, false, fmt.Errorf("listing events: %w", err)
		}
		slices.SortStableFunc(listed, func(a, b event.Listed) int {
			return cmp.Or(
				cmp.Compare(a.Occurrence.Start, b.Occurrence.Start),
				cmp.Compare(a.Occurrence.Event.ID, b.Occurrence.Event.ID),
			)
		})
		return &EventList{
			resolved:  resolved{Calendar: info, Settings: access.Settings, Location: loc},
			From:      start,
			FromGiven: in.From != "",
			Days:      days,
			Items:     listed,
		}, anyDecryptFailed(listed), nil
	})
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
	// Reminders is the event's own reminder set; RemindersSet selects the
	// tri-state (RemindersSet false = inherit the calendar default, true +
	// empty = explicitly none, true + non-empty = custom). Notifications are
	// pre-parsed by the frontend (see internal/reminders).
	Reminders    []caltypes.Notification
	RemindersSet bool
	// Color is the per-event color override ("#RRGGBB"); "" = inherit.
	Color string
}

// CreatedEvent reports a create outcome. ID/UID are empty when the server
// did not echo the created row; Start/End are the server-echoed times when
// available, else the requested ones. Reminders/Color reflect what was
// REQUESTED (the create response does not reliably echo row fields).
type CreatedEvent struct {
	ID          string
	UID         string
	Summary     string
	Description string
	Location    string
	Start       time.Time
	End         time.Time
	AllDay      bool
	RRule       string
	Reminders   []caltypes.Notification
	Color       string
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

	return withAccessResult(ctx, s, in.Calendar, func(_ calendar.Info, access *calendar.Access) (*CreatedEvent, bool, error) {
		raw, err := event.Create(ctx, s.client, access, event.CreateOptions{
			Summary:      in.Summary,
			Description:  in.Description,
			Location:     in.Location,
			Start:        start,
			End:          end,
			TZName:       tz,
			AllDay:       in.AllDay,
			RRule:        rrule,
			Reminders:    in.Reminders,
			RemindersSet: in.RemindersSet,
			Color:        in.Color,
		})
		if err != nil {
			return nil, false, fmt.Errorf("creating event: %w", err)
		}
		out := &CreatedEvent{
			Summary: in.Summary, Description: in.Description, Location: in.Location,
			Start: start, End: end, AllDay: in.AllDay, RRule: rrule,
			Reminders: in.Reminders, Color: in.Color,
		}
		if raw != nil {
			out.ID = raw.ID
			out.UID = raw.UID
			out.Start = time.Unix(raw.StartTime, 0).UTC()
			out.End = time.Unix(raw.EndTime, 0).UTC()
		}
		return out, false, nil
	})
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
	resolved
	Event *event.Event
	Raw   *caltypes.RawEvent
	ICS   string // populated only when GetEventInput.WithICS
}

// ErrICSUndecryptable is the error frontends return when an ICS export was
// requested but no card could be decrypted (GotEvent.ICS is empty).
var ErrICSUndecryptable = errors.New("event could not be decrypted into iCalendar")

// GetEvent fetches, decrypts and (optionally) reconstructs the ICS of a
// single event in the addressed calendar. The event must live in the
// resolved calendar (default/first unless Calendar is given); a missing
// event returns an error advising --calendar.
func (s *Service) GetEvent(ctx context.Context, in GetEventInput) (*GotEvent, error) {
	if in.EventID == "" {
		return nil, errors.New("event ID is required")
	}
	tz := s.EffectiveTimezone(in.TZ)
	loc, err := icaltime.LoadLocation(tz)
	if err != nil {
		return nil, err
	}

	// Stale cached calendar keys decrypt nothing; the degraded flag makes
	// withAccessResult refresh keys and retry once.
	return withAccessResult(ctx, s, in.Calendar, func(info calendar.Info, access *calendar.Access) (*GotEvent, bool, error) {
		raw, err := event.Get(ctx, s.client, info.ID, in.EventID)
		if err != nil {
			return nil, false, fmt.Errorf("fetching event: %w", err)
		}
		ev, err := event.Decrypt(raw, access.KR)
		if err != nil {
			return nil, false, fmt.Errorf("decrypting event: %w", err)
		}
		got := &GotEvent{resolved: resolved{Calendar: info, Settings: access.Settings, Location: loc}, Event: ev, Raw: raw}
		if in.WithICS {
			ics, ierr := event.BuildICS(raw, access.KR)
			if ierr != nil && !errors.Is(ierr, event.ErrDecryptDegraded) {
				return nil, false, fmt.Errorf("building ICS: %w", ierr)
			}
			got.ICS = ics
		}
		return got, ev.DecryptFailed, nil
	})
}

// GotCalendar is a single resolved calendar with its default settings and a
// flag for whether it is the configured default.
type GotCalendar struct {
	Info      calendar.Info
	Settings  calendar.Settings
	IsDefault bool
}

// GetCalendar resolves a single calendar by selector (ID or name; "" = the
// configured default, else first), unlocks it to read its default settings
// (reminders/duration), and reports whether it is the configured default.
// The bootstrap fetch is cached, so repeat calls add no network round-trips.
func (s *Service) GetCalendar(ctx context.Context, selector string) (*GotCalendar, error) {
	return withAccessResult(ctx, s, selector, func(info calendar.Info, access *calendar.Access) (*GotCalendar, bool, error) {
		return &GotCalendar{
			Info:      info,
			Settings:  access.Settings,
			IsDefault: info.Matches(s.cfg.DefaultCalendar),
		}, false, nil
	})
}

// anyDecryptFailed reports whether any listed event came back degraded.
func anyDecryptFailed(listed []event.Listed) bool {
	return slices.ContainsFunc(listed, func(l event.Listed) bool {
		return l.Event != nil && l.Event.DecryptFailed
	})
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
	// Reminders/Color: nil = keep current; non-nil overrides (the frontend
	// resolves the keep/inherit/none/custom intent into these). Reusing the
	// event package's tri-state types keeps the mapping a straight pass.
	Reminders *event.RemindersUpdate
	Color     *event.ColorUpdate
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
		Reminders:   in.Reminders,
		Color:       in.Color,
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

	return withAccessResult(ctx, s, in.Calendar, func(info calendar.Info, access *calendar.Access) (*event.UpdateOutcome, bool, error) {
		// Recurrence options need the event's all-day-ness for the UNTIL
		// form, so fetch the raw row first - only in that case (the
		// occurrence path never builds an RRULE).
		if in.Occurrence == "" && !in.Recurrence.Empty() {
			raw, err := event.Get(ctx, s.client, info.ID, in.EventID)
			if err != nil {
				return nil, false, fmt.Errorf("fetching event: %w", err)
			}
			rrule, err := in.Recurrence.buildRRule(tz, raw.IsAllDay())
			if err != nil {
				return nil, false, err
			}
			if rrule != "" {
				opts.RRule = &rrule
			}
		}

		o, err := event.SmartUpdate(ctx, s.client, access, in.EventID, opts, occurrenceTS)
		if err != nil {
			return nil, false, fmt.Errorf("updating event: %w", err)
		}
		return o, false, nil
	})
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

	return withAccessResult(ctx, s, in.Calendar, func(_ calendar.Info, access *calendar.Access) (*event.DeleteResult, bool, error) {
		r, err := event.SmartDelete(ctx, s.client, access, in.EventID, occurrenceTS)
		if err != nil {
			// Occurrence deletion merges an EXDATE into the master and
			// so needs full decryption; ErrDecryptDegraded retries with
			// fresh key material via withAccess.
			return nil, false, fmt.Errorf("deleting event: %w", err)
		}
		return r, false, nil
	})
}
