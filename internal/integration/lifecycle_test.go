//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/event"
)

// TestEventLifecycle exercises the full timed-event lifecycle against the
// live API: create -> get/decrypt (exact field round-trip, including RFC
// 5545 TEXT escaping through the real server) -> field-only update (no
// SEQUENCE bump) -> time update (SEQUENCE bump) -> delete -> gone.
func TestEventLifecycle(t *testing.T) {
	ctx := context.Background()
	s := setup(t)
	client, access := newAccess(t)

	start, end := futureWindow()
	// Comma, semicolon and newline prove the RFC 5545 TEXT escaping
	// round-trips through the real server, not just our own parser.
	summary := uniqueSummary("lifecycle") + " with, comma; semicolon\nand newline"
	description := "Created by the proton-cal integration suite."
	location := "Integration Test Lab"

	// 1. Create.
	created, err := event.Create(ctx, client, access, event.CreateOptions{
		Summary:     summary,
		Description: description,
		Location:    location,
		Start:       start,
		End:         end,
		TZName:      s.tzName,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created == nil || created.ID == "" {
		t.Fatalf("server did not echo the created event row: %+v", created)
	}
	markDeleted := trackEvent(t, client, access, created.ID)

	// 2. Get + decrypt: every field round-trips exactly.
	ev := getDecrypted(t, client, access, created.ID)
	if ev.Summary != summary {
		t.Errorf("summary round-trip:\n got %q\nwant %q", ev.Summary, summary)
	}
	if ev.Description != description {
		t.Errorf("description round-trip: got %q, want %q", ev.Description, description)
	}
	if ev.Location != location {
		t.Errorf("location round-trip: got %q, want %q", ev.Location, location)
	}
	if ev.StartTime != start.Unix() {
		t.Errorf("StartTime = %d, want %d", ev.StartTime, start.Unix())
	}
	if ev.EndTime != end.Unix() {
		t.Errorf("EndTime = %d, want %d", ev.EndTime, end.Unix())
	}
	if ev.AllDay {
		t.Error("timed event decrypted as all-day")
	}
	seq0 := ev.Sequence

	// 3. Field-only update: summary changes, everything else (including
	// SEQUENCE, per RFC 5546) must survive untouched.
	renamed := uniqueSummary("lifecycle renamed")
	if _, err := event.SmartUpdate(ctx, client, access, created.ID, event.UpdateOptions{
		Summary: ptr(renamed),
	}, 0); err != nil {
		t.Fatalf("SmartUpdate (summary): %v", err)
	}
	ev = getDecrypted(t, client, access, created.ID)
	if ev.Summary != renamed {
		t.Errorf("summary after update: got %q, want %q", ev.Summary, renamed)
	}
	if ev.Description != description {
		t.Errorf("description not preserved across summary update: got %q", ev.Description)
	}
	if ev.Location != location {
		t.Errorf("location not preserved across summary update: got %q", ev.Location)
	}
	if ev.StartTime != start.Unix() || ev.EndTime != end.Unix() {
		t.Errorf("times changed by a field-only update: start %d end %d, want %d/%d",
			ev.StartTime, ev.EndTime, start.Unix(), end.Unix())
	}
	if ev.Sequence != seq0 {
		t.Errorf("field-only update changed SEQUENCE: got %d, want %d", ev.Sequence, seq0)
	}

	// 4. Time update (+1h) is significant: SEQUENCE must bump.
	newStart := start.Add(time.Hour)
	newEnd := end.Add(time.Hour)
	if _, err := event.SmartUpdate(ctx, client, access, created.ID, event.UpdateOptions{
		Start: ptr(newStart),
		End:   ptr(newEnd),
	}, 0); err != nil {
		t.Fatalf("SmartUpdate (start +1h): %v", err)
	}
	ev = getDecrypted(t, client, access, created.ID)
	if ev.StartTime != newStart.Unix() || ev.EndTime != newEnd.Unix() {
		t.Errorf("times after +1h update: start %d end %d, want %d/%d",
			ev.StartTime, ev.EndTime, newStart.Unix(), newEnd.Unix())
	}
	if ev.Sequence <= seq0 {
		t.Errorf("time update did not bump SEQUENCE: got %d, want > %d", ev.Sequence, seq0)
	}

	// 5. Delete, then verify it is gone (any error from Get is accepted:
	// the server answers with a 404-ish status / Proton error code).
	res, err := event.SmartDelete(ctx, client, access, created.ID, 0)
	if err != nil {
		t.Fatalf("SmartDelete: %v", err)
	}
	if res.Kind != "event" || res.RowsDeleted != 1 {
		t.Errorf("SmartDelete result = %+v, want kind \"event\" with 1 row", res)
	}
	markDeleted()
	if _, err := event.Get(ctx, client, access.CalendarID, created.ID); err == nil {
		t.Error("event still retrievable after delete")
	} else {
		t.Logf("Get after delete failed as expected: %v", err)
	}
}
