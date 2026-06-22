//go:build integration

package calsvc

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/event"
	"github.com/cheeseandcereal/proton-cal/pkg/eventview"
)

// TestE2ECustomRemindersAndColor exercises the reminders/color write path live:
// create with custom values, then walk the tri-state (custom -> none -> inherit).
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

	// Server may reorder the array, so assert on set membership.
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

	// Proton has no "clear" for color: reverting sets the event color to the
	// calendar's own color, so it ends up equal to the calendar color (not empty).
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

// TestE2EOccurrenceInheritsReminders guards a regression: a single-occurrence
// edit must keep the master's reminders/color (the exception-row CREATE used to
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

	// Edit the second occurrence's summary only. Use the canonical formatter so
	// the occurrence start parses back to the exact instant the server expanded.
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

// TestE2ESeriesICSExport verifies GetEvent(WithICS) exports the WHOLE series by
// default (master + a VEVENT per edited occurrence with RECURRENCE-ID), and that
// NoSeries limits export to the single addressed VEVENT.
func TestE2ESeriesICSExport(t *testing.T) {
	svc, cal := liveService(t)
	ctx := context.Background()

	date := e2eFutureDate()
	created, err := svc.CreateEvent(ctx, CreateEventInput{
		Summary: e2eSummary("series-ics"), Start: date + " 09:00", End: date + " 09:30",
		Calendar: cal, TZ: "UTC",
		Reminders: []caltypes.Notification{{Type: 1, Trigger: "-PT30M"}}, RemindersSet: true,
		Recurrence: Recurrence{Repeat: "daily", Count: 3},
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	defer func() { _, _ = svc.DeleteEvent(ctx, DeleteEventInput{EventID: created.ID, Calendar: cal}) }()

	// Edit the second occurrence (creates an exception row sharing the UID).
	starts := occurrencesForUID(t, svc, cal, created.UID, date+" 00:00", 5)
	if len(starts) < 2 {
		t.Fatalf("expected >=2 occurrences, got %d", len(starts))
	}
	occStart := FormatOccurrenceStart(starts[1], false, time.UTC)
	newSummary := e2eSummary("series-ics-edited")
	if _, err := svc.UpdateEvent(ctx, UpdateEventInput{
		EventID: created.ID, Calendar: cal, TZ: "UTC", Occurrence: occStart, Summary: &newSummary,
	}); err != nil {
		t.Fatalf("UpdateEvent occurrence: %v", err)
	}

	// Default --ics: whole series. Two VEVENTs, master RRULE, one RECURRENCE-ID.
	got, err := svc.GetEvent(ctx, GetEventInput{EventID: created.ID, Calendar: cal, TZ: "UTC", WithICS: true})
	if err != nil {
		t.Fatalf("GetEvent WithICS (series): %v", err)
	}
	if n := strings.Count(got.ICS, "BEGIN:VEVENT"); n != 2 {
		t.Errorf("series VEVENT count = %d, want 2\n%s", n, got.ICS)
	}
	if strings.Count(got.ICS, "BEGIN:VCALENDAR") != 1 {
		t.Errorf("want exactly one VCALENDAR\n%s", got.ICS)
	}
	if !strings.Contains(got.ICS, "RRULE:") {
		t.Errorf("master RRULE missing\n%s", got.ICS)
	}
	if n := strings.Count(got.ICS, "RECURRENCE-ID"); n != 1 {
		t.Errorf("RECURRENCE-ID count = %d, want 1\n%s", n, got.ICS)
	}
	// Effective reminder appears on the master VEVENT.
	if !strings.Contains(got.ICS, "TRIGGER:-PT30M") {
		t.Errorf("effective reminder missing from series export\n%s", got.ICS)
	}

	// NoSeries: only the addressed (master) VEVENT.
	gotOne, err := svc.GetEvent(ctx, GetEventInput{EventID: created.ID, Calendar: cal, TZ: "UTC", WithICS: true, NoSeries: true})
	if err != nil {
		t.Fatalf("GetEvent WithICS (no-series): %v", err)
	}
	if n := strings.Count(gotOne.ICS, "BEGIN:VEVENT"); n != 1 {
		t.Errorf("no-series VEVENT count = %d, want 1\n%s", n, gotOne.ICS)
	}
}

// TestE2EInheritedRemindersInICS verifies an event inheriting the calendar's
// default reminders still shows a VALARM in --ics export (previously omitted).
func TestE2EInheritedRemindersInICS(t *testing.T) {
	svc, cal := liveService(t)
	ctx := context.Background()

	// No reminder flags => inherits the calendar default.
	start, end := e2eFutureSlot()
	created, err := svc.CreateEvent(ctx, CreateEventInput{
		Summary: e2eSummary("ics-inherit"), Start: start, End: end, Calendar: cal, TZ: "UTC",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	defer func() { _, _ = svc.DeleteEvent(ctx, DeleteEventInput{EventID: created.ID, Calendar: cal}) }()

	got, err := svc.GetEvent(ctx, GetEventInput{EventID: created.ID, Calendar: cal, TZ: "UTC", WithICS: true})
	if err != nil {
		t.Fatalf("GetEvent WithICS: %v", err)
	}
	// Resolve what the calendar default should be and require it to match.
	eff := eventview.EffectiveReminders(got.Event, got.Settings)
	if len(eff) == 0 {
		t.Skip("calendar has no default reminders; nothing to assert")
	}
	if !strings.Contains(got.ICS, "BEGIN:VALARM") {
		t.Errorf("inherited reminder not injected into --ics:\n%s", got.ICS)
	}
	if !strings.Contains(got.ICS, "TRIGGER:"+eff[0].Trigger) {
		t.Errorf("inherited reminder trigger %q missing from --ics:\n%s", eff[0].Trigger, got.ICS)
	}
	// Effective color (the calendar's color) should also be present.
	if !strings.Contains(got.ICS, "COLOR:"+eventview.EffectiveColor(got.Event, got.Calendar)) {
		t.Errorf("inherited color missing from --ics:\n%s", got.ICS)
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
