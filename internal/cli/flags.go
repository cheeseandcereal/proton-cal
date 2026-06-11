package cli

import (
	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
)

// Shared flag groups. Commands bind only the groups they need; the help
// strings stay consistent across commands.

// addCalendarFlag registers the --calendar selector flag.
func addCalendarFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVar(target, "calendar", "", "calendar ID or name (default: configured default, else first)")
}

// addTZFlag registers the --tz flag with a per-command default description.
func addTZFlag(cmd *cobra.Command, target *string, what string) {
	cmd.Flags().StringVar(target, "tz", "", "IANA timezone for "+what)
}

// addOccurrenceFlag registers the --occurrence flag.
func addOccurrenceFlag(cmd *cobra.Command, target *string, verb string) {
	cmd.Flags().StringVar(target, "occurrence", "",
		verb+" only this occurrence of a recurring event (original start: 'YYYY-MM-DD HH:MM' or 'YYYY-MM-DD')")
}

// addRecurrenceFlags registers the recurrence flags shared by create and
// update, bound to a calsvc.Recurrence.
func addRecurrenceFlags(cmd *cobra.Command, rec *calsvc.Recurrence) {
	cmd.Flags().StringVar(&rec.Repeat, "repeat", "", "make the event recurring: daily|weekly|monthly|yearly")
	cmd.Flags().IntVar(&rec.Every, "every", 1, "repeat interval, e.g. 2 = every second day/week/... (with --repeat)")
	cmd.Flags().IntVar(&rec.Count, "count", 0, "number of occurrences, max 49 (with --repeat)")
	cmd.Flags().StringVar(&rec.Until, "until", "", "last day of the recurrence, YYYY-MM-DD (with --repeat)")
	cmd.Flags().StringVar(&rec.RawRRule, "rrule", "", "raw RRULE value (advanced; replaces --repeat flags)")
}
