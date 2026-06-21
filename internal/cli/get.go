package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
)

func newGetCmd() *cobra.Command {
	var in calsvc.GetEventInput
	var format string

	cmd := &cobra.Command{
		Use:   "get EVENT_ID",
		Short: "Show a single event in full detail (attendees, conferencing, reminders), or export its iCalendar",
		Long: "Show a single event in full detail, or export it as iCalendar.\n\n" +
			"Output format:\n" +
			"  --format ics    raw iCalendar (.ics) document\n" +
			"  --format detail human-readable detail (default)\n" +
			"  --format json   structured JSON\n" +
			"When --format is omitted, the global --json flag selects JSON vs detail.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve the effective format: an explicit --format wins; else
			// the global --json flag selects json vs the human detail view.
			switch format {
			case "":
				if jsonOutput {
					format = "json"
				} else {
					format = "detail"
				}
			case "ics", "json", "detail":
			default:
				return fmt.Errorf("invalid --format %q (want ics, json or detail)", format)
			}
			in.EventID = args[0]
			in.WithICS = format == "ics"

			svc, err := newService()
			if err != nil {
				return err
			}
			defer svc.Close()

			got, err := svc.GetEvent(cmd.Context(), in)
			if err != nil {
				return err
			}

			switch format {
			case "ics":
				if got.ICS == "" {
					return fmt.Errorf("event could not be decrypted into iCalendar")
				}
				fmt.Println(got.ICS) // the document itself, to stdout
				return nil
			case "json":
				return printJSON(eventDetailJSON(got.Event, got.Location))
			default:
				w := humanOut()
				for _, line := range eventDetailLines(got.Event, got.Location) {
					fmt.Fprintln(w, line)
				}
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&format, "format", "", "output format: ics, json or detail (default: detail, or json with --json)")
	addCalendarFlag(cmd, &in.Calendar)
	addTZFlag(cmd, &in.TZ, "display (default: from config / system)")
	return cmd
}
