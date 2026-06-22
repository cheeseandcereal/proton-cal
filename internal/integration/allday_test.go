//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/event"
)

// TestAllDayLifecycle exercises the all-day lifecycle: create a VALUE=DATE event
// (exclusive iCal end) -> server reports FullDay with midnight-UTC times -> a
// field-only update keeps it all-day -> delete.
func TestAllDayLifecycle(t *testing.T) {
	ctx := context.Background()
	setup(t)
	client, access := newAccess(t)

	// Day D ~30 days out; all-day events are anchored at midnight UTC
	// server-side. End is the EXCLUSIVE iCal end (D+1 = single-day event).
	now := time.Now().UTC().AddDate(0, 0, eventOffsetDays+1)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	dayEnd := day.AddDate(0, 0, 1)
	summary := uniqueSummary("allday")

	// 1. Create.
	created, err := event.Create(ctx, client, access, event.CreateOptions{
		Summary: summary,
		Start:   day,
		End:     dayEnd,
		TZName:  "UTC",
		AllDay:  true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created == nil || created.ID == "" {
		t.Fatalf("server did not echo the created event row: %+v", created)
	}
	markDeleted := trackEvent(t, client, access, created.ID)

	// 2. Server metadata: FullDay set, dates anchored at midnight UTC.
	raw, err := event.Get(ctx, client, access.CalendarID, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !raw.IsAllDay() {
		t.Errorf("server FullDay = %d, want non-zero", raw.FullDay)
	}
	if raw.StartTime != day.Unix() {
		t.Errorf("raw StartTime = %d, want %d (midnight UTC of %s)", raw.StartTime, day.Unix(), day.Format("2006-01-02"))
	}
	if raw.EndTime != dayEnd.Unix() {
		t.Errorf("raw EndTime = %d, want %d (exclusive D+1)", raw.EndTime, dayEnd.Unix())
	}

	ev, err := event.Decrypt(raw, access.KR)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !ev.AllDay {
		t.Error("decrypted event is not all-day")
	}
	if ev.Summary != summary {
		t.Errorf("summary round-trip: got %q, want %q", ev.Summary, summary)
	}
	if ev.Start.Unix() != day.Unix() {
		t.Errorf("decrypted Start = %d, want %d (midnight UTC)", ev.Start.Unix(), day.Unix())
	}

	// 3. Field-only update must keep the event all-day and keep its date.
	renamed := summary + " v2"
	if _, err := event.SmartUpdate(ctx, client, access, created.ID, event.UpdateOptions{
		Summary: ptr(renamed),
	}, 0); err != nil {
		t.Fatalf("SmartUpdate (summary): %v", err)
	}
	raw, err = event.Get(ctx, client, access.CalendarID, created.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if !raw.IsAllDay() {
		t.Error("REGRESSION: update corrupted the all-day event into a timed one")
	}
	if raw.StartTime != day.Unix() {
		t.Errorf("update moved the all-day date: StartTime = %d, want %d", raw.StartTime, day.Unix())
	}
	ev, err = event.Decrypt(raw, access.KR)
	if err != nil {
		t.Fatalf("Decrypt after update: %v", err)
	}
	if !ev.AllDay {
		t.Error("decrypted event lost all-day flag after update")
	}
	if ev.Summary != renamed {
		t.Errorf("summary after update: got %q, want %q", ev.Summary, renamed)
	}

	// 4. Delete, then verify it is gone.
	res, err := event.SmartDelete(ctx, client, access, created.ID, 0)
	if err != nil {
		t.Fatalf("SmartDelete: %v", err)
	}
	if res.Kind != "event" {
		t.Errorf("SmartDelete kind = %q, want \"event\"", res.Kind)
	}
	markDeleted()
	if _, err := event.Get(ctx, client, access.CalendarID, created.ID); err == nil {
		t.Error("event still retrievable after delete")
	}
}
