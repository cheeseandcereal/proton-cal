package calsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cheeseandcereal/proton-cal/pkg/auth"
	"github.com/cheeseandcereal/proton-cal/pkg/calcolor"
	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
)

// UpdateCalendarInput describes a partial update to a calendar's metadata/settings
// plus an optional make-default request. Pointer fields nil = leave unchanged; an
// empty string clears Description, but Name/Color must be non-empty.
type UpdateCalendarInput struct {
	Selector string

	// Metadata (per-member): nil = keep.
	Name        *string
	Description *string
	Color       *string // a Proton color name or hex; validated to a palette hex

	// Default settings: nil = keep.
	DefaultDuration  *int
	PartDayReminders *[]caltypes.Notification
	FullDayReminders *[]caltypes.Notification
	MakesUserBusy    *bool

	// MakeDefault sets this calendar as the account's default calendar.
	MakeDefault bool
}

func (in UpdateCalendarInput) hasMetadata() bool {
	return in.Name != nil || in.Description != nil || in.Color != nil
}

func (in UpdateCalendarInput) hasSettings() bool {
	return in.DefaultDuration != nil || in.PartDayReminders != nil ||
		in.FullDayReminders != nil || in.MakesUserBusy != nil
}

// UpdateCalendar applies metadata/settings/default changes and returns refreshed
// detail. Only owned (normal) calendars can be modified. Applied in order
// (metadata, settings, default); a later failure leaves earlier changes standing.
func (s *Service) UpdateCalendar(ctx context.Context, in UpdateCalendarInput) (*GotCalendar, error) {
	if !in.hasMetadata() && !in.hasSettings() && !in.MakeDefault {
		return nil, errors.New("nothing to update")
	}

	// Resolve color to a canonical palette hex up front (fail before any write).
	// A calendar has no inheritable default, so the "default" sentinel is rejected.
	var colorHex *string
	if in.Color != nil {
		if *in.Color == "" || calcolor.IsDefault(*in.Color) {
			return nil, errors.New(`--color requires a Proton color name or hex (a calendar has no inheritable default color)`)
		}
		hex, err := calcolor.Resolve(*in.Color)
		if err != nil {
			return nil, err
		}
		colorHex = &hex
	}

	info, err := s.resolveCalendar(ctx, in.Selector)
	if err != nil {
		return nil, err
	}
	if info.Type != 0 {
		return nil, fmt.Errorf("cannot modify calendar %q: only owned (normal) calendars can be updated, this is a %s calendar", info.Name, info.TypeString())
	}

	// Apply metadata first, then settings, then default. Each guarded so a
	// failure reports what had already been applied.
	if in.hasMetadata() {
		patch := calendar.MemberPatch{Name: in.Name, Description: in.Description, Color: colorHex}
		if err := calendar.UpdateMember(ctx, s.api, info.ID, info.MemberID, patch); err != nil {
			return nil, err
		}
		s.invalidateCalendarList()
	}

	if in.hasSettings() {
		patch := calendar.SettingsPatch{
			DefaultEventDuration: in.DefaultDuration,
			PartDayNotifications: in.PartDayReminders,
			FullDayNotifications: in.FullDayReminders,
			MakesUserBusy:        in.MakesUserBusy,
		}
		if err := calendar.UpdateSettings(ctx, s.api, info.ID, patch); err != nil {
			return nil, fmt.Errorf("%w (metadata changes, if any, were applied)", err)
		}
		s.invalidateCalendarKeys(info.ID)
	}

	if in.MakeDefault {
		if err := calendar.SetDefaultCalendarID(ctx, s.api, info.ID); err != nil {
			return nil, fmt.Errorf("%w (other changes, if any, were applied)", err)
		}
		s.invalidateCache(calendar.UserSettingsPath)
	}

	// Return refreshed detail without a key unlock: the touched caches are now
	// invalidated, so resolveCalendar and FetchSettings read fresh.
	return s.refreshedCalendar(ctx, info.ID)
}

// refreshedCalendar re-reads a calendar's info and default settings (fresh,
// since callers invalidate the relevant caches first) and reports whether it
// is the account default. Shared by Create/UpdateCalendar's return tail.
func (s *Service) refreshedCalendar(ctx context.Context, id string) (*GotCalendar, error) {
	refreshed, err := s.resolveCalendar(ctx, id)
	if err != nil {
		return nil, err
	}
	set, err := calendar.FetchSettings(ctx, s.api, id)
	if err != nil {
		return nil, err
	}
	defaultID, _ := s.DefaultCalendarID(ctx)
	return &GotCalendar{
		Info:      refreshed,
		Settings:  set,
		IsDefault: defaultID != "" && refreshed.ID == defaultID,
	}, nil
}

// maxCalendarNameLen and maxCalendarDescriptionLen mirror the web client's
// MAX_CHARS_API (CALENDAR_NAME / CALENDAR_DESCRIPTION); enforced client-side for
// a clean error before the round-trip.
const (
	maxCalendarNameLen        = 100
	maxCalendarDescriptionLen = 255
)

// CreateCalendarInput describes a new owned calendar. Name is required; Color
// is a Proton color name or hex (empty = a random palette color, as the web
// client does). MakeDefault also sets it as the account's default calendar.
type CreateCalendarInput struct {
	Name        string
	Description string
	Color       string
	MakeDefault bool
}

// CreateCalendar creates a new owned calendar and returns its refreshed detail.
// It resolves the color (random when unset), unlocks the account keys to pick
// the signing address, then performs the two-step create (metadata + keys). On
// a key-setup failure the (keyless) calendar's ID is reported via the wrapped
// error so it can be completed or removed.
func (s *Service) CreateCalendar(ctx context.Context, in CreateCalendarInput) (*GotCalendar, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, errors.New("a calendar name is required")
	}
	if len(name) > maxCalendarNameLen {
		return nil, fmt.Errorf("calendar name is too long (%d chars; max %d)", len(name), maxCalendarNameLen)
	}
	if len(in.Description) > maxCalendarDescriptionLen {
		return nil, fmt.Errorf("calendar description is too long (%d chars; max %d)", len(in.Description), maxCalendarDescriptionLen)
	}

	// Resolve the color up front (fail before any write). A calendar has no
	// inheritable color, so the "default" sentinel is rejected; an empty color
	// gets a random palette color like the web client.
	var colorHex string
	switch {
	case in.Color == "":
		colorHex = calcolor.RandomHex()
	case calcolor.IsDefault(in.Color):
		return nil, errors.New(`--color requires a Proton color name or hex (a calendar has no inheritable default color)`)
	default:
		hex, err := calcolor.Resolve(in.Color)
		if err != nil {
			return nil, err
		}
		colorHex = hex
	}

	kc, err := s.keychainLazy(ctx)
	if err != nil {
		return nil, err
	}
	addressID, addrKR, err := kc.SigningAddress()
	if err != nil {
		return nil, err
	}

	info, err := calendar.Create(ctx, s.api, calendar.CreateInput{
		Name:        name,
		Description: in.Description,
		ColorHex:    colorHex,
		AddressID:   addressID,
		AddrKR:      addrKR,
	})
	if err != nil {
		return nil, err
	}

	// The new calendar must appear in subsequent resolutions/lists.
	s.invalidateCalendarList()

	if in.MakeDefault {
		if err := calendar.SetDefaultCalendarID(ctx, s.api, info.ID); err != nil {
			return nil, fmt.Errorf("calendar %q was created but could not be made the default: %w", info.Name, err)
		}
		s.invalidateCache(calendar.UserSettingsPath)
	}

	// Return refreshed detail without a key unlock: the bootstrap already
	// carries the server-applied default settings for the new calendar.
	return s.refreshedCalendar(ctx, info.ID)
}

// DeleteCalendarInput describes a calendar deletion. Password (the login password)
// is required only for owned (normal) calendars, which need the elevated "locked"
// scope; it is ignored for managed (holidays) calendars.
type DeleteCalendarInput struct {
	Selector string
	Password string
}

// ResolveCalendarInfo resolves a selector to a calendar's Info without unlocking
// keys. Used for pre-delete dry runs and to decide whether a password is needed.
func (s *Service) ResolveCalendarInfo(ctx context.Context, selector string) (calendar.Info, error) {
	return s.resolveCalendar(ctx, selector)
}

// DeleteCalendar removes a calendar. Owned (normal) calendars need the elevated
// scope (re-prove the login password via SRP); managed (holidays) use the managed
// route without a password. Subscribed calendars cannot be deleted this way.
func (s *Service) DeleteCalendar(ctx context.Context, in DeleteCalendarInput) error {
	info, err := s.resolveCalendar(ctx, in.Selector)
	if err != nil {
		return err
	}

	switch info.Type {
	case 0: // owned/normal: needs the locked scope (login password)
		if in.Password == "" {
			return errors.New("deleting a calendar requires re-authentication: provide the login password")
		}
		username, err := s.loginUsername(ctx)
		if err != nil {
			return err
		}
		werr := auth.WithLockedScope(ctx, s.client.Manager(), s.client, username, in.Password, func() error {
			return calendar.DeleteCalendar(ctx, s.api, info.ID, false)
		})
		if werr != nil {
			return werr
		}
	case 2: // backend-managed (holidays): managed route, no password
		if err := calendar.DeleteCalendar(ctx, s.api, info.ID, true); err != nil {
			return err
		}
	default: // subscribed (1) or unknown
		return fmt.Errorf("cannot delete calendar %q: it is a %s calendar (unsubscribe in the Proton app)", info.Name, info.TypeString())
	}

	s.invalidateCalendarList()
	return nil
}
