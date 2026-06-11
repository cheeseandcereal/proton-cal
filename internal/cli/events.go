package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
)

func newEventsCmd() *cobra.Command {
	var in calsvc.ListEventsInput

	cmd := &cobra.Command{
		Use:   "events",
		Short: "List upcoming events (recurring events expanded into occurrences)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := newService()
			if err != nil {
				return err
			}
			defer svc.Close()

			list, err := svc.ListEvents(cmd.Context(), in)
			if err != nil {
				return err
			}

			if jsonOutput {
				rows := make([]eventJSON, 0, len(list.Items))
				for _, l := range list.Items {
					rows = append(rows, occurrenceJSON(l, list.Location))
				}
				return printJSON(rows)
			}

			w := humanOut()
			if len(list.Items) == 0 {
				fmt.Fprintln(w, "No upcoming events.")
				return nil
			}
			if list.FromGiven {
				fmt.Fprintf(w, "Events for %d days from %s:\n", list.Days, list.From.In(list.Location).Format("2006-01-02 15:04"))
			} else {
				fmt.Fprintf(w, "Events in next %d days:\n", list.Days)
			}
			fmt.Fprintln(w, strings.Repeat("-", 50))
			for _, l := range list.Items {
				for _, line := range occurrenceLines(l, list.Location) {
					fmt.Fprintln(w, line)
				}
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&in.Days, "days", 7, "number of days to look ahead")
	addCalendarFlag(cmd, &in.Calendar)
	addTZFlag(cmd, &in.TZ, "queries and display (default: from config / system)")
	cmd.Flags().StringVar(&in.From, "from", "", "window start, YYYY-MM-DD [HH:MM] (default: now; --days counts from it)")
	return cmd
}
