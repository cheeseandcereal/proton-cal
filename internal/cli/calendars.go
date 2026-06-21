package cli

import (
	"fmt"

	"io"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
)

func newCalendarsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "calendars",
		Short: "List all calendars",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := newService()
			if err != nil {
				return err
			}
			defer svc.Close()

			cals, err := svc.Calendars(cmd.Context())
			if err != nil {
				return err
			}
			defaultSel := svc.DefaultCalendarSelector()

			if outputJSON() {
				return printJSON(calendarsJSON(cals, defaultSel))
			}
			renderCalendars(humanOut(), cals, defaultSel)
			return nil
		},
	}
}

// calendarJSON is the machine-readable shape of one calendar.
type calendarJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Color       string `json:"color"`
	Type        int    `json:"type"`
	IsDefault   bool   `json:"is_default"`
	Email       string `json:"email,omitempty"`
	MemberID    string `json:"member_id,omitempty"`
	AddressID   string `json:"address_id,omitempty"`
}

func calendarJSONOf(c calendar.Info, isDefault bool) calendarJSON {
	return calendarJSON{
		ID:          c.ID,
		Name:        c.Name,
		Description: c.Description,
		Color:       c.Color,
		Type:        c.Type,
		IsDefault:   isDefault,
		Email:       c.Email,
		MemberID:    c.MemberID,
		AddressID:   c.AddressID,
	}
}

func calendarsJSON(cals []calendar.Info, defaultSel string) []calendarJSON {
	rows := make([]calendarJSON, 0, len(cals))
	for _, c := range cals {
		rows = append(rows, calendarJSONOf(c, c.Matches(defaultSel)))
	}
	return rows
}

func renderCalendars(w io.Writer, cals []calendar.Info, defaultSel string) {
	if len(cals) == 0 {
		fmt.Fprintln(w, "No calendars found.")
		return
	}
	for _, c := range cals {
		marker := ""
		if c.Matches(defaultSel) {
			marker = "  [default]"
		}
		fmt.Fprintf(w, "%s (%s)%s\n", c.Name, c.TypeString(), marker)
		fmt.Fprintf(w, "  ID: %s\n", c.ID)
		fmt.Fprintf(w, "  Color: %s%s\n", swatch(c.Color), c.Color)
		if c.Description != "" {
			fmt.Fprintf(w, "  Description: %s\n", c.Description)
		}
	}
}
