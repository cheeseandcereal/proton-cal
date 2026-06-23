package cli

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/pkg/calcolor"
	"github.com/cheeseandcereal/proton-cal/pkg/calsvc"
	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/event"
)

// reminderColorFlags holds the raw reminder/color flag values bound to a
// command. The resolvers turn flag presence into the create/update intent.
type reminderColorFlags struct {
	reminder         []string // repeatable --reminder
	noReminders      bool     // --no-reminders (explicit none)
	remindersDefault bool     // --reminders-default (inherit; update only)
	color            string   // --color (a name/hex, or "default" for the calendar color)
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
// extra reminder states (inherit/none) and color "default".
func addUpdateReminderColorFlags(cmd *cobra.Command, f *reminderColorFlags) {
	cmd.Flags().StringArrayVar(&f.reminder, "reminder", nil,
		"replace reminders, repeatable: 15m, 1h30m, 2d, 1w (prefix email:; default notify)")
	cmd.Flags().BoolVar(&f.noReminders, "no-reminders", false, "remove all reminders from the event")
	cmd.Flags().BoolVar(&f.remindersDefault, "reminders-default", false, "revert reminders to the calendar default")
	cmd.Flags().StringVar(&f.color, "color", "",
		`set the event color: a Proton color name (e.g. strawberry) or its hex; "default" reverts to the calendar color`)
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
	if cmd.Flags().Changed("color") && f.color == "" {
		return errors.New(`--color requires a value (a Proton color name or hex, or "default" for the calendar color)`)
	}
	if f.color != "" && !calcolor.IsDefault(f.color) {
		if _, err := calcolor.Resolve(f.color); err != nil {
			return err
		}
	}
	return nil
}

// reminderMode maps the boolean flag presence to the shared intent. The CLI
// has no explicit "keep": absent reminder flags mean inherit on create and keep
// on update, both of which fall out of the resolvers below.
func (f *reminderColorFlags) reminderMode() calsvc.ReminderMode {
	switch {
	case f.remindersDefault:
		return calsvc.ReminderInherit
	case f.noReminders:
		return calsvc.ReminderNone
	case len(f.reminder) > 0:
		return calsvc.ReminderCustom
	default:
		return calsvc.ReminderKeep
	}
}

// createColor resolves the create-side color to a palette hex ("" for no
// --color or "default": a null Color already inherits the calendar color).
func (f *reminderColorFlags) createColor() (string, error) {
	return calsvc.ResolveColorCreate(f.color)
}

// createReminders resolves the create-side reminder list + set flag: no flags
// -> inherit (nil,false); --no-reminders -> none (nil,true); --reminder -> custom.
func (f *reminderColorFlags) createReminders() (list []caltypes.Notification, set bool, err error) {
	return calsvc.ResolveCreateReminders(f.reminderMode(), f.reminder)
}

// updateReminders resolves the update-side reminder intent into a
// *event.RemindersUpdate (nil = keep current).
func (f *reminderColorFlags) updateReminders() (*event.RemindersUpdate, error) {
	return calsvc.ResolveUpdateReminders(f.reminderMode(), f.reminder)
}

// updateColor resolves the update-side color into a *calsvc.ColorUpdate (nil =
// keep; "default" reverts to calendar color). Requires validateExclusive first.
func (f *reminderColorFlags) updateColor(cmd *cobra.Command) (*calsvc.ColorUpdate, error) {
	if !cmd.Flags().Changed("color") {
		return nil, nil
	}
	return calsvc.ResolveColorUpdate(f.color)
}
