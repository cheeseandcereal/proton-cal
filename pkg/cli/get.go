package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/pkg/caljson"
	"github.com/cheeseandcereal/proton-cal/pkg/calsvc"
)

// newGetCmd is the parent "get" command grouping single-resource getters.
func newGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get",
		Short: "Show a single resource in detail (event or calendar)",
		Args:  requireArgs(cobra.NoArgs),
		RunE: func(_ *cobra.Command, _ []string) error {
			return errMissingSubcommand
		},
	}
	cmd.AddCommand(newGetEventCmd(), newGetCalendarCmd())
	return cmd
}

func newGetEventCmd() *cobra.Command {
	var (
		in       calsvc.GetEventInput
		ics      bool
		noSeries bool
		fields   []string
		all      bool
	)

	cmd := &cobra.Command{
		Use:   "event EVENT_ID",
		Short: "Show a single event in full detail, or export its iCalendar",
		Long: "Show a single event in full detail (attendees, conferencing, reminders),\n" +
			"or export it as an iCalendar (.ics) document with --ics.\n\n" +
			"For a recurring event, --ics exports the WHOLE series by default: one\n" +
			".ics with the master VEVENT plus a VEVENT per edited occurrence. Use\n" +
			"--no-series to export only the single addressed VEVENT instead.\n\n" +
			"Use -o/--output json for structured JSON (always the full field set).\n" +
			"In text output, --fields selects which fields to show and --all reveals\n" +
			"everything (including uid, calendar_id and the raw RRULE).",
		Args: requireArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if ics && outputJSON() {
				return errors.New("--ics cannot be combined with --output json")
			}
			if noSeries && !ics {
				return errors.New("--no-series only applies with --ics")
			}
			in.EventID = args[0]
			in.WithICS = ics
			in.NoSeries = noSeries

			sel, err := selectFields(eventFieldRegistry, fields, all)
			if err != nil {
				return err
			}

			svc, err := serviceFactory()
			if err != nil {
				return err
			}
			defer svc.Close()

			got, err := svc.GetEvent(cmd.Context(), in)
			if err != nil {
				return err
			}

			switch {
			case ics:
				if got.ICS == "" {
					return calsvc.ErrICSUndecryptable
				}
				fmt.Fprintln(outWriter, got.ICS) // the document itself, to stdout
				return nil
			case outputJSON():
				return printJSON(caljson.EventDetail(got.Event, got.Location, got.Settings, got.Calendar))
			default:
				w := humanOut()
				for _, line := range eventDetailLines(got.Event, got.Location, sel, got.Settings, got.Calendar) {
					fmt.Fprintln(w, line)
				}
				return nil
			}
		},
	}

	cmd.Flags().BoolVar(&ics, "ics", false, "export the raw iCalendar (.ics) document instead of detail (whole series for recurring events)")
	cmd.Flags().BoolVar(&noSeries, "no-series", false, "with --ics, export only the addressed VEVENT, not the whole recurring series")
	cmd.Flags().StringSliceVar(&fields, "fields", nil, "comma-separated fields to show in text output (default: curated set)")
	cmd.Flags().BoolVar(&all, "all", false, "show all fields in text output (including uid, calendar_id, rrule)")
	addCalendarFlag(cmd, &in.Calendar)
	addTZFlag(cmd, &in.TZ, "display (default: from config / system)")
	return cmd
}

func newGetCalendarCmd() *cobra.Command {
	var (
		selector string
		fields   []string
		all      bool
	)

	cmd := &cobra.Command{
		Use:   "calendar [SELECTOR]",
		Short: "Show a single calendar in detail",
		Long: "Show a single calendar (by ID or name) in detail. With no selector,\n" +
			"shows the account's default calendar (else the first).\n\n" +
			"Use -o/--output json for structured JSON. In text output, --fields\n" +
			"selects which fields to show and --all reveals everything (including\n" +
			"the account email and member/address IDs).",
		Args: requireArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				selector = args[0]
			}

			sel, err := selectFields(calendarFieldRegistry, fields, all)
			if err != nil {
				return err
			}

			svc, err := serviceFactory()
			if err != nil {
				return err
			}
			defer svc.Close()

			got, err := svc.GetCalendar(cmd.Context(), selector)
			if err != nil {
				return err
			}

			if outputJSON() {
				return printJSON(caljson.CalendarDetailOf(got.Info, got.Settings, got.IsDefault))
			}
			w := humanOut()
			for _, line := range calendarDetailLines(got.Info, got.Settings, got.IsDefault, sel) {
				fmt.Fprintln(w, line)
			}
			return nil
		},
	}

	cmd.Flags().StringSliceVar(&fields, "fields", nil, "comma-separated fields to show in text output (default: curated set)")
	cmd.Flags().BoolVar(&all, "all", false, "show all fields in text output (including email, member_id, address_id)")
	return cmd
}
