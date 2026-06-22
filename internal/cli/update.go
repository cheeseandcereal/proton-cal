package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/caljson"
	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
	"github.com/cheeseandcereal/proton-cal/internal/eventview"
)

// newUpdateCmd is the parent "update" command grouping resource updaters.
func newUpdateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update a resource (event or calendar)",
		Args:  requireArgs(cobra.NoArgs),
		RunE: func(_ *cobra.Command, _ []string) error {
			return errMissingSubcommand
		},
	}
	cmd.AddCommand(newUpdateEventCmd(), newUpdateCalendarCmd())
	return cmd
}

func newUpdateEventCmd() *cobra.Command {
	var (
		in          calsvc.UpdateEventInput
		summary     string
		description string
		location    string
		rc          reminderColorFlags
	)

	cmd := &cobra.Command{
		Use:   "event EVENT_ID",
		Short: "Update an existing calendar event (only specified fields change)",
		Long: `Update an existing calendar event. Only specified fields are changed.

Recurring events keep their recurrence; use --repeat/--rrule to change it or
--no-repeat to remove it. Changing the recurrence or the times of a series
removes its edited occurrences (they no longer apply). Note: the server
requires the start time to match the recurrence pattern (e.g. a weekly BYDAY
rule and a matching weekday).`,
		Args: requireArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.EventID = args[0]

			// Flag-presence semantics: a flag given as an empty string
			// clears the field; an omitted flag keeps it.
			if cmd.Flags().Changed("summary") {
				in.Summary = &summary
			}
			if cmd.Flags().Changed("description") {
				in.Description = &description
			}
			if cmd.Flags().Changed("location") {
				in.Location = &location
			}

			if err := rc.validateExclusive(cmd); err != nil {
				return err
			}
			rem, err := rc.updateReminders()
			if err != nil {
				return err
			}
			in.Reminders = rem
			col, err := rc.updateColor(cmd)
			if err != nil {
				return err
			}
			in.Color = col

			svc, err := serviceFactory()
			if err != nil {
				return err
			}
			defer svc.Close()

			outcome, err := svc.UpdateEvent(cmd.Context(), in)
			if err != nil {
				return err
			}

			if outputJSON() {
				return printJSON(caljson.UpdatedOf(outcome))
			}

			w := humanOut()
			headline, note := eventview.UpdateOutcomeMessage(outcome)
			fmt.Fprintln(w, headline)
			if note != "" {
				fmt.Fprintln(w, note)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&summary, "summary", "", "new event title")
	cmd.Flags().StringVar(&in.Start, "start", "", "new start, YYYY-MM-DD HH:MM (all-day events: YYYY-MM-DD)")
	cmd.Flags().StringVar(&in.End, "end", "", "new end, YYYY-MM-DD HH:MM (all-day events: YYYY-MM-DD)")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	cmd.Flags().StringVar(&location, "location", "", "new location")
	addCalendarFlag(cmd, &in.Calendar)
	addTZFlag(cmd, &in.TZ, "the event times (default: event's stored timezone)")
	addOccurrenceFlag(cmd, &in.Occurrence, "edit")
	cmd.Flags().BoolVar(&in.NoRepeat, "no-repeat", false, "remove the recurrence from this event")
	addRecurrenceFlags(cmd, &in.Recurrence)
	addUpdateReminderColorFlags(cmd, &rc)
	return cmd
}
