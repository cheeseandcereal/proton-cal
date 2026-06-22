package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/caljson"
	"github.com/cheeseandcereal/proton-cal/internal/eventview"
)

func newCalendarsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "calendars",
		Short: "List all calendars",
		Args:  cobra.NoArgs,
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
			defaultSel := svc.DefaultCalendarSelector()

			if outputJSON() {
				return printJSON(caljson.Calendars(cals, defaultSel))
			}
			renderCalendars(humanOut(), cals, defaultSel)
			return nil
		},
	}
}

func renderCalendars(w io.Writer, cals []calendar.Info, defaultSel string) {
	if len(cals) == 0 {
		fmt.Fprintln(w, "No calendars found.")
		return
	}
	for _, c := range cals {
		// Shared header + ID lines, then the CLI's extra color/description.
		for _, line := range eventview.CalendarHeaderLines(c, defaultSel) {
			fmt.Fprintln(w, line)
		}
		fmt.Fprintf(w, "  Color: %s%s\n", swatch(c.Color), c.Color)
		if c.Description != "" {
			fmt.Fprintf(w, "  Description: %s\n", c.Description)
		}
	}
}
