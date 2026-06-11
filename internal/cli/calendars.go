package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
)

// calendarJSON is the machine-readable shape of one calendar.
type calendarJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Color       string `json:"color"`
	Type        int    `json:"type"`
	IsDefault   bool   `json:"is_default"`
}

// calTypeString maps a calendar type to its display name.
func calTypeString(t int) string {
	switch t {
	case 0:
		return "normal"
	case 1:
		return "subscribed"
	case 2:
		return "holidays"
	default:
		return fmt.Sprintf("type %d", t)
	}
}

// isDefaultCalendar reports whether cal matches the configured default
// calendar selector (by ID or case-insensitive name).
func isDefaultCalendar(cal calendar.Info, defaultSelector string) bool {
	if defaultSelector == "" {
		return false
	}
	return cal.ID == defaultSelector || strings.EqualFold(cal.Name, defaultSelector)
}

func newCalendarsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "calendars",
		Short: "List all calendars",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := newApp()
			if err != nil {
				return err
			}
			cals, err := calendar.List(cmd.Context(), a.client)
			if err != nil {
				return err
			}

			if jsonOutput {
				rows := make([]calendarJSON, 0, len(cals))
				for _, c := range cals {
					rows = append(rows, calendarJSON{
						ID:          c.ID,
						Name:        c.Name,
						Description: c.Description,
						Color:       c.Color,
						Type:        c.Type,
						IsDefault:   isDefaultCalendar(c, a.cfg.DefaultCalendar),
					})
				}
				return printJSON(rows)
			}

			w := humanOut()
			if len(cals) == 0 {
				fmt.Fprintln(w, "No calendars found.")
				return nil
			}
			for _, c := range cals {
				marker := ""
				if isDefaultCalendar(c, a.cfg.DefaultCalendar) {
					marker = "  [default]"
				}
				fmt.Fprintf(w, "%s (%s)%s\n", c.Name, calTypeString(c.Type), marker)
				fmt.Fprintf(w, "  ID: %s\n", c.ID)
				fmt.Fprintf(w, "  Color: %s\n", c.Color)
				if c.Description != "" {
					fmt.Fprintf(w, "  Description: %s\n", c.Description)
				}
			}
			return nil
		},
	}
}

func init() {
	rootCmd.AddCommand(newCalendarsCmd())
}
