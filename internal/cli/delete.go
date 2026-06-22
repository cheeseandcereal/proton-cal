package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
	"github.com/cheeseandcereal/proton-cal/internal/eventview"
)

// newDeleteCmd is the parent "delete" command grouping resource deleters.
func newDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a resource (event or calendar)",
		Args:  requireArgs(cobra.NoArgs),
		RunE: func(_ *cobra.Command, _ []string) error {
			return errMissingSubcommand
		},
	}
	cmd.AddCommand(newDeleteEventCmd(), newDeleteCalendarCmd())
	return cmd
}

func newDeleteEventCmd() *cobra.Command {
	var in calsvc.DeleteEventInput

	cmd := &cobra.Command{
		Use:   "event EVENT_ID",
		Short: "Delete a calendar event",
		Long: `Delete a calendar event.

Recurring events: deletes the whole series (master + edited occurrences)
unless --occurrence limits it to one occurrence. Passing an edited
occurrence's own ID deletes just that occurrence.`,
		Args: requireArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.EventID = args[0]

			svc, err := serviceFactory()
			if err != nil {
				return err
			}
			defer svc.Close()

			res, err := svc.DeleteEvent(cmd.Context(), in)
			if err != nil {
				return err
			}

			if outputJSON() {
				return printJSON(res)
			}

			fmt.Fprintln(humanOut(), eventview.DeleteResultMessage(res, in.EventID, false))
			return nil
		},
	}

	addCalendarFlag(cmd, &in.Calendar)
	addOccurrenceFlag(cmd, &in.Occurrence, "delete")
	addTZFlag(cmd, &in.TZ, "--occurrence (default: from config / system)")
	return cmd
}
