package calsvc

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/event"
	"github.com/cheeseandcereal/proton-cal/pkg/eventview"
	"github.com/cheeseandcereal/proton-cal/pkg/icaltime"
)

// ListEventsInput addresses a listing window with user-shaped strings. The
// window spans one or more calendars: an empty Calendars (with AllCalendars
// false) lists the default-or-first calendar; AllCalendars lists every calendar.
type ListEventsInput struct {
	Calendars    []string // calendar IDs or names; empty = default, else first
	AllCalendars bool     // list every calendar (ignores Calendars)
	TZ           string   // IANA zone override; "" = configured / system
	From         string   // window start "YYYY-MM-DD [HH:MM]"; "" = now
	Days         int      // days to look ahead; <= 0 = 7
}

// ListedItem is one expanded occurrence tagged with the calendar it came from.
// event.Listed is embedded so callers reach Event/Occurrence directly; Calendar
// and Settings give the per-item calendar context needed when a listing spans
// multiple calendars (each calendar has its own color and default reminders).
type ListedItem struct {
	event.Listed
	Calendar calendar.Info
	Settings calendar.Settings
}

// EventList is a resolved, expanded, decrypted listing window. It may span
// multiple calendars; each item carries its own calendar context.
type EventList struct {
	Calendars []calendar.Info // the resolved calendars this window covers
	Location  *time.Location  // display zone the read was resolved in
	From      time.Time
	FromGiven bool // false when the window starts "now"
	Days      int
	Items     []ListedItem // sorted by occurrence start, then row ID
}

// SingleCalendar reports whether the window covers exactly one calendar (the
// common case); frontends use it to decide whether to label each event with its
// calendar.
func (l *EventList) SingleCalendar() bool { return len(l.Calendars) <= 1 }

// calendarListing is one calendar's portion of a multi-calendar window.
type calendarListing struct {
	info     calendar.Info
	settings calendar.Settings
	listed   []event.Listed
	degraded bool
}

// ListEvents lists all event occurrences in the addressed window across one or
// more calendars, merged and sorted by occurrence start.
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

	cals, err := s.resolveCalendars(ctx, in.Calendars, in.AllCalendars)
	if err != nil {
		return nil, err
	}
	// Preserve the single-calendar notice: an empty selector resolving to one
	// calendar tells the user which one was chosen.
	if len(in.Calendars) == 0 && !in.AllCalendars && len(cals) == 1 {
		s.notify("Using calendar: " + cals[0].Name)
	}

	listings, err := s.listAcrossCalendars(ctx, cals, start.Unix(), end.Unix(), tz)
	if err != nil {
		return nil, err
	}

	items := make([]ListedItem, 0)
	for _, lg := range listings {
		for _, l := range lg.listed {
			items = append(items, ListedItem{Listed: l, Calendar: lg.info, Settings: lg.settings})
		}
	}
	slices.SortStableFunc(items, func(a, b ListedItem) int {
		return cmp.Or(
			cmp.Compare(a.Occurrence.Start, b.Occurrence.Start),
			cmp.Compare(a.Occurrence.Event.ID, b.Occurrence.Event.ID),
		)
	})

	return &EventList{
		Calendars: cals,
		Location:  loc,
		From:      start,
		FromGiven: in.From != "",
		Days:      days,
		Items:     items,
	}, nil
}

// CreateEventInput describes a new event with user-shaped strings.
type CreateEventInput struct {
	Summary     string
	Description string
	Location    string
	Start       string // "YYYY-MM-DD HH:MM"; with AllDay: "YYYY-MM-DD"
	End         string // timed: "" defaults to start + calendar default duration; all-day: inclusive end date, "" = Start
	AllDay      bool
	Recurrence  Recurrence
	Calendar    string
	TZ          string
	// Reminders + RemindersSet select the tri-state (false = inherit default,
	// true+empty = none, true+non-empty = custom); pre-parsed by the frontend.
	Reminders    []caltypes.Notification
	RemindersSet bool
	// Color is the per-event color override ("#RRGGBB"); "" = inherit.
	Color string
}

// CreatedEvent reports a create outcome. ID/UID empty when the server didn't echo
// the row; Start/End are echoed times if available, else requested. Reminders/Color
// reflect what was REQUESTED (the create response doesn't reliably echo row fields).
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

	start, end, endProvided, err := resolveCreateTimes(in.Start, in.End, in.AllDay, tz)
	if err != nil {
		return nil, err
	}
	rrule, err := in.Recurrence.buildRRule(tz, in.AllDay)
	if err != nil {
		return nil, err
	}

	return withAccessResult(ctx, s, in.Calendar, func(_ calendar.Info, access *calendar.Access) (*CreatedEvent, bool, error) {
		// A timed event with no explicit end defaults to the calendar's default
		// duration; resolved here since it needs the unlocked calendar's settings.
		end := end
		if !endProvided {
			defaulted, derr := applyDefaultDuration(start, access.Settings)
			if derr != nil {
				return nil, false, derr
			}
			end = defaulted
		}
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
	// NoSeries (with WithICS on a recurring event) limits the export to the single
	// addressed VEVENT instead of the whole series; no-op (warns) for non-recurring.
	NoSeries bool
}

// GotEvent is a single decrypted event with its raw row, the resolved
// calendar, the display zone, and (when requested) the reconstructed ICS.
type GotEvent struct {
	Calendar calendar.Info
	Settings calendar.Settings // calendar default reminders (for inheritance)
	Location *time.Location    // display zone the read was resolved in
	Event    *event.Event
	Raw      *caltypes.RawEvent
	ICS      string // populated only when GetEventInput.WithICS
}

// ErrICSUndecryptable is the error frontends return when an ICS export was
// requested but no card could be decrypted (GotEvent.ICS is empty).
var ErrICSUndecryptable = errors.New("event could not be decrypted into iCalendar")

// GetEvent fetches, decrypts and (optionally) reconstructs the ICS of a single
// event. It must live in the resolved calendar (default/first unless Calendar
// is given); a missing event returns an error advising --calendar.
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
		got := &GotEvent{Calendar: info, Settings: access.Settings, Location: loc, Event: ev, Raw: raw}
		degraded := ev.DecryptFailed
		if in.WithICS {
			ics, icsDegraded, ierr := s.buildEventICS(ctx, in.NoSeries, info, access, raw, ev)
			if ierr != nil {
				return nil, false, ierr
			}
			got.ICS = ics
			degraded = degraded || icsDegraded
		}
		return got, degraded, nil
	})
}

// buildEventICS produces the iCalendar export. Unless noSeries is set, a recurring
// event expands into the whole series (one VCALENDAR, master + a VEVENT per edited
// occurrence) with EFFECTIVE COLOR/VALARMs. The degraded flag is true when any row
// failed to decrypt (caller drives the key-refresh retry); no content yields "".
func (s *Service) buildEventICS(ctx context.Context, noSeries bool, info calendar.Info, access *calendar.Access, raw *caltypes.RawEvent, ev *event.Event) (ics string, degraded bool, err error) {
	single := func() (string, bool, error) {
		out, ierr := event.BuildICS(raw, access.KR, s.effectiveExtras(ev, info, access.Settings))
		if ierr != nil && !errors.Is(ierr, event.ErrDecryptDegraded) {
			return "", false, fmt.Errorf("building ICS: %w", ierr)
		}
		return out, errors.Is(ierr, event.ErrDecryptDegraded), nil
	}

	if noSeries {
		if !ev.IsRecurring() && raw.RecurrenceID == 0 {
			s.notify("--no-series ignored: event is not recurring")
		}
		return single()
	}

	rows, err := event.GetByUID(ctx, s.client, info.ID, raw.UID)
	if err != nil {
		return "", false, fmt.Errorf("fetching series: %w", err)
	}
	// A non-recurring event (no master among the UID rows) needs no
	// expansion; emit just its single VEVENT.
	if !seriesHasMaster(rows) {
		return single()
	}

	extras := make(map[string]event.RowExtras, len(rows))
	for _, r := range rows {
		rev, derr := event.Decrypt(r, access.KR)
		if derr != nil || rev.DecryptFailed {
			// Leave this row out of the extras map; BuildSeriesICS falls
			// back to its literal columns (and may skip it if undecryptable).
			degraded = true
			continue
		}
		extras[r.ID] = s.effectiveExtras(rev, info, access.Settings)
	}

	out, anyFailed, ierr := event.BuildSeriesICS(rows, access.KR, extras)
	if ierr != nil && !errors.Is(ierr, event.ErrDecryptDegraded) {
		return "", false, fmt.Errorf("building series ICS: %w", ierr)
	}
	return out, degraded || anyFailed || errors.Is(ierr, event.ErrDecryptDegraded), nil
}

// effectiveExtras resolves the injected COLOR/VALARM data to the values that
// actually apply (event's own when set, else calendar default).
func (s *Service) effectiveExtras(ev *event.Event, info calendar.Info, set calendar.Settings) event.RowExtras {
	return event.RowExtras{
		Color:         eventview.EffectiveColor(ev, info),
		Notifications: eventview.EffectiveReminders(ev, set),
	}
}

// seriesHasMaster reports whether any row is a recurring master (RRULE set,
// no RECURRENCE-ID).
func seriesHasMaster(rows []*caltypes.RawEvent) bool {
	for _, r := range rows {
		if r != nil && r.IsMaster() {
			return true
		}
	}
	return false
}

// GotCalendar is a single resolved calendar with its default settings and a
// flag for whether it is the configured default.
type GotCalendar struct {
	Info      calendar.Info
	Settings  calendar.Settings
	IsDefault bool
}

// GetCalendar resolves a calendar by selector ("" = configured default, else
// first), unlocks it to read default settings, and reports whether it is the
// configured default. The bootstrap fetch is cached.
func (s *Service) GetCalendar(ctx context.Context, selector string) (*GotCalendar, error) {
	// The server-side default ID determines IsDefault. A failure to read it
	// is non-fatal: the calendar is simply not marked default.
	defaultID, _ := s.DefaultCalendarID(ctx)
	return withAccessResult(ctx, s, selector, func(info calendar.Info, access *calendar.Access) (*GotCalendar, bool, error) {
		return &GotCalendar{
			Info:      info,
			Settings:  access.Settings,
			IsDefault: defaultID != "" && info.ID == defaultID,
		}, false, nil
	})
}

// anyDecryptFailed reports whether any listed event came back degraded.
func anyDecryptFailed(listed []event.Listed) bool {
	return slices.ContainsFunc(listed, func(l event.Listed) bool {
		return l.Event != nil && l.Event.DecryptFailed
	})
}

// UpdateEventInput describes a partial update with user-shaped strings. Text
// fields are pointers so frontends choose presence semantics (CLI sets on flag
// presence, empty clears; MCP only for non-empty values).
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
	// Reminders: nil = keep current; non-nil overrides (frontend resolves the
	// keep/inherit/none/custom intent into the event tri-state type).
	Reminders *event.RemindersUpdate
	// Color: nil = keep current; non-nil overrides. Proton has no color "inherit"
	// sentinel - reverting means setting the calendar's own color, which UpdateEvent
	// resolves, so this carries the intent rather than a literal hex.
	Color *ColorUpdate
}

// ColorUpdate is the calsvc-level color change intent. Inherit reverts to the
// calendar's color (resolved at apply time); else Hex is the palette color to set.
type ColorUpdate struct {
	Inherit bool
	Hex     string
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

// updateTZName picks the update timezone: the explicit override, else the default
// only when a new start/end is set, else "" (keep the event's stored timezone).
func updateTZName(override, defaultTZ string, timesGiven bool) string {
	if override != "" {
		return override
	}
	if timesGiven {
		return defaultTZ
	}
	return ""
}

// UpdateEvent validates, resolves and applies a partial update; series and
// single-occurrence semantics are handled per event.SmartUpdate.
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
		// Resolve the color intent now the calendar is known: "inherit" maps to
		// the calendar's own color (Proton has no clear/null for color).
		if in.Color != nil {
			hex := in.Color.Hex
			if in.Color.Inherit {
				hex = info.Color
			}
			opts.Color = &event.ColorUpdate{Value: hex}
		}

		// Recurrence options need the event's all-day-ness for the UNTIL form,
		// so fetch the raw row first - only here (the occurrence path builds no RRULE).
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
			// Occurrence deletion EXDATEs the master so needs full decryption;
			// ErrDecryptDegraded retries with fresh key material via withAccess.
			return nil, false, fmt.Errorf("deleting event: %w", err)
		}
		return r, false, nil
	})
}
