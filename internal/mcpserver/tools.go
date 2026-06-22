package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cheeseandcereal/proton-cal/internal/caljson"
	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/eventview"
	"github.com/cheeseandcereal/proton-cal/internal/reminders"
)

// textResult wraps a string as a successful text tool result.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// emptyString is the addressable empty value clear_fields points the update
// input at (a non-nil "" pointer clears the field; see calsvc.UpdateEventInput).
var emptyString string

// applyClearFields sets the named fields to "clear" on the update input. JSON
// args can't express "explicitly empty", so clearing is opt-in here; the text
// fields map to a non-nil empty-string pointer, and "color" reverts to the
// calendar color. Unknown names error.
func applyClearFields(in *calsvc.UpdateEventInput, fields []string) error {
	for _, f := range fields {
		switch f {
		case "summary":
			in.Summary = &emptyString
		case "description":
			in.Description = &emptyString
		case "location":
			in.Location = &emptyString
		case "color":
			in.Color = &event.ColorUpdate{Value: ""}
		default:
			return fmt.Errorf("clear_fields: unknown field %q (valid: summary, description, location, color)", f)
		}
	}
	return nil
}

// reminderModeNone/Inherit/Custom select the update reminder tri-state.
const (
	reminderModeKeep    = ""
	reminderModeInherit = "inherit"
	reminderModeNone    = "none"
	reminderModeCustom  = "custom"
)

// resolveUpdateReminders turns the reminders_mode arg plus the reminders list
// into an *event.RemindersUpdate (nil = keep). An empty mode with a non-empty
// list is treated as "custom" for convenience.
func resolveUpdateReminders(mode string, specs []string) (*event.RemindersUpdate, error) {
	if mode == reminderModeKeep && len(specs) > 0 {
		mode = reminderModeCustom
	}
	switch mode {
	case reminderModeKeep:
		return nil, nil
	case reminderModeInherit:
		return &event.RemindersUpdate{Inherit: true}, nil
	case reminderModeNone:
		return &event.RemindersUpdate{List: nil}, nil
	case reminderModeCustom:
		list, err := reminders.ParseList(specs)
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, errors.New("reminders_mode \"custom\" requires at least one entry in reminders")
		}
		return &event.RemindersUpdate{List: list}, nil
	default:
		return nil, fmt.Errorf("invalid reminders_mode %q (keep, inherit, none or custom)", mode)
	}
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
		Name: "get_calendar",
		Description: "Get a single calendar in detail by ID or name (default: the configured " +
			"default calendar, else the first): name, type, color, the calendar's default " +
			"reminders (timed and all-day) and default event duration.",
	}, s.getCalendar)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_event",
		Description: "Get a single calendar event in full detail by its ID: " +
			"summary, times, location, description, organizer, attendees with their " +
			"RSVP status, video conferencing (Proton Meet/Zoom) link, color and reminders. " +
			"Set format to \"ics\" to instead return the event as a raw iCalendar (.ics) document.",
	}, s.getEvent)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "create_event",
		Description: "Create a calendar event. " +
			"Timed events need start and end (\"YYYY-MM-DD HH:MM\" in the configured default timezone, or tz); " +
			"all-day events use dates (\"YYYY-MM-DD\") and end is the inclusive last day (default: start). " +
			"Use repeat/every/count/until (or a raw rrule) to make it recurring.",
	}, s.createEvent)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "update_event",
		Description: "Update an existing calendar event. Only non-empty fields are changed; " +
			"to blank out summary/description/location, list them in clear_fields. " +
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
	defaultSel := svc.DefaultCalendarSelector()
	return textResult(renderCalendars(cals, defaultSel)), caljson.Calendars(cals, defaultSel), nil
}

// ---------------------------------------------------------------- get_calendar

type getCalendarArgs struct {
	Calendar string `json:"calendar,omitempty" jsonschema:"Calendar ID or name (optional; default: the configured default calendar, else the first calendar)"`
}

func (s *server) getCalendar(ctx context.Context, _ *mcp.CallToolRequest, args getCalendarArgs) (*mcp.CallToolResult, any, error) {
	svc, err := s.service()
	if err != nil {
		return nil, nil, err
	}
	got, err := svc.GetCalendar(ctx, args.Calendar)
	if err != nil {
		return nil, nil, err
	}
	text := renderCalendarDetail(got.Info, got.Settings, got.IsDefault)
	return textResult(text), caljson.CalendarOf(got.Info, got.IsDefault), nil
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
	rows := make([]caljson.Event, 0, len(list.Items))
	for _, l := range list.Items {
		rows = append(rows, caljson.Occurrence(l, list.Location, list.Settings, list.Calendar))
	}
	text := renderEvents(list.Items, list.Days, list.Location, list.Settings, list.Calendar)
	return textResult(text), rows, nil
}

// ---------------------------------------------------------------- get_event

type getEventArgs struct {
	EventID  string `json:"event_id" jsonschema:"The event ID (get from list_events)"`
	Calendar string `json:"calendar,omitempty" jsonschema:"Calendar ID or name the event lives in (optional; default: the configured default calendar, else the first calendar)"`
	TZ       string `json:"tz,omitempty" jsonschema:"IANA timezone for display (optional; default: the configured timezone)"`
	Format   string `json:"format,omitempty" jsonschema:"Output format: \"detail\" (default, human-readable) or \"ics\" (raw iCalendar document)"`
}

func (s *server) getEvent(ctx context.Context, _ *mcp.CallToolRequest, args getEventArgs) (*mcp.CallToolResult, any, error) {
	svc, err := s.service()
	if err != nil {
		return nil, nil, err
	}
	ics := args.Format == "ics"
	got, err := svc.GetEvent(ctx, calsvc.GetEventInput{
		EventID:  args.EventID,
		Calendar: args.Calendar,
		TZ:       args.TZ,
		WithICS:  ics,
	})
	if err != nil {
		return nil, nil, err
	}
	if ics {
		if got.ICS == "" {
			return nil, nil, calsvc.ErrICSUndecryptable
		}
		return textResult(got.ICS), nil, nil
	}
	text := renderEventDetail(got.Event, got.Location, got.Settings, got.Calendar)
	return textResult(text), caljson.EventDetail(got.Event, got.Location, got.Settings, got.Calendar), nil
}

// ---------------------------------------------------------------- create_event

type createEventArgs struct {
	Summary     string   `json:"summary" jsonschema:"Event title"`
	Start       string   `json:"start" jsonschema:"Start time \"YYYY-MM-DD HH:MM\" (in the configured default timezone, or tz); with all_day: a \"YYYY-MM-DD\" date"`
	End         string   `json:"end,omitempty" jsonschema:"End time \"YYYY-MM-DD HH:MM\"; with all_day: inclusive end date (optional, defaults to start). Required for timed events."`
	Description string   `json:"description,omitempty" jsonschema:"Optional event description"`
	Location    string   `json:"location,omitempty" jsonschema:"Optional event location"`
	AllDay      bool     `json:"all_day,omitempty" jsonschema:"All-day event (dates instead of times)"`
	Repeat      string   `json:"repeat,omitempty" jsonschema:"Make the event recurring: \"daily\", \"weekly\", \"monthly\" or \"yearly\""`
	Every       int      `json:"every,omitempty" jsonschema:"Repeat interval, e.g. 2 = every second day/week/... (with repeat; default 1)"`
	Count       int      `json:"count,omitempty" jsonschema:"Number of occurrences, max 49 (with repeat; 0 = unlimited)"`
	Until       string   `json:"until,omitempty" jsonschema:"Last day of the recurrence \"YYYY-MM-DD\" (with repeat)"`
	RRule       string   `json:"rrule,omitempty" jsonschema:"Raw RRULE value (advanced; replaces the repeat/every/count/until args)"`
	Reminders   []string `json:"reminders,omitempty" jsonschema:"Reminders before the event, e.g. [\"15m\",\"1h30m\",\"2d\"]; prefix \"email:\" for an email reminder (default a notification). Raw iCal triggers like \"-PT15M\" also accepted. Omit to inherit the calendar default; pass no_reminders to set none."`
	NoReminders bool     `json:"no_reminders,omitempty" jsonschema:"Create the event with no reminders (overrides the calendar default)"`
	Color       string   `json:"color,omitempty" jsonschema:"Event color \"#RRGGBB\" (optional; default: the calendar color)"`
	Calendar    string   `json:"calendar,omitempty" jsonschema:"Calendar ID or name (optional; default: the configured default calendar, else the first calendar)"`
	TZ          string   `json:"tz,omitempty" jsonschema:"IANA timezone for the event times (optional; default: the configured timezone)"`
}

func (s *server) createEvent(ctx context.Context, _ *mcp.CallToolRequest, args createEventArgs) (*mcp.CallToolResult, any, error) {
	svc, err := s.service()
	if err != nil {
		return nil, nil, err
	}
	if len(args.Reminders) > 0 && args.NoReminders {
		return nil, nil, errors.New("reminders and no_reminders are mutually exclusive")
	}
	var remList []caltypes.Notification
	remSet := args.NoReminders
	if len(args.Reminders) > 0 {
		remList, err = reminders.ParseList(args.Reminders)
		if err != nil {
			return nil, nil, err
		}
		remSet = true
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
		Reminders:    remList,
		RemindersSet: remSet,
		Color:        args.Color,
		Calendar:     args.Calendar,
		TZ:           args.TZ,
	})
	if err != nil {
		return nil, nil, err
	}
	return textResult(renderCreated(created)), caljson.CreatedOf(created), nil
}

// ---------------------------------------------------------------- update_event

type updateEventArgs struct {
	EventID       string   `json:"event_id" jsonschema:"The event ID (get from list_events)"`
	Summary       string   `json:"summary,omitempty" jsonschema:"New event title (leave empty to keep current)"`
	Start         string   `json:"start,omitempty" jsonschema:"New start \"YYYY-MM-DD HH:MM\" (\"YYYY-MM-DD\" for all-day events)"`
	End           string   `json:"end,omitempty" jsonschema:"New end \"YYYY-MM-DD HH:MM\" (\"YYYY-MM-DD\" for all-day events)"`
	Description   string   `json:"description,omitempty" jsonschema:"New description (leave empty to keep current)"`
	Location      string   `json:"location,omitempty" jsonschema:"New location (leave empty to keep current)"`
	Occurrence    string   `json:"occurrence,omitempty" jsonschema:"For recurring events - the ORIGINAL start of the one occurrence to edit (as shown by list_events); other fields then apply to just that occurrence"`
	Repeat        string   `json:"repeat,omitempty" jsonschema:"New recurrence: \"daily\", \"weekly\", \"monthly\" or \"yearly\""`
	Every         int      `json:"every,omitempty" jsonschema:"Repeat interval (with repeat; default 1)"`
	Count         int      `json:"count,omitempty" jsonschema:"Number of occurrences, max 49 (with repeat; 0 = unlimited)"`
	Until         string   `json:"until,omitempty" jsonschema:"Last day of the recurrence \"YYYY-MM-DD\" (with repeat)"`
	RRule         string   `json:"rrule,omitempty" jsonschema:"Raw RRULE value (advanced; replaces repeat/every/count/until)"`
	NoRepeat      bool     `json:"no_repeat,omitempty" jsonschema:"Remove the recurrence from this event"`
	Reminders     []string `json:"reminders,omitempty" jsonschema:"New reminders, e.g. [\"15m\",\"email:1h\"] (prefix \"email:\"; default a notification). Setting this implies reminders_mode=custom. Raw iCal triggers like \"-PT15M\" also accepted."`
	RemindersMode string   `json:"reminders_mode,omitempty" jsonschema:"How to change reminders: \"keep\" (default), \"inherit\" (calendar default), \"none\" (remove all), or \"custom\" (use the reminders list)."`
	Color         string   `json:"color,omitempty" jsonschema:"Set the event color \"#RRGGBB\". To revert to the calendar color, pass \"color\" in clear_fields instead."`
	ClearFields   []string `json:"clear_fields,omitempty" jsonschema:"Fields to clear: any of \"summary\", \"description\", \"location\" (set empty) or \"color\" (revert to the calendar color). Use this instead of passing an empty string, which is treated as \"keep current\"."`
	Calendar      string   `json:"calendar,omitempty" jsonschema:"Calendar ID or name (optional; default: the configured default calendar, else the first calendar)"`
	TZ            string   `json:"tz,omitempty" jsonschema:"IANA timezone for the event times (optional; default: the configured timezone)"`
}

func (s *server) updateEvent(ctx context.Context, _ *mcp.CallToolRequest, args updateEventArgs) (*mcp.CallToolResult, any, error) {
	svc, err := s.service()
	if err != nil {
		return nil, nil, err
	}

	// Non-empty = change: pointers are built only for non-empty values, since
	// JSON args cannot distinguish "absent" from "empty". Explicit clearing
	// goes through clear_fields below (the CLI keys off flag presence instead).
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
	if args.Color != "" {
		in.Color = &event.ColorUpdate{Value: args.Color}
	}
	rem, err := resolveUpdateReminders(args.RemindersMode, args.Reminders)
	if err != nil {
		return nil, nil, err
	}
	in.Reminders = rem
	// clear_fields (incl. "color") is applied after the color arg so an
	// explicit clear wins over an accidental empty color.
	if err := applyClearFields(&in, args.ClearFields); err != nil {
		return nil, nil, err
	}

	outcome, err := svc.UpdateEvent(ctx, in)
	if err != nil {
		return nil, nil, err
	}

	headline, note := eventview.UpdateOutcomeMessage(outcome)
	out := headline
	if note != "" {
		out += " " + note
	}
	return textResult(out), caljson.UpdatedOf(outcome), nil
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
	return textResult(renderDeleteResult(res, args.EventID)), res, nil
}
