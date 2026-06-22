package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/calcolor"
	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/caljson"
	"github.com/cheeseandcereal/proton-cal/internal/eventview"
)

func newCalendarsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "calendars",
		Short: "List all calendars",
		Args:  requireArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := serviceFactory()
			if err != nil {
				return err
			}
			defer svc.Close()

			cals, err := svc.Calendars(cmd.Context())
			if err != nil {
				return err
			}
			// Best-effort: a failure to read the server default just leaves
			// no calendar marked default.
			defaultID, _ := svc.DefaultCalendarID(cmd.Context())

			if outputJSON() {
				return printJSON(caljson.Calendars(cals, defaultID))
			}
			renderCalendars(humanOut(), cals, defaultID)
			return nil
		},
	}
}

func renderCalendars(w io.Writer, cals []calendar.Info, defaultID string) {
	if len(cals) == 0 {
		fmt.Fprintln(w, "No calendars found.")
		return
	}
	for _, c := range cals {
		// Shared header + ID lines, then the CLI's extra color/description.
		for _, line := range eventview.CalendarHeaderLines(c, defaultID) {
			fmt.Fprintln(w, line)
		}
		fmt.Fprintf(w, "  Color: %s%s\n", swatch(c.Color), calcolor.Label(c.Color))
		if c.Description != "" {
			fmt.Fprintf(w, "  Description: %s\n", c.Description)
		}
	}
}
