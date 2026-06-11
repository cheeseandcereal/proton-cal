package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal-go/internal/event"
	"github.com/cheeseandcereal/proton-cal-go/internal/front"
)

// updatedJSON is the machine-readable shape of an update outcome.
type updatedJSON struct {
	Updated           bool `json:"updated"`
	EditedOccurrence  bool `json:"edited_occurrence"`
	RemovedExceptions int  `json:"removed_exceptions"`
}

// validateUpdateFlags ports the cli.py update flag-conflict validation
// EXACTLY: --no-repeat conflicts with --repeat/--rrule, and --occurrence
// conflicts with --no-repeat/--repeat/--rrule (edit the series instead).
func validateUpdateFlags(occurrence string, noRepeat bool, rec front.RecurrenceFlags) error {
	if noRepeat && (rec.Repeat != "" || rec.RawRRule != "") {
		return errors.New("--no-repeat cannot be combined with --repeat/--rrule")
	}
	if occurrence != "" && (noRepeat || rec.Repeat != "" || rec.RawRRule != "") {
		return errors.New("recurrence flags cannot be combined with --occurrence (edit the series instead)")
	}
	return nil
}

// updateTZName ports the cli.py timezone passing rule for updates:
// `tz_explicit or (timezone_str if (start or end) else None)` - the explicit
// --tz value when given, else the default timezone only when a new start or
// end is being set, else "" (keep the event's stored timezone).
func updateTZName(tzFlag, defaultTZ string, timesGiven bool) string {
	if tzFlag != "" {
		return tzFlag
	}
	if timesGiven {
		return defaultTZ
	}
	return ""
}

func newUpdateCmd() *cobra.Command {
	var (
		summary     string
		startStr    string
		endStr      string
		description string
		location    string
		calSel      string
		tzFlag      string
		occurrence  string
		noRepeat    bool
		repeat      string
		every       int
		count       int
		until       string
		rawRRule    string
	)

	cmd := &cobra.Command{
		Use:   "update EVENT_ID",
		Short: "Update an existing calendar event (only specified fields change)",
		Long: `Update an existing calendar event. Only specified fields are changed.

Recurring events keep their recurrence; use --repeat/--rrule to change it or
--no-repeat to remove it. Changing the recurrence or the times of a series
removes its edited occurrences (they no longer apply). Note: the server
requires the start time to match the recurrence pattern (e.g. a weekly BYDAY
rule and a matching weekday).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			eventID := args[0]
			rec := front.RecurrenceFlags{Repeat: repeat, Every: every, Count: count, Until: until, RawRRule: rawRRule}

			if err := validateUpdateFlags(occurrence, noRepeat, rec); err != nil {
				return err
			}

			a, err := newApp()
			if err != nil {
				return err
			}
			tz := a.effectiveTZ(tzFlag)

			opts := event.UpdateOptions{ClearRRule: noRepeat}
			if cmd.Flags().Changed("summary") {
				opts.Summary = &summary
			}
			if cmd.Flags().Changed("description") {
				opts.Description = &description
			}
			if cmd.Flags().Changed("location") {
				opts.Location = &location
			}
			if cmd.Flags().Changed("start") {
				t, err := front.ParseWhen(startStr, tz)
				if err != nil {
					return err
				}
				opts.Start = &t
			}
			if cmd.Flags().Changed("end") {
				t, err := front.ParseWhen(endStr, tz)
				if err != nil {
					return err
				}
				opts.End = &t
			}
			opts.TZName = updateTZName(tzFlag, tz, opts.Start != nil || opts.End != nil)

			var occurrenceTS int64
			if occurrence != "" {
				occurrenceTS, err = front.ParseOccurrence(occurrence, tz)
				if err != nil {
					return err
				}
			}

			ctx := cmd.Context()
			info, access, err := a.resolveAccess(ctx, calSel)
			if err != nil {
				return err
			}

			// Recurrence flags need the event's all-day-ness for the UNTIL
			// form, so fetch the raw row first - only in that case (ports
			// cli.py, where the occurrence path never builds an RRULE).
			if occurrence == "" && !rec.Empty() {
				raw, err := event.Get(ctx, a.client, info.ID, eventID)
				if err != nil {
					return fmt.Errorf("fetching event: %w", err)
				}
				rrule, err := rec.BuildRRule(tz, raw.IsAllDay())
				if err != nil {
					return err
				}
				if rrule != "" {
					opts.RRule = &rrule
				}
			}

			outcome, err := event.SmartUpdate(ctx, a.client, access, eventID, opts, occurrenceTS)
			if err != nil {
				return fmt.Errorf("updating event: %w", err)
			}

			if jsonOutput {
				return printJSON(updatedJSON{
					Updated:           true,
					EditedOccurrence:  outcome.EditedOccurrence,
					RemovedExceptions: outcome.RemovedExceptions,
				})
			}

			w := humanOut()
			if outcome.EditedOccurrence {
				fmt.Fprintln(w, "Occurrence updated.")
			} else {
				fmt.Fprintln(w, "Event updated.")
			}
			if outcome.RemovedExceptions > 0 {
				fmt.Fprintf(w, "Removed %d edited occurrence(s) invalidated by the series change.\n", outcome.RemovedExceptions)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&summary, "summary", "", "new event title")
	cmd.Flags().StringVar(&startStr, "start", "", "new start, YYYY-MM-DD HH:MM (all-day events: YYYY-MM-DD)")
	cmd.Flags().StringVar(&endStr, "end", "", "new end, YYYY-MM-DD HH:MM (all-day events: YYYY-MM-DD)")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	cmd.Flags().StringVar(&location, "location", "", "new location")
	cmd.Flags().StringVar(&calSel, "calendar", "", "calendar ID or name (default: configured default, else first)")
	cmd.Flags().StringVar(&tzFlag, "tz", "", "IANA timezone for the event times (default: event's stored timezone)")
	cmd.Flags().StringVar(&occurrence, "occurrence", "", "edit only this occurrence of a recurring event (original start: 'YYYY-MM-DD HH:MM' or 'YYYY-MM-DD')")
	cmd.Flags().BoolVar(&noRepeat, "no-repeat", false, "remove the recurrence from this event")
	addRecurrenceFlags(cmd, &repeat, &every, &count, &until, &rawRRule)
	return cmd
}

func init() {
	rootCmd.AddCommand(newUpdateCmd())
}
