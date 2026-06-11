package cli

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/front"
)

func newEventsCmd() *cobra.Command {
	var (
		days    int
		calSel  string
		tzFlag  string
		fromStr string
	)

	cmd := &cobra.Command{
		Use:   "events",
		Short: "List upcoming events (recurring events expanded into occurrences)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			tz := a.effectiveTZ(tzFlag)
			loc, err := time.LoadLocation(tz)
			if err != nil {
				return fmt.Errorf("invalid timezone %q: %w", tz, err)
			}

			start := time.Now()
			if fromStr != "" {
				start, err = front.ParseWhen(fromStr, tz)
				if err != nil {
					return err
				}
			}
			end := start.AddDate(0, 0, days)

			ctx := cmd.Context()
			info, access, err := a.resolveAccess(ctx, calSel)
			if err != nil {
				return err
			}

			listed, err := event.ListWindow(ctx, a.client, access.KR, info.ID, start.Unix(), end.Unix(), tz)
			if err != nil {
				return err
			}
			sort.SliceStable(listed, func(i, j int) bool {
				if listed[i].Occurrence.Start != listed[j].Occurrence.Start {
					return listed[i].Occurrence.Start < listed[j].Occurrence.Start
				}
				return listed[i].Occurrence.Event.ID < listed[j].Occurrence.Event.ID
			})

			if jsonOutput {
				rows := make([]eventJSON, 0, len(listed))
				for _, l := range listed {
					rows = append(rows, occurrenceJSON(l, loc))
				}
				return printJSON(rows)
			}

			w := humanOut()
			if len(listed) == 0 {
				fmt.Fprintln(w, "No upcoming events.")
				return nil
			}
			if fromStr == "" {
				fmt.Fprintf(w, "Events in next %d days:\n", days)
			} else {
				fmt.Fprintf(w, "Events for %d days from %s:\n", days, start.In(loc).Format("2006-01-02 15:04"))
			}
			fmt.Fprintln(w, strings.Repeat("-", 50))
			for _, l := range listed {
				for _, line := range occurrenceLines(l, loc) {
					fmt.Fprintln(w, line)
				}
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&days, "days", 7, "number of days to look ahead")
	cmd.Flags().StringVar(&calSel, "calendar", "", "calendar ID or name (default: configured default, else first)")
	cmd.Flags().StringVar(&tzFlag, "tz", "", "IANA timezone for queries and display (default: from config / system)")
	cmd.Flags().StringVar(&fromStr, "from", "", "window start, YYYY-MM-DD [HH:MM] (default: now; --days counts from it)")
	return cmd
}

func init() {
	rootCmd.AddCommand(newEventsCmd())
}
