package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
)

// textResult wraps a string as a successful text tool result.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
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
			"Times are shown in the configured default timezone unless tz overrides it. " +
			"Recurring events are expanded into their individual occurrences, marked \"(recurring)\"; " +
			"pass the shown ID plus the shown \"occurrence start\" value to update_event/delete_event to address one occurrence.",
	}, s.listEvents)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "create_event",
		Description: "Create a calendar event. " +
			"Timed events need start and end (\"YYYY-MM-DD HH:MM\" in the configured default timezone, or tz); " +
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
	svc, err := s.service()
	if err != nil {
		return nil, nil, err
	}
	cals, err := svc.Calendars(ctx)
	if err != nil {
		return nil, nil, err
	}
	return textResult(renderCalendars(cals, svc.DefaultCalendarSelector())), nil, nil
}

// ---------------------------------------------------------------- list_events

type listEventsArgs struct {
	Days     int    `json:"days,omitempty" jsonschema:"Number of days to look ahead (default 7)"`
	From     string `json:"from,omitempty" jsonschema:"Window start \"YYYY-MM-DD HH:MM\" or \"YYYY-MM-DD\" (optional; default: now; days counts from it)"`
	Calendar string `json:"calendar,omitempty" jsonschema:"Calendar ID or name (optional; default: the configured default calendar, else the first calendar)"`
	TZ       string `json:"tz,omitempty" jsonschema:"IANA timezone for queries and display (optional; default: the configured timezone)"`
}

func (s *server) listEvents(ctx context.Context, _ *mcp.CallToolRequest, args listEventsArgs) (*mcp.CallToolResult, any, error) {
	svc, err := s.service()
	if err != nil {
		return nil, nil, err
	}
	list, err := svc.ListEvents(ctx, calsvc.ListEventsInput{
		Days:     args.Days,
		From:     args.From,
		Calendar: args.Calendar,
		TZ:       args.TZ,
	})
	if err != nil {
		return nil, nil, err
	}
	return textResult(renderEvents(list.Items, list.Days, list.Location)), nil, nil
}

// ---------------------------------------------------------------- create_event

type createEventArgs struct {
	Summary     string `json:"summary" jsonschema:"Event title"`
	Start       string `json:"start" jsonschema:"Start time \"YYYY-MM-DD HH:MM\" (in the configured default timezone, or tz); with all_day: a \"YYYY-MM-DD\" date"`
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
	TZ          string `json:"tz,omitempty" jsonschema:"IANA timezone for the event times (optional; default: the configured timezone)"`
}

func (s *server) createEvent(ctx context.Context, _ *mcp.CallToolRequest, args createEventArgs) (*mcp.CallToolResult, any, error) {
	svc, err := s.service()
	if err != nil {
		return nil, nil, err
	}
	created, err := svc.CreateEvent(ctx, calsvc.CreateEventInput{
		Summary:     args.Summary,
		Description: args.Description,
		Location:    args.Location,
		Start:       args.Start,
		End:         args.End,
		AllDay:      args.AllDay,
		Recurrence: calsvc.Recurrence{
			Repeat: args.Repeat, Every: args.Every, Count: args.Count,
			Until: args.Until, RawRRule: args.RRule,
		},
		Calendar: args.Calendar,
		TZ:       args.TZ,
	})
	if err != nil {
		return nil, nil, err
	}
	return textResult(renderCreated(created)), nil, nil
}

// ---------------------------------------------------------------- update_event

type updateEventArgs struct {
	EventID     string `json:"event_id" jsonschema:"The event ID (get from list_events)"`
	Summary     string `json:"summary,omitempty" jsonschema:"New event title (leave empty to keep current)"`
	Start       string `json:"start,omitempty" jsonschema:"New start \"YYYY-MM-DD HH:MM\" (\"YYYY-MM-DD\" for all-day events)"`
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
	TZ          string `json:"tz,omitempty" jsonschema:"IANA timezone for the event times (optional; default: the configured timezone)"`
}

func (s *server) updateEvent(ctx context.Context, _ *mcp.CallToolRequest, args updateEventArgs) (*mcp.CallToolResult, any, error) {
	svc, err := s.service()
	if err != nil {
		return nil, nil, err
	}

	// Non-empty = change: pointers are built only for non-empty values
	// (unlike the CLI, which keys off flag presence and can clear fields).
	in := calsvc.UpdateEventInput{
		EventID:    args.EventID,
		Start:      args.Start,
		End:        args.End,
		Occurrence: args.Occurrence,
		NoRepeat:   args.NoRepeat,
		Recurrence: calsvc.Recurrence{
			Repeat: args.Repeat, Every: args.Every, Count: args.Count,
			Until: args.Until, RawRRule: args.RRule,
		},
		Calendar: args.Calendar,
		TZ:       args.TZ,
	}
	if args.Summary != "" {
		in.Summary = &args.Summary
	}
	if args.Description != "" {
		in.Description = &args.Description
	}
	if args.Location != "" {
		in.Location = &args.Location
	}

	outcome, err := svc.UpdateEvent(ctx, in)
	if err != nil {
		return nil, nil, err
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
	TZ         string `json:"tz,omitempty" jsonschema:"IANA timezone for occurrence (optional; default: the configured timezone)"`
}

func (s *server) deleteEvent(ctx context.Context, _ *mcp.CallToolRequest, args deleteEventArgs) (*mcp.CallToolResult, any, error) {
	svc, err := s.service()
	if err != nil {
		return nil, nil, err
	}
	res, err := svc.DeleteEvent(ctx, calsvc.DeleteEventInput{
		EventID:    args.EventID,
		Occurrence: args.Occurrence,
		Calendar:   args.Calendar,
		TZ:         args.TZ,
	})
	if err != nil {
		return nil, nil, err
	}
	return textResult(renderDeleteResult(res, args.EventID)), nil, nil
}
