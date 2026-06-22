package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/calcolor"
	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
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
	cmd.Flags().StringVar(&f.color, "color", "", "event color: a Proton color name (e.g. strawberry) or its hex (default: the calendar color)")
}

// addUpdateReminderColorFlags registers the update-side flags, including the
// extra states (inherit/none) and --no-color.
func addUpdateReminderColorFlags(cmd *cobra.Command, f *reminderColorFlags) {
	cmd.Flags().StringArrayVar(&f.reminder, "reminder", nil,
		"replace reminders, repeatable: 15m, 1h30m, 2d, 1w (prefix email:; default notify)")
	cmd.Flags().BoolVar(&f.noReminders, "no-reminders", false, "remove all reminders from the event")
	cmd.Flags().BoolVar(&f.remindersDefault, "reminders-default", false, "revert reminders to the calendar default")
	cmd.Flags().StringVar(&f.color, "color", "", "set the event color: a Proton color name (e.g. strawberry) or its hex")
	cmd.Flags().BoolVar(&f.noColor, "no-color", false, "revert to the calendar color")
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
		return errors.New("--color requires a value (a Proton color name or hex); use --no-color for the calendar color")
	}
	if f.color != "" {
		if _, err := calcolor.Resolve(f.color); err != nil {
			return err
		}
	}
	return nil
}

// createColor resolves the create-side color to a canonical palette hex
// ("" when no --color was given, i.e. inherit the calendar color, which a
// create with a null Color does naturally).
func (f *reminderColorFlags) createColor() (string, error) {
	if f.color == "" {
		return "", nil
	}
	return calcolor.Resolve(f.color)
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

// updateColor resolves the update-side color intent into a *calsvc.ColorUpdate
// (nil = keep current). --no-color reverts to the calendar color; an explicit
// --color sets a palette color. validateExclusive must have run first so the
// color value is already known-valid.
func (f *reminderColorFlags) updateColor(cmd *cobra.Command) (*calsvc.ColorUpdate, error) {
	if f.noColor {
		return &calsvc.ColorUpdate{Inherit: true}, nil
	}
	if cmd.Flags().Changed("color") {
		hex, err := calcolor.Resolve(f.color)
		if err != nil {
			return nil, err
		}
		return &calsvc.ColorUpdate{Hex: hex}, nil
	}
	return nil, nil
}
