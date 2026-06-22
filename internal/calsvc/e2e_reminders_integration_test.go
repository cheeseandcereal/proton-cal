//go:build integration

package calsvc

import (
	"context"
	"reflect"
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/eventview"
)

// TestE2ERemindersInherit verifies the reminder inheritance tri-state end to
// end: an event created through the service carries no reminders of its own
// (the API is sent Notifications=null), so its EFFECTIVE reminders must equal
// the calendar's default set for its kind. A field-only update must not
// freeze a copy of the defaults onto the event (NotificationsSet stays
// false), preserving inheritance. This guards the behavior the Proton
// clients rely on against API/library drift.
func TestE2ERemindersInherit(t *testing.T) {
	svc, cal := liveService(t)
	ctx := context.Background()

	start, end := e2eFutureSlot()
	summary := e2eSummary("reminders-inherit")
	created, err := svc.CreateEvent(ctx, CreateEventInput{
		Summary: summary, Start: start, End: end, Calendar: cal, TZ: "UTC",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	defer func() { _, _ = svc.DeleteEvent(ctx, DeleteEventInput{EventID: created.ID, Calendar: cal}) }()

	got, err := svc.GetEvent(ctx, GetEventInput{EventID: created.ID, Calendar: cal, TZ: "UTC"})
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.Event.NotificationsSet {
		t.Error("freshly created event must inherit reminders (NotificationsSet=false)")
	}
	want := got.Settings.DefaultNotifications(false) // timed event
	eff := eventview.EffectiveReminders(got.Event, got.Settings)
	if !reflect.DeepEqual(eff, want) {
		t.Errorf("effective reminders = %+v, want calendar default %+v", eff, want)
	}

	// A field-only update must keep inheritance intact.
	renamed := e2eSummary("reminders-renamed")
	if _, err := svc.UpdateEvent(ctx, UpdateEventInput{EventID: created.ID, Calendar: cal, Summary: &renamed}); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	got, err = svc.GetEvent(ctx, GetEventInput{EventID: created.ID, Calendar: cal, TZ: "UTC"})
	if err != nil {
		t.Fatalf("GetEvent after update: %v", err)
	}
	if got.Event.NotificationsSet {
		t.Error("field-only update froze reminders onto the event (inheritance broken)")
	}
}
