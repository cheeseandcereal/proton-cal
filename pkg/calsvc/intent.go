package calsvc

import (
	"errors"
	"fmt"

	"github.com/cheeseandcereal/proton-cal/pkg/calcolor"
	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/event"
	"github.com/cheeseandcereal/proton-cal/pkg/reminders"
)

// ReminderMode is the surface-independent reminder change intent, shared by the
// CLI (from boolean flags) and the MCP server (from a string). The CLI never
// uses ReminderKeep on create (a new event with no flags inherits, same as
// ReminderInherit); update uses all four.
type ReminderMode int

// Reminder modes, shared by both frontends.
const (
	ReminderKeep    ReminderMode = iota // leave the event's reminders unchanged (update only)
	ReminderInherit                     // revert to the calendar default
	ReminderNone                        // explicitly no reminders
	ReminderCustom                      // use the supplied list
)

// ParseReminderMode maps the MCP reminders_mode string to a ReminderMode.
// "" and "keep" both mean keep.
func ParseReminderMode(s string) (ReminderMode, error) {
	switch s {
	case "", "keep":
		return ReminderKeep, nil
	case "inherit":
		return ReminderInherit, nil
	case "none":
		return ReminderNone, nil
	case "custom":
		return ReminderCustom, nil
	default:
		return ReminderKeep, fmt.Errorf("invalid reminders mode %q (keep, inherit, none or custom)", s)
	}
}

// ResolveUpdateReminders turns a mode + specs into the update tri-state
// (nil = keep). The Inherit:true (revert -> null) vs List:nil (none -> [])
// distinction is preserved deliberately; see event/write.go.
func ResolveUpdateReminders(mode ReminderMode, specs []string) (*event.RemindersUpdate, error) {
	switch mode {
	case ReminderKeep:
		return nil, nil
	case ReminderInherit:
		return &event.RemindersUpdate{Inherit: true}, nil
	case ReminderNone:
		return &event.RemindersUpdate{List: nil}, nil
	case ReminderCustom:
		list, err := parseCustom(specs)
		if err != nil {
			return nil, err
		}
		return &event.RemindersUpdate{List: list}, nil
	default:
		return nil, fmt.Errorf("invalid reminders mode %d", mode)
	}
}

// ResolveCreateReminders turns a mode + specs into the create-side (list, set)
// pair: inherit -> (nil, false); none -> (nil, true); custom -> (list, true).
// ReminderKeep is treated as inherit (a create with no reminders inherits).
func ResolveCreateReminders(mode ReminderMode, specs []string) (list []caltypes.Notification, set bool, err error) {
	switch mode {
	case ReminderKeep, ReminderInherit:
		return nil, false, nil
	case ReminderNone:
		return nil, true, nil
	case ReminderCustom:
		list, err = parseCustom(specs)
		if err != nil {
			return nil, false, err
		}
		return list, true, nil
	default:
		return nil, false, fmt.Errorf("invalid reminders mode %d", mode)
	}
}

func parseCustom(specs []string) ([]caltypes.Notification, error) {
	list, err := reminders.ParseList(specs)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, errors.New("custom reminders mode requires at least one reminder")
	}
	return list, nil
}

// ResolveColorCreate resolves a create-side color spec to a palette hex.
// "" or "default" return "" (a create with no color inherits the calendar's).
func ResolveColorCreate(spec string) (string, error) {
	if spec == "" || calcolor.IsDefault(spec) {
		return "", nil
	}
	return calcolor.Resolve(spec)
}

// ResolveColorUpdate resolves an update-side color spec to the change intent.
// "default" reverts to the calendar color; else the spec is a name/hex. The
// caller decides whether a color was supplied at all (nil = keep).
func ResolveColorUpdate(spec string) (*ColorUpdate, error) {
	if calcolor.IsDefault(spec) {
		return &ColorUpdate{Inherit: true}, nil
	}
	hex, err := calcolor.Resolve(spec)
	if err != nil {
		return nil, err
	}
	return &ColorUpdate{Hex: hex}, nil
}
