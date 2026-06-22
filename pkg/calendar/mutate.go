package calendar

import (
	"context"
	"errors"
	"fmt"

	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/papi"
)

// codeNotValidProtonColor is returned by the API (HTTP 400) when a calendar
// color is not one of Proton's fixed accent-palette values.
const codeNotValidProtonColor = 2011

// MemberPatch is a partial update to a calendar's per-user member metadata
// (name, description, color). Only non-nil fields are sent.
type MemberPatch struct {
	Name        *string
	Description *string
	Color       *string
}

// empty reports whether the patch would change nothing.
func (p MemberPatch) empty() bool {
	return p.Name == nil && p.Description == nil && p.Color == nil
}

// UpdateMember applies a partial metadata update to our member entry via
// PUT /calendar/v1/{calID}/members/{memberID}. A no-op patch is rejected.
func UpdateMember(ctx context.Context, client papi.API, calendarID, memberID string, patch MemberPatch) error {
	if patch.empty() {
		return errors.New("no member fields to update")
	}
	body := map[string]any{}
	if patch.Name != nil {
		body["Name"] = *patch.Name
	}
	if patch.Description != nil {
		body["Description"] = *patch.Description
	}
	if patch.Color != nil {
		body["Color"] = *patch.Color
	}
	path := APIPath + "/" + calendarID + "/members/" + memberID
	if err := client.Put(ctx, path, body, nil); err != nil {
		if papi.IsCode(err, codeNotValidProtonColor) {
			return fmt.Errorf("calendar color must be a Proton palette color: %w", err)
		}
		return fmt.Errorf("updating calendar metadata: %w", err)
	}
	return nil
}

// SettingsPatch is a partial update to a calendar's default settings; only
// non-nil fields are sent (PUT .../settings is a partial update, not replace).
type SettingsPatch struct {
	DefaultEventDuration *int
	PartDayNotifications *[]caltypes.Notification
	FullDayNotifications *[]caltypes.Notification
	MakesUserBusy        *bool
}

// empty reports whether the patch would change nothing.
func (p SettingsPatch) empty() bool {
	return p.DefaultEventDuration == nil && p.PartDayNotifications == nil &&
		p.FullDayNotifications == nil && p.MakesUserBusy == nil
}

// UpdateSettings applies a partial settings update via
// PUT /calendar/v1/{calID}/settings. A no-op patch is rejected.
func UpdateSettings(ctx context.Context, client papi.API, calendarID string, patch SettingsPatch) error {
	if patch.empty() {
		return errors.New("no settings fields to update")
	}
	body := map[string]any{}
	if patch.DefaultEventDuration != nil {
		body["DefaultEventDuration"] = *patch.DefaultEventDuration
	}
	if patch.PartDayNotifications != nil {
		body["DefaultPartDayNotifications"] = notificationsBody(*patch.PartDayNotifications)
	}
	if patch.FullDayNotifications != nil {
		body["DefaultFullDayNotifications"] = notificationsBody(*patch.FullDayNotifications)
	}
	if patch.MakesUserBusy != nil {
		body["MakesUserBusy"] = boolToInt(*patch.MakesUserBusy)
	}
	path := APIPath + "/" + calendarID + "/settings"
	if err := client.Put(ctx, path, body, nil); err != nil {
		return fmt.Errorf("updating calendar settings: %w", err)
	}
	return nil
}

// DeleteCalendar removes a calendar: owned via DELETE /calendar/v1/{calID},
// backend-managed (holidays) via .../managed. The routes aren't interchangeable
// (wrong managed route -> code 2011; wrong normal route -> insufficient scope).
func DeleteCalendar(ctx context.Context, client papi.API, calendarID string, managed bool) error {
	path := APIPath + "/" + calendarID
	if managed {
		path += "/managed"
	}
	if err := client.Delete(ctx, path, nil); err != nil {
		return fmt.Errorf("deleting calendar: %w", err)
	}
	return nil
}

// notificationsBody maps notifications to the API wire shape, always returning
// a non-nil slice so JSON serializes as [] (never null) when clearing the set.
func notificationsBody(ns []caltypes.Notification) []map[string]any {
	out := make([]map[string]any, 0, len(ns))
	for _, n := range ns {
		out = append(out, map[string]any{"Type": n.Type, "Trigger": n.Trigger})
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
