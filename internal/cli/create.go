package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal-go/internal/event"
	"github.com/cheeseandcereal/proton-cal-go/internal/front"
)

// createdJSON is the machine-readable shape of a created event.
type createdJSON struct {
	ID      string `json:"id"`
	UID     string `json:"uid"`
	Summary string `json:"summary"`
	StartTS int64  `json:"start_ts"`
	EndTS   int64  `json:"end_ts"`
	AllDay  bool   `json:"all_day"`
	RRule   string `json:"rrule,omitempty"`
}

// resolveCreateTimes ports the cli.py create date wrangling. All-day: start
// is a date; end is an optional INCLUSIVE date (default = start) converted to
// the exclusive iCal end by +24h, erroring when it lands at or before the
// start. Timed: --end is required and both parse as wall times in tzName.
func resolveCreateTimes(startStr, endStr string, allDay bool, tzName string) (start, end time.Time, err error) {
	if allDay {
		start, err = front.ParseDate(startStr)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		end = start
		if endStr != "" {
			end, err = front.ParseDate(endStr)
			if err != nil {
				return time.Time{}, time.Time{}, err
			}
		}
		end = end.Add(24 * time.Hour) // exclusive iCal end
		if !end.After(start) {
			return time.Time{}, time.Time{}, errors.New("end date must not be before start date")
		}
		return start, end, nil
	}

	if endStr == "" {
		return time.Time{}, time.Time{}, errors.New("--end is required for timed events")
	}
	start, err = front.ParseLocalDateTime(startStr, tzName)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, err = front.ParseLocalDateTime(endStr, tzName)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return start, end, nil
}

func newCreateCmd() *cobra.Command {
	var (
		startStr    string
		endStr      string
		description string
		location    string
		calSel      string
		tzFlag      string
		allDay      bool
		repeat      string
		every       int
		count       int
		until       string
		rawRRule    string
	)

	cmd := &cobra.Command{
		Use:   "create SUMMARY",
		Short: "Create a new calendar event (optionally recurring or all-day)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			summary := args[0]

			a, err := newApp()
			if err != nil {
				return err
			}
			tz := a.effectiveTZ(tzFlag)

			start, end, err := resolveCreateTimes(startStr, endStr, allDay, tz)
			if err != nil {
				return err
			}

			rec := front.RecurrenceFlags{Repeat: repeat, Every: every, Count: count, Until: until, RawRRule: rawRRule}
			rrule, err := rec.BuildRRule(tz, allDay)
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			_, access, err := a.resolveAccess(ctx, calSel)
			if err != nil {
				return err
			}

			raw, err := event.Create(ctx, a.client, access, event.CreateOptions{
				Summary:     summary,
				Description: description,
				Location:    location,
				Start:       start,
				End:         end,
				TZName:      tz,
				AllDay:      allDay,
				RRule:       rrule,
			})
			if err != nil {
				return fmt.Errorf("creating event: %w", err)
			}

			if jsonOutput {
				out := createdJSON{
					Summary: summary,
					StartTS: start.Unix(),
					EndTS:   end.Unix(),
					AllDay:  allDay,
					RRule:   rrule,
				}
				if raw != nil {
					out.ID = raw.ID
					out.UID = raw.UID
					out.StartTS = raw.StartTime
					out.EndTS = raw.EndTime
				}
				return printJSON(out)
			}

			w := humanOut()
			fmt.Fprintln(w, "Event created.")
			if raw != nil {
				fmt.Fprintf(w, "  ID: %s\n", raw.ID)
			}
			if rrule != "" {
				fmt.Fprintf(w, "  Repeats: %s\n", rrule)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&startStr, "start", "", "start time, YYYY-MM-DD HH:MM (with --all-day: YYYY-MM-DD)")
	cmd.Flags().StringVar(&endStr, "end", "", "end time, YYYY-MM-DD HH:MM (with --all-day: inclusive end date, default = start)")
	cmd.Flags().StringVar(&description, "description", "", "event description")
	cmd.Flags().StringVar(&location, "location", "", "event location")
	cmd.Flags().StringVar(&calSel, "calendar", "", "calendar ID or name (default: configured default, else first)")
	cmd.Flags().StringVar(&tzFlag, "tz", "", "IANA timezone for the event times (default: from config / system)")
	cmd.Flags().BoolVar(&allDay, "all-day", false, "all-day event (dates instead of times)")
	addRecurrenceFlags(cmd, &repeat, &every, &count, &until, &rawRRule)
	_ = cmd.MarkFlagRequired("start")
	return cmd
}

// addRecurrenceFlags registers the recurrence flags shared by create and
// update.
func addRecurrenceFlags(cmd *cobra.Command, repeat *string, every, count *int, until, rawRRule *string) {
	cmd.Flags().StringVar(repeat, "repeat", "", "make the event recurring: daily|weekly|monthly|yearly")
	cmd.Flags().IntVar(every, "every", 1, "repeat interval, e.g. 2 = every second day/week/... (with --repeat)")
	cmd.Flags().IntVar(count, "count", 0, "number of occurrences, max 49 (with --repeat)")
	cmd.Flags().StringVar(until, "until", "", "last day of the recurrence, YYYY-MM-DD (with --repeat)")
	cmd.Flags().StringVar(rawRRule, "rrule", "", "raw RRULE value (advanced; replaces --repeat flags)")
}

func init() {
	rootCmd.AddCommand(newCreateCmd())
}
