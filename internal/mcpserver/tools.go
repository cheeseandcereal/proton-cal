package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cheeseandcereal/proton-cal-go/internal/event"
	"github.com/cheeseandcereal/proton-cal-go/internal/front"
)

// textResult wraps a string as a successful text tool result.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// invalidArgs decorates a parse/validation error with the accepted time
// formats, replying "invalid arguments: ..." to the caller.
func invalidArgs(err error) error {
	return fmt.Errorf("invalid arguments: %v (times use %s)", err, front.TimeFormatHint)
}

// register adds all proton-calendar tools to the MCP server.
func (s *server) register(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_calendars",
		Description: "List all Proton calendars on this account (name, type, ID, default marker).",
	}, s.listCalendars)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_events",
		Description: "List upcoming calendar events for the next N days. " +
			"Times are shown in the configured default timezone. " +
			"Recurring events are expanded into their individual occurrences, marked \"(recurring)\"; " +
			"pass the shown ID plus the shown \"occurrence start\" value to update_event/delete_event to address one occurrence.",
	}, s.listEvents)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "create_event",
		Description: "Create a calendar event. " +
			"Timed events need start and end (\"YYYY-MM-DD HH:MM\" in the configured default timezone); " +
			"all-day events use dates (\"YYYY-MM-DD\") and end is the inclusive last day (default: start). " +
			"Use repeat/every/count/until (or a raw rrule) to make it recurring.",
	}, s.createEvent)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "update_event",
		Description: "Update an existing calendar event. Only non-empty fields are changed. " +
			"Recurring events keep their recurrence unless repeat/rrule/no_repeat change it. " +
			"Changing a series' times or recurrence removes its edited occurrences. " +
			"Use occurrence to edit ONE occurrence instead of the whole series.",
	}, s.updateEvent)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "delete_event",
		Description: "Delete a calendar event by its ID. " +
			"Recurring events: deletes the whole series (master + edited occurrences) " +
			"unless occurrence limits it to a single occurrence.",
	}, s.deleteEvent)
}

// ---------------------------------------------------------------- list_calendars

type listCalendarsArgs struct{}

func (s *server) listCalendars(ctx context.Context, _ *mcp.CallToolRequest, _ listCalendarsArgs) (*mcp.CallToolResult, any, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, nil, err
	}
	return textResult(renderCalendars(sess.cals, sess.cfg.DefaultCalendar)), nil, nil
}

// ---------------------------------------------------------------- list_events

type listEventsArgs struct {
	Days     int    `json:"days,omitempty" jsonschema:"Number of days to look ahead (default 7)"`
	Calendar string `json:"calendar,omitempty" jsonschema:"Calendar ID or name (optional; default: the configured default calendar, else the first calendar)"`
}

func (s *server) listEvents(ctx context.Context, _ *mcp.CallToolRequest, args listEventsArgs) (*mcp.CallToolResult, any, error) {
	days := args.Days
	if days <= 0 {
		days = 7
	}

	sess, err := s.session(ctx)
	if err != nil {
		return nil, nil, err
	}
	tz := sess.cfg.EffectiveTimezone()
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid timezone %q: %w", tz, err)
	}

	info, access, err := sess.access(ctx, args.Calendar)
	if err != nil {
		return nil, nil, err
	}

	start := time.Now()
	end := start.AddDate(0, 0, days)
	listed, err := event.ListWindow(ctx, sess.client, access.KR, info.ID, start.Unix(), end.Unix(), tz)
	if err != nil {
		return nil, nil, fmt.Errorf("listing events: %w", err)
	}
	sort.SliceStable(listed, func(i, j int) bool {
		if listed[i].Occurrence.Start != listed[j].Occurrence.Start {
			return listed[i].Occurrence.Start < listed[j].Occurrence.Start
		}
		return listed[i].Occurrence.Event.ID < listed[j].Occurrence.Event.ID
	})

	return textResult(renderEvents(listed, days, loc)), nil, nil
}

// ---------------------------------------------------------------- create_event

type createEventArgs struct {
	Summary     string `json:"summary" jsonschema:"Event title"`
	Start       string `json:"start" jsonschema:"Start time \"YYYY-MM-DD HH:MM\" (in the configured default timezone); with all_day: a \"YYYY-MM-DD\" date"`
	End         string `json:"end,omitempty" jsonschema:"End time \"YYYY-MM-DD HH:MM\"; with all_day: inclusive end date (optional, defaults to start). Required for timed events."`
	Description string `json:"description,omitempty" jsonschema:"Optional event description"`
	Location    string `json:"location,omitempty" jsonschema:"Optional event location"`
	AllDay      bool   `json:"all_day,omitempty" jsonschema:"All-day event (dates instead of times)"`
	Repeat      string `json:"repeat,omitempty" jsonschema:"Make the event recurring: \"daily\", \"weekly\", \"monthly\" or \"yearly\""`
	Every       int    `json:"every,omitempty" jsonschema:"Repeat interval, e.g. 2 = every second day/week/... (with repeat; default 1)"`
	Count       int    `json:"count,omitempty" jsonschema:"Number of occurrences, max 49 (with repeat; 0 = unlimited)"`
	Until       string `json:"until,omitempty" jsonschema:"Last day of the recurrence \"YYYY-MM-DD\" (with repeat)"`
	RRule       string `json:"rrule,omitempty" jsonschema:"Raw RRULE value (advanced; replaces the repeat/every/count/until args)"`
	Calendar    string `json:"calendar,omitempty" jsonschema:"Calendar ID or name (optional; default: the configured default calendar, else the first calendar)"`
}

// resolveCreateTimes resolves the create start/end times (same rules as the
// CLI). All-day: start is a date; end is an optional
// INCLUSIVE date (default = start), converted to the exclusive iCal end by
// +24h. Timed: end is required; both parse as wall times in tzName.
func resolveCreateTimes(startStr, endStr string, allDay bool, tzName string) (start, end time.Time, err error) {
	if allDay {
		start, err = front.ParseDate(startStr)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		end = start
		if endStr != "" {
			end, err = front.ParseDate(endStr)
			if err != nil {
				return time.Time{}, time.Time{}, err
			}
		}
		end = end.Add(24 * time.Hour) // exclusive iCal end
		if !end.After(start) {
			return time.Time{}, time.Time{}, errors.New("end date must not be before start date")
		}
		return start, end, nil
	}

	if endStr == "" {
		return time.Time{}, time.Time{}, errors.New("end is required for timed events")
	}
	start, err = front.ParseLocalDateTime(startStr, tzName)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err = front.ParseLocalDateTime(endStr, tzName)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start, end, nil
}

func (s *server) createEvent(ctx context.Context, _ *mcp.CallToolRequest, args createEventArgs) (*mcp.CallToolResult, any, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, nil, err
	}
	tz := sess.cfg.EffectiveTimezone()

	start, end, err := resolveCreateTimes(args.Start, args.End, args.AllDay, tz)
	if err != nil {
		return nil, nil, invalidArgs(err)
	}
	rec := front.RecurrenceFlags{Repeat: args.Repeat, Every: args.Every, Count: args.Count, Until: args.Until, RawRRule: args.RRule}
	rrule, err := rec.BuildRRule(tz, args.AllDay)
	if err != nil {
		return nil, nil, invalidArgs(err)
	}

	_, access, err := sess.access(ctx, args.Calendar)
	if err != nil {
		return nil, nil, err
	}

	raw, err := event.Create(ctx, sess.client, access, event.CreateOptions{
		Summary:     args.Summary,
		Description: args.Description,
		Location:    args.Location,
		Start:       start,
		End:         end,
		TZName:      tz,
		AllDay:      args.AllDay,
		RRule:       rrule,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("creating event: %w", err)
	}

	var when string
	if args.AllDay {
		when = start.UTC().Format("Mon 02 Jan")
	} else {
		when = start.Format("Mon 02 Jan 15:04") + " - " + end.Format("15:04")
	}
	id := "unknown"
	if raw != nil {
		id = raw.ID
	}
	out := fmt.Sprintf("Event created: %s\n  %s\n  ID: %s", args.Summary, when, id)
	if rrule != "" {
		out += "\n  Repeats: " + rrule
	}
	return textResult(out), nil, nil
}

// ---------------------------------------------------------------- update_event

type updateEventArgs struct {
	EventID     string `json:"event_id" jsonschema:"The event ID (get from list_events)"`
	Summary     string `json:"summary,omitempty" jsonschema:"New event title (leave empty to keep current)"`
	Start       string `json:"start,omitempty" jsonschema:"New start \"YYYY-MM-DD HH:MM\" in the default timezone (\"YYYY-MM-DD\" for all-day events)"`
	End         string `json:"end,omitempty" jsonschema:"New end \"YYYY-MM-DD HH:MM\" (\"YYYY-MM-DD\" for all-day events)"`
	Description string `json:"description,omitempty" jsonschema:"New description (leave empty to keep current)"`
	Location    string `json:"location,omitempty" jsonschema:"New location (leave empty to keep current)"`
	Occurrence  string `json:"occurrence,omitempty" jsonschema:"For recurring events - the ORIGINAL start of the one occurrence to edit (as shown by list_events); other fields then apply to just that occurrence"`
	Repeat      string `json:"repeat,omitempty" jsonschema:"New recurrence: \"daily\", \"weekly\", \"monthly\" or \"yearly\""`
	Every       int    `json:"every,omitempty" jsonschema:"Repeat interval (with repeat; default 1)"`
	Count       int    `json:"count,omitempty" jsonschema:"Number of occurrences, max 49 (with repeat; 0 = unlimited)"`
	Until       string `json:"until,omitempty" jsonschema:"Last day of the recurrence \"YYYY-MM-DD\" (with repeat)"`
	RRule       string `json:"rrule,omitempty" jsonschema:"Raw RRULE value (advanced; replaces repeat/every/count/until)"`
	NoRepeat    bool   `json:"no_repeat,omitempty" jsonschema:"Remove the recurrence from this event"`
	Calendar    string `json:"calendar,omitempty" jsonschema:"Calendar ID or name (optional; default: the configured default calendar, else the first calendar)"`
}

// validateUpdateArgs rejects conflicting update arguments: no_repeat
// conflicts with repeat/rrule, and occurrence conflicts with all recurrence
// args (edit the series instead).
func validateUpdateArgs(occurrence string, noRepeat bool, rec front.RecurrenceFlags) error {
	if noRepeat && (rec.Repeat != "" || rec.RawRRule != "") {
		return errors.New("no_repeat cannot be combined with repeat/rrule")
	}
	if occurrence != "" && (noRepeat || rec.Repeat != "" || rec.RawRRule != "") {
		return errors.New("recurrence args cannot be combined with occurrence (edit the series instead)")
	}
	return nil
}

func (s *server) updateEvent(ctx context.Context, _ *mcp.CallToolRequest, args updateEventArgs) (*mcp.CallToolResult, any, error) {
	rec := front.RecurrenceFlags{Repeat: args.Repeat, Every: args.Every, Count: args.Count, Until: args.Until, RawRRule: args.RRule}
	if err := validateUpdateArgs(args.Occurrence, args.NoRepeat, rec); err != nil {
		return nil, nil, err
	}

	sess, err := s.session(ctx)
	if err != nil {
		return nil, nil, err
	}
	tz := sess.cfg.EffectiveTimezone()

	// Empty string = keep current: pointers are built only for non-empty
	// values (unlike the CLI, which keys off flag presence).
	opts := event.UpdateOptions{ClearRRule: args.NoRepeat}
	if args.Summary != "" {
		opts.Summary = &args.Summary
	}
	if args.Description != "" {
		opts.Description = &args.Description
	}
	if args.Location != "" {
		opts.Location = &args.Location
	}
	if args.Start != "" {
		t, err := front.ParseWhen(args.Start, tz)
		if err != nil {
			return nil, nil, invalidArgs(err)
		}
		opts.Start = &t
	}
	if args.End != "" {
		t, err := front.ParseWhen(args.End, tz)
		if err != nil {
			return nil, nil, invalidArgs(err)
		}
		opts.End = &t
	}
	if opts.Start != nil || opts.End != nil {
		opts.TZName = tz
	}

	var occurrenceTS int64
	if args.Occurrence != "" {
		occurrenceTS, err = front.ParseOccurrence(args.Occurrence, tz)
		if err != nil {
			return nil, nil, invalidArgs(err)
		}
	}

	info, access, err := sess.access(ctx, args.Calendar)
	if err != nil {
		return nil, nil, err
	}

	// Recurrence args need the event's all-day-ness for the UNTIL form, so
	// fetch the raw row first - only in that case (the occurrence path
	// never builds an RRULE).
	if args.Occurrence == "" && !rec.Empty() {
		raw, err := event.Get(ctx, sess.client, info.ID, args.EventID)
		if err != nil {
			return nil, nil, fmt.Errorf("fetching event: %w", err)
		}
		rrule, err := rec.BuildRRule(tz, raw.IsAllDay())
		if err != nil {
			return nil, nil, invalidArgs(err)
		}
		if rrule != "" {
			opts.RRule = &rrule
		}
	}

	outcome, err := event.SmartUpdate(ctx, sess.client, access, args.EventID, opts, occurrenceTS)
	if err != nil {
		return nil, nil, fmt.Errorf("updating event: %w", err)
	}

	out := fmt.Sprintf("Event %s updated.", args.EventID)
	if outcome.EditedOccurrence {
		out = "Occurrence updated."
	}
	if outcome.RemovedExceptions > 0 {
		out += fmt.Sprintf(" Removed %d edited occurrence(s) invalidated by the series change.", outcome.RemovedExceptions)
	}
	return textResult(out), nil, nil
}

// ---------------------------------------------------------------- delete_event

type deleteEventArgs struct {
	EventID    string `json:"event_id" jsonschema:"The event ID (get from list_events)"`
	Occurrence string `json:"occurrence,omitempty" jsonschema:"For recurring events only - the ORIGINAL start of the one occurrence to delete (\"YYYY-MM-DD HH:MM\", or \"YYYY-MM-DD\" for all-day events), as shown by list_events"`
	Calendar   string `json:"calendar,omitempty" jsonschema:"Calendar ID or name (optional; default: the configured default calendar, else the first calendar)"`
}

func (s *server) deleteEvent(ctx context.Context, _ *mcp.CallToolRequest, args deleteEventArgs) (*mcp.CallToolResult, any, error) {
	sess, err := s.session(ctx)
	if err != nil {
		return nil, nil, err
	}
	tz := sess.cfg.EffectiveTimezone()

	var occurrenceTS int64
	if args.Occurrence != "" {
		occurrenceTS, err = front.ParseOccurrence(args.Occurrence, tz)
		if err != nil {
			return nil, nil, invalidArgs(err)
		}
	}

	_, access, err := sess.access(ctx, args.Calendar)
	if err != nil {
		return nil, nil, err
	}

	res, err := event.SmartDelete(ctx, sess.client, access, args.EventID, occurrenceTS)
	if err != nil {
		return nil, nil, fmt.Errorf("deleting event: %w", err)
	}
	return textResult(renderDeleteResult(res, args.EventID)), nil, nil
}
