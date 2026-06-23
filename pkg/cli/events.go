package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/pkg/caljson"
	"github.com/cheeseandcereal/proton-cal/pkg/calsvc"
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
		Args:  requireArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := serviceFactory()
			if err != nil {
				return err
			}
			defer svc.Close()

			list, err := svc.ListEvents(cmd.Context(), in)
			if err != nil {
				return err
			}

			// Default to a curated subset of detail sub-lines (labeling each
			// event with its calendar when the window spans several);
			// --fields/--all override it.
			sel := listDefaultFields(!list.SingleCalendar())
			if all || len(fields) > 0 {
				if sel, err = selectFields(eventFieldRegistry, fields, all); err != nil {
					return err
				}
			}

			if outputJSON() {
				rows := make([]caljson.Event, 0, len(list.Items))
				for _, item := range list.Items {
					rows = append(rows, caljson.Occurrence(item.Listed, list.Location, item.Settings, item.Calendar))
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
			for _, line := range occurrenceListLines(list.Items, list.Location, sel) {
				fmt.Fprintln(w, line)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&in.Days, "days", 7, "number of days to look ahead")
	addCalendarsFlag(cmd, &in.Calendars)
	cmd.Flags().BoolVar(&in.AllCalendars, "all-calendars", false, "list events across every calendar")
	cmd.MarkFlagsMutuallyExclusive("calendar", "all-calendars")
	addTZFlag(cmd, &in.TZ, "queries and display (default: from config / system)")
	cmd.Flags().StringVar(&in.From, "from", "", "window start, YYYY-MM-DD [HH:MM] (default: now; --days counts from it)")
	cmd.Flags().StringSliceVar(&fields, "fields", nil, "comma-separated detail fields to expand under each event (text output)")
	cmd.Flags().BoolVar(&all, "all", false, "expand all detail fields under each event (text output)")
	return cmd
}
