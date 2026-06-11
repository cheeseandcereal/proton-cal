// Package calendar implements Proton Calendar listing, selection, and the
// calendar key unlock chain on top of the raw papi client.
//
// All API calls in this package use the raw papi request path: the typed
// go-proton-api calendar endpoints have stale response types (calendar
// Name/Color/Description now live on the per-user member entry, verified
// live against the API in June 2026).
package calendar

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cheeseandcereal/proton-cal-go/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal-go/internal/papi"
)

// Info is a calendar with its display metadata resolved (from the per-user
// member entry, with legacy top-level fallback) plus our member identity.
type Info struct {
	ID          string
	Name        string
	Description string
	Color       string
	Type        int    // 0 = normal, 1 = subscribed, 2 = holidays (observed)
	MemberID    string // our member entry's ID ("" if none)
	AddressID   string // our member entry's AddressID ("" if none)
	Email       string // our member entry's email
}

// listResponse is the wire shape of GET /calendar/v1.
type listResponse struct {
	Calendars []caltypes.Calendar `json:"Calendars"`
}

// List fetches and resolves all calendars on the account.
func List(ctx context.Context, client *papi.Client) ([]Info, error) {
	var resp listResponse
	if err := client.Get(ctx, "/calendar/v1", nil, &resp); err != nil {
		return nil, fmt.Errorf("listing calendars: %w", err)
	}
	infos := make([]Info, 0, len(resp.Calendars))
	for _, cal := range resp.Calendars {
		infos = append(infos, newInfo(cal))
	}
	return infos, nil
}

// newInfo resolves one calendar list entry. The modern API carries
// Name/Description/Color on the per-user member entry (the list endpoint
// returns only OUR member); legacy responses had them top-level.
func newInfo(cal caltypes.Calendar) Info {
	var member caltypes.CalendarMember
	if len(cal.Members) > 0 {
		member = cal.Members[0]
	}
	return Info{
		ID:          cal.ID,
		Name:        firstNonEmpty(member.Name, cal.Name, "Unnamed"),
		Description: firstNonEmpty(member.Description, cal.Description),
		Color:       firstNonEmpty(member.Color, cal.Color, "#000000"),
		Type:        cal.Type,
		MemberID:    member.ID,
		AddressID:   member.AddressID,
		Email:       member.Email,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Resolve picks a calendar: by exact ID match, then case-insensitive unique
// name match, from the given selector; empty selector → defaultSelector
// (same matching); empty both → first calendar. Errors are user-actionable
// (no calendars / no match / ambiguous name listing the candidates).
func Resolve(cals []Info, selector, defaultSelector string) (Info, error) {
	if len(cals) == 0 {
		return Info{}, errors.New("no calendars found on this account")
	}

	sel := selector
	if sel == "" {
		sel = defaultSelector
	}
	if sel == "" {
		return cals[0], nil
	}

	for _, c := range cals {
		if c.ID == sel {
			return c, nil
		}
	}

	var matches []Info
	for _, c := range cals {
		if strings.EqualFold(c.Name, sel) {
			matches = append(matches, c)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		names := make([]string, len(cals))
		for i, c := range cals {
			names[i] = fmt.Sprintf("%q", c.Name)
		}
		return Info{}, fmt.Errorf("no calendar with ID or name %q; available calendars: %s",
			sel, strings.Join(names, ", "))
	default:
		candidates := make([]string, len(matches))
		for i, c := range matches {
			candidates[i] = fmt.Sprintf("%q (ID %s)", c.Name, c.ID)
		}
		return Info{}, fmt.Errorf("calendar name %q is ambiguous, select by ID instead; matches: %s",
			sel, strings.Join(candidates, ", "))
	}
}
