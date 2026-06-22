//go:build integration

package calsvc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/eventview"
)

// TestE2ECustomRemindersAndColor exercises the full reminders/color write
// path live: create with custom reminders + color, read them back, then walk
// the update tri-state (custom -> none -> inherit) and color (set -> inherit).
func TestE2ECustomRemindersAndColor(t *testing.T) {
	svc, cal := liveService(t)
	ctx := context.Background()

	start, end := e2eFutureSlot()
	custom := []caltypes.Notification{
		{Type: 1, Trigger: "-PT15M"},
		{Type: 0, Trigger: "-PT1H"},
	}
	created, err := svc.CreateEvent(ctx, CreateEventInput{
		Summary: e2eSummary("custom-reminders"), Start: start, End: end, Calendar: cal, TZ: "UTC",
		Reminders: custom, RemindersSet: true, Color: "#EC3E7C",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	defer func() { _, _ = svc.DeleteEvent(ctx, DeleteEventInput{EventID: created.ID, Calendar: cal}) }()

	// Read back: custom reminders are the event's own (set), color matches.
	// The server may reorder the array, so assert on set membership.
	got := getEvt(t, svc, cal, created.ID)
	if !got.Event.NotificationsSet || len(got.Event.Notifications) != 2 {
		t.Fatalf("created reminders not stored: set=%v %+v", got.Event.NotificationsSet, got.Event.Notifications)
	}
	if !hasReminder(got.Event.Notifications, 1, "-PT15M") || !hasReminder(got.Event.Notifications, 0, "-PT1H") {
		t.Errorf("reminders round trip = %+v, want -PT15M (notify) + -PT1H (email)", got.Event.Notifications)
	}
	if got.Event.Color != "#EC3E7C" {
		t.Errorf("color = %q, want #EC3E7C", got.Event.Color)
	}

	// Update to explicit none.
	if _, err := svc.UpdateEvent(ctx, UpdateEventInput{
		EventID: created.ID, Calendar: cal,
		Reminders: &event.RemindersUpdate{List: nil},
	}); err != nil {
		t.Fatalf("UpdateEvent none: %v", err)
	}
	got = getEvt(t, svc, cal, created.ID)
	if !got.Event.NotificationsSet || len(got.Event.Notifications) != 0 {
		t.Errorf("after none: set=%v %+v, want explicit empty", got.Event.NotificationsSet, got.Event.Notifications)
	}

	// Update to inherit (calendar default).
	if _, err := svc.UpdateEvent(ctx, UpdateEventInput{
		EventID: created.ID, Calendar: cal,
		Reminders: &event.RemindersUpdate{Inherit: true},
	}); err != nil {
		t.Fatalf("UpdateEvent inherit: %v", err)
	}
	got = getEvt(t, svc, cal, created.ID)
	if got.Event.NotificationsSet {
		t.Errorf("after inherit: NotificationsSet should be false, got %+v", got.Event.Notifications)
	}
	// Effective reminders now equal the calendar default for a timed event.
	wantDefault := got.Settings.DefaultNotifications(false)
	eff := eventview.EffectiveReminders(got.Event, got.Settings)
	if len(eff) != len(wantDefault) {
		t.Errorf("effective reminders after inherit = %+v, want calendar default %+v", eff, wantDefault)
	}

	// Revert color to the calendar default. Proton has no "clear" for color:
	// reverting sets the event color explicitly to the calendar's own color,
	// so the event color ends up equal to the calendar color (not empty).
	calInfo, err := svc.GetCalendar(ctx, cal)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if _, err := svc.UpdateEvent(ctx, UpdateEventInput{
		EventID: created.ID, Calendar: cal,
		Color: &ColorUpdate{Inherit: true},
	}); err != nil {
		t.Fatalf("UpdateEvent color inherit: %v", err)
	}
	got = getEvt(t, svc, cal, created.ID)
	if got.Event.Color != calInfo.Info.Color {
		t.Errorf("color after revert = %q, want calendar color %q", got.Event.Color, calInfo.Info.Color)
	}
}

// TestE2EOccurrenceInheritsReminders is a regression guard: a fresh
// single-occurrence edit of a series with custom reminders/color must keep
// the master's reminders/color (the exception row is a CREATE that used to
// silently revert to inherit).
func TestE2EOccurrenceInheritsReminders(t *testing.T) {
	svc, cal := liveService(t)
	ctx := context.Background()

	date := e2eFutureDate()
	start := date + " 09:00"
	end := date + " 09:30"
	created, err := svc.CreateEvent(ctx, CreateEventInput{
		Summary: e2eSummary("occ-reminders"), Start: start, End: end, Calendar: cal, TZ: "UTC",
		Reminders:    []caltypes.Notification{{Type: 1, Trigger: "-PT30M"}},
		RemindersSet: true,
		Color:        "#415DF0",
		Recurrence:   Recurrence{Repeat: "daily", Count: 3},
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	defer func() { _, _ = svc.DeleteEvent(ctx, DeleteEventInput{EventID: created.ID, Calendar: cal}) }()

	// Edit the SECOND occurrence's summary only (no reminder/color flags).
	// Format the occurrence start with the canonical formatter so it parses
	// back to the exact same instant the server expanded.
	starts := occurrencesForUID(t, svc, cal, created.UID, date+" 00:00", 5)
	if len(starts) < 2 {
		t.Fatalf("expected >=2 occurrences, got %d", len(starts))
	}
	occStart := FormatOccurrenceStart(starts[1], false, time.UTC)
	newSummary := e2eSummary("occ-edited")
	// TZ:"UTC" so the occurrence string (formatted in UTC above) parses back
	// to the same instant; otherwise parseOccurrence uses the configured zone.
	if _, err := svc.UpdateEvent(ctx, UpdateEventInput{
		EventID: created.ID, Calendar: cal, TZ: "UTC", Occurrence: occStart, Summary: &newSummary,
	}); err != nil {
		t.Fatalf("UpdateEvent occurrence: %v", err)
	}

	// Find the edited exception row and assert it kept the master's reminders.
	list, err := svc.ListEvents(ctx, ListEventsInput{Calendar: cal, TZ: "UTC", From: date + " 00:00", Days: 5})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var edited *event.Event
	for _, l := range list.Items {
		if l.Event != nil && l.Event.UID == created.UID && !l.Event.RecurrenceID.IsZero() {
			edited = l.Event
			break
		}
	}
	if edited == nil {
		t.Fatal("edited exception row not found")
	}
	if !edited.NotificationsSet || len(edited.Notifications) != 1 || edited.Notifications[0].Trigger != "-PT30M" {
		t.Errorf("exception did not inherit master reminders: set=%v %+v", edited.NotificationsSet, edited.Notifications)
	}
	if edited.Color != "#415DF0" {
		t.Errorf("exception did not inherit master color: %q", edited.Color)
	}
}

// TestE2ERemindersInICSExport verifies a custom reminder materializes as a
// VALARM with the right TRIGGER in the ICS export.
func TestE2ERemindersInICSExport(t *testing.T) {
	svc, cal := liveService(t)
	ctx := context.Background()

	start, end := e2eFutureSlot()
	created, err := svc.CreateEvent(ctx, CreateEventInput{
		Summary: e2eSummary("ics-reminder"), Start: start, End: end, Calendar: cal, TZ: "UTC",
		Reminders: []caltypes.Notification{{Type: 1, Trigger: "-PT45M"}}, RemindersSet: true,
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	defer func() { _, _ = svc.DeleteEvent(ctx, DeleteEventInput{EventID: created.ID, Calendar: cal}) }()

	got, err := svc.GetEvent(ctx, GetEventInput{EventID: created.ID, Calendar: cal, WithICS: true})
	if err != nil {
		t.Fatalf("GetEvent WithICS: %v", err)
	}
	if !strings.Contains(got.ICS, "BEGIN:VALARM") || !strings.Contains(got.ICS, "TRIGGER:-PT45M") {
		t.Errorf("ICS export missing VALARM/TRIGGER:\n%s", got.ICS)
	}
}

// hasReminder reports whether ns contains a notification with the given type
// and trigger (order-independent; the server may reorder the array).
func hasReminder(ns []caltypes.Notification, typ int, trigger string) bool {
	for _, n := range ns {
		if n.Type == typ && n.Trigger == trigger {
			return true
		}
	}
	return false
}

// getEvt fetches and fails on error.
func getEvt(t *testing.T, svc *Service, cal, id string) *GotEvent {
	t.Helper()
	got, err := svc.GetEvent(context.Background(), GetEventInput{EventID: id, Calendar: cal, TZ: "UTC"})
	if err != nil {
		t.Fatalf("GetEvent(%s): %v", id, err)
	}
	return got
}
