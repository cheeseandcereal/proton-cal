package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/caljson"
	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
)

func newEventsCmd() *cobra.Command {
	var (
		in     calsvc.ListEventsInput
		fields []string
		all    bool
	)

	cmd := &cobra.Command{
		Use:   "events",
		Short: "List upcoming events (recurring events expanded into occurrences)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The list head shows date/time + summary; the rest are labeled
			// sub-lines. By default a curated subset (location, description,
			// conference, color) is shown; --fields/--all override it.
			sel := listDefaultFields()
			if all || len(fields) > 0 {
				var err error
				if sel, err = selectFields(eventFieldRegistry, fields, all); err != nil {
					return err
				}
			}

			svc, err := serviceFactory()
			if err != nil {
				return err
			}
			defer svc.Close()

			list, err := svc.ListEvents(cmd.Context(), in)
			if err != nil {
				return err
			}

			if outputJSON() {
				rows := make([]caljson.Event, 0, len(list.Items))
				for _, l := range list.Items {
					rows = append(rows, caljson.Occurrence(l, list.Location, list.Settings, list.Calendar))
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
			for _, line := range occurrenceListLines(list.Items, list.Location, sel, list.Settings, list.Calendar) {
				fmt.Fprintln(w, line)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&in.Days, "days", 7, "number of days to look ahead")
	addCalendarFlag(cmd, &in.Calendar)
	addTZFlag(cmd, &in.TZ, "queries and display (default: from config / system)")
	cmd.Flags().StringVar(&in.From, "from", "", "window start, YYYY-MM-DD [HH:MM] (default: now; --days counts from it)")
	cmd.Flags().StringSliceVar(&fields, "fields", nil, "comma-separated detail fields to expand under each event (text output)")
	cmd.Flags().BoolVar(&all, "all", false, "expand all detail fields under each event (text output)")
	return cmd
}
