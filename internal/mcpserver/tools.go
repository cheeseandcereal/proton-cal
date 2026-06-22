package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cheeseandcereal/proton-cal/internal/calcolor"
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
			in.Color = &calsvc.ColorUpdate{Inherit: true}
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
		Description: "Get a single calendar in detail by ID or name (default: the account's " +
			"default calendar, else the first): name, type, color, the calendar's default " +
			"reminders (timed and all-day) and default event duration.",
	}, s.getCalendar)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_event",
		Description: "Get a single calendar event in full detail by its ID: " +
			"summary, times, location, description, organizer, attendees with their " +
			"RSVP status, video conferencing (Proton Meet/Zoom) link, color and reminders. " +
			"Set format to \"ics\" to instead return the event as a raw iCalendar (.ics) document. " +
			"For a recurring event, \"ics\" returns the WHOLE series (master VEVENT plus a " +
			"VEVENT per edited occurrence) unless no_series is true, which returns only the " +
			"single addressed VEVENT.",
	}, s.getEvent)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "create_event",
		Description: "Create a calendar event. " +
			"Timed events take start (\"YYYY-MM-DD HH:MM\" in the configured default timezone, or tz); end is optional and defaults to the calendar's default duration; " +
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

	mcp.AddTool(srv, &mcp.Tool{
		Name: "update_calendar",
		Description: "Update an owned calendar (by ID or name; default: the account's " +
			"default, else the first). Change its name, description, color, default " +
			"event duration, busy setting, default reminder sets, and/or make it the " +
			"account default. Only provided fields change. Subscribed and holidays " +
			"calendars cannot be modified.",
	}, s.updateCalendar)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "delete_calendar",
		Description: "Delete a calendar by ID or name. Requires confirm=true. Deleting an " +
			"OWNED calendar is irreversible and requires the account login password " +
			"(pass it as password); holidays calendars need no password; subscribed " +
			"calendars cannot be deleted here. The password is used only for the " +
			"deletion handshake and is never stored or echoed.",
	}, s.deleteCalendar)
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
	// Best-effort: a failure to read the server default just leaves no
	// calendar marked default.
	defaultID, _ := svc.DefaultCalendarID(ctx)
	return textResult(renderCalendars(cals, defaultID)), caljson.Calendars(cals, defaultID), nil
}

// ---------------------------------------------------------------- get_calendar

type getCalendarArgs struct {
	Calendar string `json:"calendar,omitempty" jsonschema:"Calendar ID or name (optional; default: the account's default calendar, else the first calendar)"`
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
	return textResult(text), caljson.CalendarDetailOf(got.Info, got.Settings, got.IsDefault), nil
}

// ---------------------------------------------------------------- list_events

type listEventsArgs struct {
	Days     int    `json:"days,omitempty" jsonschema:"Number of days to look ahead (default 7)"`
	From     string `json:"from,omitempty" jsonschema:"Window start \"YYYY-MM-DD HH:MM\" or \"YYYY-MM-DD\" (optional; default: now; days counts from it)"`
	Calendar string `json:"calendar,omitempty" jsonschema:"Calendar ID or name (optional; default: the account's default calendar, else the first calendar)"`
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
	Calendar string `json:"calendar,omitempty" jsonschema:"Calendar ID or name the event lives in (optional; default: the account's default calendar, else the first calendar)"`
	TZ       string `json:"tz,omitempty" jsonschema:"IANA timezone for display (optional; default: the configured timezone)"`
	Format   string `json:"format,omitempty" jsonschema:"Output format: \"detail\" (default, human-readable) or \"ics\" (raw iCalendar document)"`
	NoSeries bool   `json:"no_series,omitempty" jsonschema:"With format \"ics\" on a recurring event, export only the single addressed VEVENT instead of the whole series (master + edited occurrences). Ignored for non-recurring events."`
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
		NoSeries: args.NoSeries,
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
	End         string   `json:"end,omitempty" jsonschema:"End time \"YYYY-MM-DD HH:MM\"; with all_day: inclusive end date (optional, defaulting to a single day). For timed events, defaults to the calendar's default duration (an explicit end is required only if the calendar defines no default)."`
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
	Color       string   `json:"color,omitempty" jsonschema:"Event color: a Proton color name (e.g. \"strawberry\", \"pacific\") or its hex (optional; default: the calendar color). Only Proton's fixed palette is accepted; \"default\" also means the calendar color."`
	Calendar    string   `json:"calendar,omitempty" jsonschema:"Calendar ID or name (optional; default: the account's default calendar, else the first calendar)"`
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
	// On create, "default" (or empty) means inherit the calendar color, which
	// a create with no color does naturally.
	color := ""
	if args.Color != "" && !calcolor.IsDefault(args.Color) {
		if color, err = calcolor.Resolve(args.Color); err != nil {
			return nil, nil, err
		}
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
		Color:        color,
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
	Color         string   `json:"color,omitempty" jsonschema:"Set the event color: a Proton color name (e.g. \"strawberry\") or its hex (only Proton's fixed palette). Pass \"default\" to revert to the calendar color."`
	ClearFields   []string `json:"clear_fields,omitempty" jsonschema:"Fields to clear: any of \"summary\", \"description\", \"location\" (set empty) or \"color\" (revert to the calendar color; equivalent to color=\"default\"). Use this instead of passing an empty string, which is treated as \"keep current\"."`
	Calendar      string   `json:"calendar,omitempty" jsonschema:"Calendar ID or name (optional; default: the account's default calendar, else the first calendar)"`
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
		if calcolor.IsDefault(args.Color) {
			in.Color = &calsvc.ColorUpdate{Inherit: true}
		} else {
			hex, cerr := calcolor.Resolve(args.Color)
			if cerr != nil {
				return nil, nil, cerr
			}
			in.Color = &calsvc.ColorUpdate{Hex: hex}
		}
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
	Calendar   string `json:"calendar,omitempty" jsonschema:"Calendar ID or name (optional; default: the account's default calendar, else the first calendar)"`
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

// ---------------------------------------------------------------- update_calendar

type updateCalendarArgs struct {
	Calendar            string   `json:"calendar,omitempty" jsonschema:"Calendar ID or name (optional; default: the account's default calendar, else the first calendar)"`
	Name                string   `json:"name,omitempty" jsonschema:"New calendar name (optional)"`
	Description         string   `json:"description,omitempty" jsonschema:"New description (optional; use clear_description to set it empty)"`
	ClearDescription    bool     `json:"clear_description,omitempty" jsonschema:"Set the description to empty (use instead of an empty description, which is treated as 'keep')"`
	Color               string   `json:"color,omitempty" jsonschema:"New color: a Proton color name (e.g. \"strawberry\") or its hex (only Proton's fixed palette). A calendar has no inheritable default color, so \"default\" is not accepted."`
	DefaultDuration     int      `json:"default_duration,omitempty" jsonschema:"Default event duration in minutes (optional; >0)"`
	MakesBusy           *bool    `json:"makes_busy,omitempty" jsonschema:"Whether events on this calendar mark you busy (optional)"`
	Reminders           []string `json:"reminders,omitempty" jsonschema:"Replace the timed-event default reminders, e.g. [\"15m\",\"email:1h\"] (prefix \"email:\"; default a notification). An empty list clears them. Omit to keep."`
	SetReminders        bool     `json:"set_reminders,omitempty" jsonschema:"Set true to apply the reminders list (including an empty list to clear); needed because an omitted list is indistinguishable from an empty one over JSON."`
	FullDayReminders    []string `json:"full_day_reminders,omitempty" jsonschema:"Replace the all-day default reminders (same syntax as reminders). An empty list clears them. Omit to keep."`
	SetFullDayReminders bool     `json:"set_full_day_reminders,omitempty" jsonschema:"Set true to apply the full_day_reminders list (including an empty list to clear)."`
	MakeDefault         bool     `json:"make_default,omitempty" jsonschema:"Make this the account's default calendar"`
}

func (s *server) updateCalendar(ctx context.Context, _ *mcp.CallToolRequest, args updateCalendarArgs) (*mcp.CallToolResult, any, error) {
	svc, err := s.service()
	if err != nil {
		return nil, nil, err
	}

	in := calsvc.UpdateCalendarInput{Selector: args.Calendar, MakeDefault: args.MakeDefault}
	if args.Name != "" {
		in.Name = &args.Name
	}
	switch {
	case args.ClearDescription:
		empty := ""
		in.Description = &empty
	case args.Description != "":
		in.Description = &args.Description
	}
	if args.Color != "" {
		in.Color = &args.Color // validated (incl. rejecting "default") in the service
	}
	if args.DefaultDuration != 0 {
		in.DefaultDuration = &args.DefaultDuration
	}
	if args.MakesBusy != nil {
		in.MakesUserBusy = args.MakesBusy
	}
	if args.SetReminders {
		ns, err := parseCalendarReminders(args.Reminders)
		if err != nil {
			return nil, nil, err
		}
		in.PartDayReminders = &ns
	}
	if args.SetFullDayReminders {
		ns, err := parseCalendarReminders(args.FullDayReminders)
		if err != nil {
			return nil, nil, err
		}
		in.FullDayReminders = &ns
	}

	got, err := svc.UpdateCalendar(ctx, in)
	if err != nil {
		return nil, nil, err
	}
	return textResult(renderCalendarDetail(got.Info, got.Settings, got.IsDefault)),
		caljson.CalendarDetailOf(got.Info, got.Settings, got.IsDefault), nil
}

// parseCalendarReminders turns reminder specs into a notification set; an
// empty input clears the set (non-nil empty slice).
func parseCalendarReminders(specs []string) ([]caltypes.Notification, error) {
	nonEmpty := make([]string, 0, len(specs))
	for _, s := range specs {
		if s != "" {
			nonEmpty = append(nonEmpty, s)
		}
	}
	if len(nonEmpty) == 0 {
		return []caltypes.Notification{}, nil
	}
	return reminders.ParseList(nonEmpty)
}

// ---------------------------------------------------------------- delete_calendar

type deleteCalendarArgs struct {
	Calendar string `json:"calendar" jsonschema:"Calendar ID or name to delete"`
	Confirm  bool   `json:"confirm" jsonschema:"Must be true to actually delete (a safety guard)"`
	Password string `json:"password,omitempty" jsonschema:"Account login password, required to delete an OWNED calendar (not needed for holidays calendars). Used only for the deletion handshake; never stored."`
}

func (s *server) deleteCalendar(ctx context.Context, _ *mcp.CallToolRequest, args deleteCalendarArgs) (*mcp.CallToolResult, any, error) {
	svc, err := s.service()
	if err != nil {
		return nil, nil, err
	}

	// Dry run: resolve the target first so confirm=false refuses while naming
	// the exact calendar that WOULD be deleted (guards against the wrong one).
	info, err := svc.ResolveCalendarInfo(ctx, args.Calendar)
	if err != nil {
		return nil, nil, err
	}
	if !args.Confirm {
		return nil, nil, fmt.Errorf(
			"refusing to delete calendar %q (%s, ID %s) without confirm=true; set confirm=true to delete it",
			info.Name, info.TypeString(), info.ID)
	}

	// Owned calendars need the password; surface a clear error if it is
	// required but absent (rather than failing deep in the scope handshake).
	if info.Type == 0 && args.Password == "" {
		return nil, nil, fmt.Errorf("deleting owned calendar %q requires the account login password (pass it as password)", info.Name)
	}

	if err := svc.DeleteCalendar(ctx, calsvc.DeleteCalendarInput{
		Selector: info.ID,
		Password: args.Password,
	}); err != nil {
		return nil, nil, err
	}
	return textResult(fmt.Sprintf("Calendar %q deleted.", info.Name)),
		map[string]any{"deleted": true, "id": info.ID, "name": info.Name}, nil
}
