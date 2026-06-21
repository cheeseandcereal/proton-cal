package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
)

// createdJSON is the machine-readable shape of a created event. ID/UID are
// empty when the server did not echo the created row.
type createdJSON struct {
	ID      string `json:"id,omitempty"`
	UID     string `json:"uid,omitempty"`
	Summary string `json:"summary"`
	StartTS int64  `json:"start_ts"`
	EndTS   int64  `json:"end_ts"`
	AllDay  bool   `json:"all_day"`
	RRule   string `json:"rrule,omitempty"`
}

func newCreateCmd() *cobra.Command {
	var in calsvc.CreateEventInput

	cmd := &cobra.Command{
		Use:   "create SUMMARY",
		Short: "Create a new calendar event (optionally recurring or all-day)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.Summary = args[0]

			svc, err := newService()
			if err != nil {
				return err
			}
			defer svc.Close()

			created, err := svc.CreateEvent(cmd.Context(), in)
			if err != nil {
				return err
			}

			if outputJSON() {
				return printJSON(createdJSON{
					ID:      created.ID,
					UID:     created.UID,
					Summary: created.Summary,
					StartTS: created.Start.Unix(),
					EndTS:   created.End.Unix(),
					AllDay:  created.AllDay,
					RRule:   created.RRule,
				})
			}

			w := humanOut()
			fmt.Fprintln(w, "Event created.")
			if created.ID != "" {
				fmt.Fprintf(w, "  ID: %s\n", created.ID)
			}
			if created.RRule != "" {
				fmt.Fprintf(w, "  Repeats: %s\n", created.RRule)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&in.Start, "start", "", "start time, YYYY-MM-DD HH:MM (with --all-day: YYYY-MM-DD)")
	cmd.Flags().StringVar(&in.End, "end", "", "end time, YYYY-MM-DD HH:MM (with --all-day: inclusive end date, default = start)")
	cmd.Flags().StringVar(&in.Description, "description", "", "event description")
	cmd.Flags().StringVar(&in.Location, "location", "", "event location")
	addCalendarFlag(cmd, &in.Calendar)
	addTZFlag(cmd, &in.TZ, "the event times (default: from config / system)")
	cmd.Flags().BoolVar(&in.AllDay, "all-day", false, "all-day event (dates instead of times)")
	addRecurrenceFlags(cmd, &in.Recurrence)
	_ = cmd.MarkFlagRequired("start")
	return cmd
}
