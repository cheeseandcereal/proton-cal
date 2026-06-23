package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/pkg/calcolor"
	"github.com/cheeseandcereal/proton-cal/pkg/caljson"
	"github.com/cheeseandcereal/proton-cal/pkg/calsvc"
	"github.com/cheeseandcereal/proton-cal/pkg/eventview"
)

// newCreateCmd is the parent "create" command grouping resource creators.
func newCreateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a resource (event, calendar)",
		Args:  requireArgs(cobra.NoArgs),
		RunE: func(_ *cobra.Command, _ []string) error {
			return errMissingSubcommand
		},
	}
	cmd.AddCommand(newCreateEventCmd())
	cmd.AddCommand(newCreateCalendarCmd())
	return cmd
}

func newCreateEventCmd() *cobra.Command {
	var (
		in calsvc.CreateEventInput
		rc reminderColorFlags
	)

	cmd := &cobra.Command{
		Use:   "event SUMMARY",
		Short: "Create a new calendar event (optionally recurring or all-day)",
		Args:  requireArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.Summary = args[0]

			if err := rc.validateExclusive(cmd); err != nil {
				return err
			}
			reminders, set, err := rc.createReminders()
			if err != nil {
				return err
			}
			color, err := rc.createColor()
			if err != nil {
				return err
			}
			in.Reminders, in.RemindersSet, in.Color = reminders, set, color

			svc, err := serviceFactory()
			if err != nil {
				return err
			}
			defer svc.Close()

			created, err := svc.CreateEvent(cmd.Context(), in)
			if err != nil {
				return err
			}

			if outputJSON() {
				return printJSON(caljson.CreatedOf(created))
			}

			w := humanOut()
			fmt.Fprintln(w, "Event created.")
			if created.ID != "" {
				fmt.Fprintf(w, "  ID: %s\n", created.ID)
			}
			if created.RRule != "" {
				fmt.Fprintf(w, "  Repeats: %s\n", created.RRule)
			}
			if created.Color != "" {
				fmt.Fprintf(w, "  Color: %s%s\n", swatch(created.Color), calcolor.Label(created.Color))
			}
			for _, n := range created.Reminders {
				fmt.Fprintf(w, "  Reminder (%s): %s\n", eventview.ReminderKind(n.Type), n.Trigger)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&in.Start, "start", "", "start time, YYYY-MM-DD HH:MM (with --all-day: YYYY-MM-DD)")
	cmd.Flags().StringVar(&in.End, "end", "", "end time, YYYY-MM-DD HH:MM (timed: defaults to the calendar's default duration; with --all-day: inclusive end date, defaulting to a single day)")
	cmd.Flags().StringVar(&in.Description, "description", "", "event description")
	cmd.Flags().StringVar(&in.Location, "location", "", "event location")
	addCalendarFlag(cmd, &in.Calendar)
	addTZFlag(cmd, &in.TZ, "the event times (default: from config / system)")
	cmd.Flags().BoolVar(&in.AllDay, "all-day", false, "all-day event (dates instead of times)")
	addRecurrenceFlags(cmd, &in.Recurrence)
	addCreateReminderColorFlags(cmd, &rc)
	_ = cmd.MarkFlagRequired("start")
	return cmd
}
