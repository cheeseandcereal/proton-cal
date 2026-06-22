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

	"github.com/cheeseandcereal/proton-cal/pkg/papi"
)

// APIPath is the route prefix of the Proton Calendar API.
const APIPath = "/calendar/v1"

// UserSettingsPath is the per-account calendar user-settings endpoint
// carrying DefaultCalendarID. GET reads it; PUT writes a partial update.
const UserSettingsPath = "/settings/calendar"

// userSettingsResponse is the wire shape of GET /settings/calendar.
type userSettingsResponse struct {
	CalendarUserSettings struct {
		DefaultCalendarID string `json:"DefaultCalendarID"`
	} `json:"CalendarUserSettings"`
}

// DefaultCalendarID fetches the account's server-side default calendar ID
// (empty when unset); the source of truth for display and selector defaults.
func DefaultCalendarID(ctx context.Context, client papi.API) (string, error) {
	var resp userSettingsResponse
	if err := client.Get(ctx, UserSettingsPath, nil, &resp); err != nil {
		return "", fmt.Errorf("fetching calendar user settings: %w", err)
	}
	return resp.CalendarUserSettings.DefaultCalendarID, nil
}

// SetDefaultCalendarID sets the account's server-side default calendar via a
// partial PUT to /settings/calendar.
func SetDefaultCalendarID(ctx context.Context, client papi.API, calendarID string) error {
	body := map[string]any{"DefaultCalendarID": calendarID}
	if err := client.Put(ctx, UserSettingsPath, body, nil); err != nil {
		return fmt.Errorf("setting default calendar: %w", err)
	}
	return nil
}

// apiCalendar is one entry of GET /calendar/v1. Display metadata lives on the
// per-user member entry; legacy top-level fields are kept as fallback.
type apiCalendar struct {
	ID          string      `json:"ID"`
	Type        int         `json:"Type"` // 0 = normal, 1 = subscribed (2 observed: holidays)
	CreateTime  int64       `json:"CreateTime"`
	Members     []apiMember `json:"Members"`
	Name        string      `json:"Name,omitempty"`        // legacy top-level fallback
	Description string      `json:"Description,omitempty"` // legacy top-level fallback
	Color       string      `json:"Color,omitempty"`       // legacy top-level fallback
}

// apiMember is a member entry on a calendar (the per-user view).
type apiMember struct {
	ID          string `json:"ID"`
	AddressID   string `json:"AddressID"`
	CalendarID  string `json:"CalendarID"`
	Email       string `json:"Email"`
	Name        string `json:"Name"`
	Description string `json:"Description"`
	Color       string `json:"Color"`
	Display     int    `json:"Display"`
	Permissions int    `json:"Permissions"`
	Flags       int    `json:"Flags"`
}

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

// TypeString returns the calendar type's display name.
func (i Info) TypeString() string {
	switch i.Type {
	case 0:
		return "normal"
	case 1:
		return "subscribed"
	case 2:
		return "holidays"
	default:
		return fmt.Sprintf("type %d", i.Type)
	}
}

// Matches reports whether selector addresses this calendar: by exact ID or
// case-insensitive name. An empty selector matches nothing.
func (i Info) Matches(selector string) bool {
	return selector != "" && (i.ID == selector || strings.EqualFold(i.Name, selector))
}

// listResponse is the wire shape of GET /calendar/v1.
type listResponse struct {
	Calendars []apiCalendar `json:"Calendars"`
}

// List fetches and resolves all calendars on the account.
func List(ctx context.Context, client papi.API) ([]Info, error) {
	var resp listResponse
	if err := client.Get(ctx, APIPath, nil, &resp); err != nil {
		return nil, fmt.Errorf("listing calendars: %w", err)
	}
	infos := make([]Info, 0, len(resp.Calendars))
	for _, cal := range resp.Calendars {
		infos = append(infos, newInfo(cal))
	}
	return infos, nil
}

// newInfo resolves one calendar list entry, taking display metadata from our
// per-user member entry with legacy top-level fields as fallback.
func newInfo(cal apiCalendar) Info {
	var member apiMember
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

// Resolve picks a calendar: a non-empty selector matches by exact ID then
// unique case-insensitive name; an empty selector uses defaultID (if listed)
// else the first calendar. Errors cover none/no-match/ambiguous cases.
func Resolve(cals []Info, selector, defaultID string) (Info, error) {
	if len(cals) == 0 {
		return Info{}, errors.New("no calendars found on this account")
	}

	if selector == "" {
		// Empty selector: prefer the server default when it resolves to a
		// listed calendar; otherwise fall back to the first calendar.
		if defaultID != "" {
			for _, c := range cals {
				if c.ID == defaultID {
					return c, nil
				}
			}
		}
		return cals[0], nil
	}

	sel := selector
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
