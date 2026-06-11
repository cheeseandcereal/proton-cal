package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
)

// updatedJSON is the machine-readable shape of an update outcome.
type updatedJSON struct {
	Updated           bool `json:"updated"`
	EditedOccurrence  bool `json:"edited_occurrence"`
	RemovedExceptions int  `json:"removed_exceptions"`
}

func newUpdateCmd() *cobra.Command {
	var (
		in          calsvc.UpdateEventInput
		summary     string
		description string
		location    string
	)

	cmd := &cobra.Command{
		Use:   "update EVENT_ID",
		Short: "Update an existing calendar event (only specified fields change)",
		Long: `Update an existing calendar event. Only specified fields are changed.

Recurring events keep their recurrence; use --repeat/--rrule to change it or
--no-repeat to remove it. Changing the recurrence or the times of a series
removes its edited occurrences (they no longer apply). Note: the server
requires the start time to match the recurrence pattern (e.g. a weekly BYDAY
rule and a matching weekday).`,
		Args: cobra.ExactArgs(1),
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

			svc, err := newService()
			if err != nil {
				return err
			}
			defer svc.Close()

			outcome, err := svc.UpdateEvent(cmd.Context(), in)
			if err != nil {
				return err
			}

			if jsonOutput {
				return printJSON(updatedJSON{
					Updated:           true,
					EditedOccurrence:  outcome.EditedOccurrence,
					RemovedExceptions: outcome.RemovedExceptions,
				})
			}

			w := humanOut()
			if outcome.EditedOccurrence {
				fmt.Fprintln(w, "Occurrence updated.")
			} else {
				fmt.Fprintln(w, "Event updated.")
			}
			if outcome.RemovedExceptions > 0 {
				fmt.Fprintf(w, "Removed %d edited occurrence(s) invalidated by the series change.\n", outcome.RemovedExceptions)
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
	return cmd
}
