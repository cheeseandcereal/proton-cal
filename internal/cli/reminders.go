package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/reminders"
)

// reminderColorFlags holds the raw reminder/color flag values bound to a
// command. The resolvers turn flag presence into the create/update intent.
type reminderColorFlags struct {
	reminder         []string // repeatable --reminder
	noReminders      bool     // --no-reminders (explicit none)
	remindersDefault bool     // --reminders-default (inherit; update only)
	color            string   // --color
	noColor          bool     // --no-color (inherit)
}

// addCreateReminderColorFlags registers the create-side flags (no
// --reminders-default: a new event with no reminder flags already inherits).
func addCreateReminderColorFlags(cmd *cobra.Command, f *reminderColorFlags) {
	cmd.Flags().StringArrayVar(&f.reminder, "reminder", nil,
		"reminder before the event, repeatable: 15m, 1h30m, 2d, 1w (prefix email: for an email reminder; default notify)")
	cmd.Flags().BoolVar(&f.noReminders, "no-reminders", false, "create the event with no reminders (overrides the calendar default)")
	cmd.Flags().StringVar(&f.color, "color", "", "event color #RRGGBB (default: the calendar color)")
}

// addUpdateReminderColorFlags registers the update-side flags, including the
// extra states (inherit/none) and --no-color.
func addUpdateReminderColorFlags(cmd *cobra.Command, f *reminderColorFlags) {
	cmd.Flags().StringArrayVar(&f.reminder, "reminder", nil,
		"replace reminders, repeatable: 15m, 1h30m, 2d, 1w (prefix email:; default notify)")
	cmd.Flags().BoolVar(&f.noReminders, "no-reminders", false, "remove all reminders from the event")
	cmd.Flags().BoolVar(&f.remindersDefault, "reminders-default", false, "revert reminders to the calendar default")
	cmd.Flags().StringVar(&f.color, "color", "", "set the event color #RRGGBB")
	cmd.Flags().BoolVar(&f.noColor, "no-color", false, "use the calendar color (clear the per-event color)")
}

// validateExclusive rejects conflicting reminder/color flag combinations,
// using flag presence (Changed) where an empty value is meaningful.
func (f *reminderColorFlags) validateExclusive(cmd *cobra.Command) error {
	rem := len(f.reminder) > 0
	n := 0
	for _, b := range []bool{rem, f.noReminders, f.remindersDefault} {
		if b {
			n++
		}
	}
	if n > 1 {
		return errors.New("--reminder, --no-reminders and --reminders-default are mutually exclusive")
	}
	if cmd.Flags().Changed("color") && f.noColor {
		return errors.New("--color and --no-color are mutually exclusive")
	}
	if cmd.Flags().Changed("color") && f.color == "" {
		return errors.New("--color requires a value like #RRGGBB; use --no-color to inherit the calendar color")
	}
	if f.color != "" {
		if _, _, _, ok := parseHexColor(f.color); !ok {
			return fmt.Errorf("invalid --color %q (want #RRGGBB)", f.color)
		}
	}
	return nil
}

// createReminders resolves the create-side reminder list + set flag:
//   - no flags        -> (nil, false): inherit the calendar default
//   - --no-reminders  -> (nil, true):  explicitly none
//   - --reminder ...  -> (list, true): custom
func (f *reminderColorFlags) createReminders() (list []caltypes.Notification, set bool, err error) {
	if f.noReminders {
		return nil, true, nil
	}
	if len(f.reminder) == 0 {
		return nil, false, nil
	}
	parsed, err := reminders.ParseList(f.reminder)
	if err != nil {
		return nil, false, err
	}
	return parsed, true, nil
}

// updateReminders resolves the update-side reminder intent into a
// *event.RemindersUpdate (nil = keep current).
func (f *reminderColorFlags) updateReminders() (*event.RemindersUpdate, error) {
	switch {
	case f.remindersDefault:
		return &event.RemindersUpdate{Inherit: true}, nil
	case f.noReminders:
		return &event.RemindersUpdate{List: nil}, nil // explicit none
	case len(f.reminder) > 0:
		parsed, err := reminders.ParseList(f.reminder)
		if err != nil {
			return nil, err
		}
		return &event.RemindersUpdate{List: parsed}, nil
	default:
		return nil, nil // keep
	}
}

// updateColor resolves the update-side color intent into a *event.ColorUpdate
// (nil = keep current). --no-color or an explicit --color set it.
func (f *reminderColorFlags) updateColor(cmd *cobra.Command) *event.ColorUpdate {
	if f.noColor {
		return &event.ColorUpdate{Value: ""}
	}
	if cmd.Flags().Changed("color") {
		return &event.ColorUpdate{Value: f.color}
	}
	return nil
}
