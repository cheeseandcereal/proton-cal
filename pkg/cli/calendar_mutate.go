package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/cheeseandcereal/proton-cal/pkg/auth"
	"github.com/cheeseandcereal/proton-cal/pkg/caljson"
	"github.com/cheeseandcereal/proton-cal/pkg/calsvc"
	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/reminders"
)

func newUpdateCalendarCmd() *cobra.Command {
	var (
		name        string
		description string
		color       string
		duration    int
		makesBusy   bool
		makeDefault bool
		partDay     []string
		fullDay     []string
	)

	cmd := &cobra.Command{
		Use:   "calendar [SELECTOR]",
		Short: "Update a calendar's name, color, default settings, or default status",
		Long: "Update an owned calendar (by ID or name; default: the account's default,\n" +
			"else the first). Only specified fields change. Subscribed and holidays\n" +
			"calendars cannot be modified.\n\n" +
			"Default reminders (--reminder / --full-day-reminder) are repeatable and\n" +
			"replace the calendar's corresponding default set; pass an empty value\n" +
			"(e.g. --reminder \"\") to clear that set.",
		Args: requireArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			var in calsvc.UpdateCalendarInput
			if len(args) == 1 {
				in.Selector = args[0]
			}

			// Flag presence drives intent (empty string is meaningful for
			// description and for clearing reminder sets).
			if cmd.Flags().Changed("name") {
				in.Name = &name
			}
			if cmd.Flags().Changed("description") {
				in.Description = &description
			}
			if cmd.Flags().Changed("color") {
				in.Color = &color
			}
			if cmd.Flags().Changed("default-duration") {
				in.DefaultDuration = &duration
			}
			if cmd.Flags().Changed("makes-busy") {
				in.MakesUserBusy = &makesBusy
			}
			if cmd.Flags().Changed("reminder") {
				ns, err := parseReminderSet(partDay)
				if err != nil {
					return err
				}
				in.PartDayReminders = &ns
			}
			if cmd.Flags().Changed("full-day-reminder") {
				ns, err := parseReminderSet(fullDay)
				if err != nil {
					return err
				}
				in.FullDayReminders = &ns
			}
			in.MakeDefault = makeDefault

			svc, err := serviceFactory()
			if err != nil {
				return err
			}
			defer svc.Close()

			got, err := svc.UpdateCalendar(cmd.Context(), in)
			if err != nil {
				return err
			}

			if outputJSON() {
				return printJSON(caljson.CalendarDetailOf(got.Info, got.Settings, got.IsDefault))
			}
			w := humanOut()
			fmt.Fprintln(w, "Calendar updated.")
			sel, _ := selectFields(calendarFieldRegistry, nil, true)
			for _, line := range calendarDetailLines(got.Info, got.Settings, got.IsDefault, sel) {
				fmt.Fprintln(w, line)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "new calendar name")
	cmd.Flags().StringVar(&description, "description", "", "new description (empty clears it)")
	cmd.Flags().StringVar(&color, "color", "", "new color: a Proton color name (e.g. strawberry) or its hex")
	cmd.Flags().IntVar(&duration, "default-duration", 0, "default event duration in minutes")
	cmd.Flags().BoolVar(&makesBusy, "makes-busy", false,
		"whether events on this calendar mark you busy; pass --makes-busy=false to turn it off (omit to leave unchanged)")
	cmd.Flags().BoolVar(&makeDefault, "make-default", false, "make this the account's default calendar")
	cmd.Flags().StringArrayVar(&partDay, "reminder", nil,
		"replace timed-event default reminders, repeatable: 15m, 1h30m, 2d, 1w (prefix email:; default notify)")
	cmd.Flags().StringArrayVar(&fullDay, "full-day-reminder", nil,
		"replace all-day default reminders, repeatable (same syntax as --reminder)")
	return cmd
}

// parseReminderSet turns repeatable reminder flag values into a notification
// set; an all-empty input clears it (empty, non-nil slice).
func parseReminderSet(values []string) ([]caltypes.Notification, error) {
	nonEmpty := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" {
			nonEmpty = append(nonEmpty, v)
		}
	}
	if len(nonEmpty) == 0 {
		return []caltypes.Notification{}, nil
	}
	return reminders.ParseList(nonEmpty)
}

func newDeleteCalendarCmd() *cobra.Command {
	var (
		yes      bool
		password string
	)

	cmd := &cobra.Command{
		Use:   "calendar SELECTOR",
		Short: "Delete a calendar",
		Long: "Delete a calendar (by ID or name).\n\n" +
			"Deleting an OWNED calendar is irreversible and requires re-entering your\n" +
			"login password (prompted interactively, or via --password for\n" +
			"non-interactive use). Holidays calendars are removed without a password.\n" +
			"Subscribed calendars cannot be deleted here (unsubscribe in the app).",
		Args: requireArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc, err := serviceFactory()
			if err != nil {
				return err
			}
			defer svc.Close()

			// Resolve first so a missing --yes can refuse with the exact
			// calendar that WOULD be deleted, guarding against the wrong one.
			info, err := svc.ResolveCalendarInfo(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !yes {
				return fmt.Errorf(
					"refusing to delete calendar %q (%s, ID %s) without confirmation; re-run with --yes to delete it",
					info.Name, info.TypeString(), info.ID)
			}

			// Only owned (normal) calendars require the login password.
			if info.Type == 0 && password == "" {
				password, err = promptDeletePassword()
				if err != nil {
					return err
				}
			}

			if err := svc.DeleteCalendar(cmd.Context(), calsvc.DeleteCalendarInput{
				Selector: info.ID, // use the resolved ID, not the raw selector
				Password: password,
			}); err != nil {
				return err
			}

			if outputJSON() {
				return printJSON(map[string]any{"deleted": true, "id": info.ID, "name": info.Name})
			}
			fmt.Fprintf(humanOut(), "Calendar %q deleted.\n", info.Name)
			return nil
		},
	}

	cmd.Flags().BoolVar(&yes, "yes", false,
		"confirm deletion; without it the command resolves and reports the target calendar, then refuses")
	cmd.Flags().StringVar(&password, "password", "",
		"login password for deleting an owned calendar (prompted if omitted on a terminal; exposed in shell history/process list when passed)")
	return cmd
}

// promptDeletePassword reads the login password from the terminal (hidden);
// errors when stdin is not a terminal so callers must pass --password.
func promptDeletePassword() (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("deleting an owned calendar requires the login password: pass --password (no terminal available to prompt)")
	}
	return auth.NewTerminalPrompter().PromptSecret("Login password")
}
