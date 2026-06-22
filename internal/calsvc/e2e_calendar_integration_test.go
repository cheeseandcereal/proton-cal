//go:build integration

package calsvc

import (
	"context"
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
)

// TestE2EUpdateCalendar drives the calendar update path live: changes name,
// color, duration, reminders and the account default, then restores all.
// Fully self-cleaning (no sweep needed).
func TestE2EUpdateCalendar(t *testing.T) {
	svc, calSel := liveService(t)
	ctx := context.Background()

	orig, err := svc.GetCalendar(ctx, calSel)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	origDefaultID, err := svc.DefaultCalendarID(ctx)
	if err != nil {
		t.Fatalf("DefaultCalendarID: %v", err)
	}

	// Restore everything at the end, including the account default.
	t.Cleanup(func() {
		origPart := orig.Settings.DefaultPartDayNotifications
		origFull := orig.Settings.DefaultFullDayNotifications
		dur := orig.Settings.DefaultEventDuration
		busy := orig.Settings.MakesUserBusy != 0
		if _, err := svc.UpdateCalendar(context.Background(), UpdateCalendarInput{
			Selector:         orig.Info.ID,
			Name:             &orig.Info.Name,
			Color:            &orig.Info.Color,
			DefaultDuration:  &dur,
			MakesUserBusy:    &busy,
			PartDayReminders: &origPart,
			FullDayReminders: &origFull,
		}); err != nil {
			t.Logf("cleanup: restore calendar: %v", err)
		}
		if origDefaultID != "" {
			if err := setDefaultCalendar(context.Background(), svc, origDefaultID); err != nil {
				t.Logf("cleanup: restore default calendar: %v", err)
			}
		}
	})

	newName := orig.Info.Name + " (e2e)"
	newColor := "#54473F" // a distinct palette color (soil)
	if newColor == orig.Info.Color {
		newColor = "#5252CC" // cobalt, if the original happened to be soil
	}
	dur := 45
	part := []caltypes.Notification{{Type: 1, Trigger: "-PT20M"}}

	got, err := svc.UpdateCalendar(ctx, UpdateCalendarInput{
		Selector:         calSel,
		Name:             &newName,
		Color:            &newColor,
		DefaultDuration:  &dur,
		PartDayReminders: &part,
		MakeDefault:      true,
	})
	if err != nil {
		t.Fatalf("UpdateCalendar: %v", err)
	}
	if got.Info.Name != newName {
		t.Errorf("name = %q, want %q", got.Info.Name, newName)
	}
	if got.Info.Color != newColor {
		t.Errorf("color = %q, want %q", got.Info.Color, newColor)
	}
	if got.Settings.DefaultEventDuration != dur {
		t.Errorf("duration = %d, want %d", got.Settings.DefaultEventDuration, dur)
	}
	if !got.IsDefault {
		t.Error("calendar should now be the account default")
	}
	if len(got.Settings.DefaultPartDayNotifications) != 1 ||
		got.Settings.DefaultPartDayNotifications[0].Trigger != "-PT20M" {
		t.Errorf("part-day reminders = %+v, want one -PT20M", got.Settings.DefaultPartDayNotifications)
	}

	// Refetch confirms persistence (not just the echo).
	refetched, err := svc.GetCalendar(ctx, calSel)
	if err != nil {
		t.Fatalf("GetCalendar (refetch): %v", err)
	}
	if refetched.Info.Name != newName || refetched.Settings.DefaultEventDuration != dur {
		t.Errorf("refetch mismatch: name=%q dur=%d", refetched.Info.Name, refetched.Settings.DefaultEventDuration)
	}
}

// setDefaultCalendar is a tiny test helper around the make-default path.
func setDefaultCalendar(ctx context.Context, svc *Service, calID string) error {
	_, err := svc.UpdateCalendar(ctx, UpdateCalendarInput{Selector: calID, MakeDefault: true})
	return err
}
