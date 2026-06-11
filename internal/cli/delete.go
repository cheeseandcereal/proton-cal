package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/front"
)

func newDeleteCmd() *cobra.Command {
	var (
		calSel     string
		occurrence string
		tzFlag     string
	)

	cmd := &cobra.Command{
		Use:   "delete EVENT_ID",
		Short: "Delete a calendar event",
		Long: `Delete a calendar event.

Recurring events: deletes the whole series (master + edited occurrences)
unless --occurrence limits it to one occurrence. Passing an edited
occurrence's own ID deletes just that occurrence.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eventID := args[0]

			a, err := newApp()
			if err != nil {
				return err
			}
			tz := a.effectiveTZ(tzFlag)

			var occurrenceTS int64
			if occurrence != "" {
				occurrenceTS, err = front.ParseOccurrence(occurrence, tz)
				if err != nil {
					return err
				}
			}

			ctx := cmd.Context()
			_, access, err := a.resolveAccess(ctx, calSel)
			if err != nil {
				return err
			}

			res, err := event.SmartDelete(ctx, a.client, access, eventID, occurrenceTS)
			if err != nil {
				return fmt.Errorf("deleting event: %w", err)
			}

			if jsonOutput {
				return printJSON(res)
			}

			w := humanOut()
			switch res.Kind {
			case "occurrence":
				fmt.Fprintln(w, "Occurrence deleted.")
			case "series":
				fmt.Fprintf(w, "Recurring series deleted (%d row(s)).\n", res.RowsDeleted)
			case "event":
				fmt.Fprintln(w, "Event deleted.")
			default:
				fmt.Fprintf(w, "Deleted (%s, %d row(s)).\n", res.Kind, res.RowsDeleted)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&calSel, "calendar", "", "calendar ID or name (default: configured default, else first)")
	cmd.Flags().StringVar(&occurrence, "occurrence", "", "delete only this occurrence of a recurring event (original start: 'YYYY-MM-DD HH:MM' or 'YYYY-MM-DD')")
	cmd.Flags().StringVar(&tzFlag, "tz", "", "IANA timezone for --occurrence (default: from config / system)")
	return cmd
}

func init() {
	rootCmd.AddCommand(newDeleteCmd())
}
