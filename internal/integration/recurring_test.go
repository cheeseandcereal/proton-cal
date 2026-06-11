//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal-go/internal/event"
	"github.com/cheeseandcereal/proton-cal-go/internal/recurrence"
)

// TestRecurringLifecycle exercises the recurrence orchestration against the
// live API, using occurrence start timestamps from ListWindow exactly as a
// CLI user would from the listing output:
//
//	a. create a daily COUNT=5 series and expand 5 occurrences,
//	b. delete ONE middle occurrence (EXDATE on the master),
//	c. single-edit a DIFFERENT occurrence (exception row),
//	d. significant master update cleans the now-invalid exception,
//	e. delete the whole series (master + all same-UID rows).
func TestRecurringLifecycle(t *testing.T) {
	ctx := context.Background()
	s := setup(t)
	client, access := newAccess(t)

	start, end := futureWindow()
	summary := uniqueSummary("recurring")
	rrule, err := recurrence.BuildRRule("daily", 1, 5, "", s.tzName, false)
	if err != nil {
		t.Fatalf("BuildRRule: %v", err)
	}

	// a. Create the series and expand it.
	created, err := event.Create(ctx, client, access, event.CreateOptions{
		Summary:     summary,
		Description: "Created by the recurring integration test.",
		Start:       start,
		End:         end,
		TZName:      s.tzName,
		RRule:       rrule,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created == nil || created.ID == "" {
		t.Fatalf("server did not echo the created event row: %+v", created)
	}
	masterID := created.ID
	// Cleanup via the master: SmartDelete on a master removes the whole
	// series including any exception rows this test creates.
	markDeleted := trackEvent(t, client, access, masterID)

	master, err := event.Get(ctx, client, access.CalendarID, masterID)
	if err != nil {
		t.Fatalf("Get master: %v", err)
	}
	uid := master.UID
	if uid == "" {
		t.Fatal("master row has no UID")
	}
	if master.RRule != rrule {
		t.Errorf("RRule round-trip: got %q, want %q", master.RRule, rrule)
	}
	if len(master.Exdates) != 0 {
		t.Errorf("fresh series already has Exdates: %v", master.Exdates)
	}

	// The listing window generously covers all 5 daily occurrences even
	// across a DST shift (daily recurrence preserves local wall time, so
	// unix spacing may be 23h or 25h around a transition).
	winStart := start.Add(-24 * time.Hour)
	winEnd := start.Add(7 * 24 * time.Hour)

	listed := listUID(t, client, access, uid, winStart, winEnd, s.tzName)
	if len(listed) != 5 {
		t.Fatalf("expected 5 expanded occurrences, got %d", len(listed))
	}
	occStarts := make([]int64, len(listed))
	for i, l := range listed {
		occStarts[i] = l.Occurrence.Start
		if l.Event.Summary != summary {
			t.Errorf("occurrence %d summary = %q, want the master's %q", i, l.Event.Summary, summary)
		}
	}
	if occStarts[0] != start.Unix() {
		t.Errorf("first occurrence start = %d, want %d", occStarts[0], start.Unix())
	}

	// b. Delete the SECOND occurrence: EXDATE on the master, 4 remain.
	deletedOcc := occStarts[1]
	res, err := event.SmartDelete(ctx, client, access, masterID, deletedOcc)
	if err != nil {
		t.Fatalf("SmartDelete (occurrence %d): %v", deletedOcc, err)
	}
	if res.Kind != "occurrence" {
		t.Errorf("SmartDelete kind = %q, want \"occurrence\"", res.Kind)
	}
	listed = listUID(t, client, access, uid, winStart, winEnd, s.tzName)
	if len(listed) != 4 {
		t.Fatalf("expected 4 occurrences after deleting one, got %d", len(listed))
	}
	masterEv := getDecrypted(t, client, access, masterID)
	if !containsTS(masterEv.Exdates, deletedOcc) {
		t.Errorf("master Exdates %v do not contain the deleted occurrence %d", masterEv.Exdates, deletedOcc)
	}
	if masterEv.RRule != rrule {
		t.Errorf("occurrence delete must not touch the RRULE: got %q, want %q", masterEv.RRule, rrule)
	}

	// c. Single-edit a DIFFERENT occurrence (the third): an exception row
	// is created; the listing shows 3 master-summary occurrences plus the
	// edited one carrying a RecurrenceID.
	editedOcc := occStarts[2]
	editedSummary := summary + " (edited occurrence)"
	outcome, err := event.SmartUpdate(ctx, client, access, masterID, event.UpdateOptions{
		Summary: ptr(editedSummary),
	}, editedOcc)
	if err != nil {
		t.Fatalf("SmartUpdate (occurrence %d): %v", editedOcc, err)
	}
	if !outcome.EditedOccurrence {
		t.Error("SmartUpdate did not report an occurrence edit")
	}
	listed = listUID(t, client, access, uid, winStart, winEnd, s.tzName)
	if len(listed) != 4 {
		t.Fatalf("expected 4 occurrences after an occurrence edit, got %d", len(listed))
	}
	var masterRows, editedRows int
	for _, l := range listed {
		switch {
		case l.Event.RecurrenceID != 0:
			editedRows++
			if l.Event.Summary != editedSummary {
				t.Errorf("exception row summary = %q, want %q", l.Event.Summary, editedSummary)
			}
			if l.Event.RecurrenceID != editedOcc {
				t.Errorf("exception RecurrenceID = %d, want the original start %d", l.Event.RecurrenceID, editedOcc)
			}
		default:
			masterRows++
			if l.Event.Summary != summary {
				t.Errorf("master occurrence summary = %q, want %q", l.Event.Summary, summary)
			}
		}
	}
	if masterRows != 3 || editedRows != 1 {
		t.Errorf("occurrence split = %d master + %d edited, want 3 + 1", masterRows, editedRows)
	}

	// d. A significant master update (start +1h) invalidates the single
	// edit: the exception row from (c) must be cleaned up.
	newStart := time.Unix(masterEv.StartTime, 0).UTC().Add(time.Hour)
	newEnd := time.Unix(masterEv.EndTime, 0).UTC().Add(time.Hour)
	outcome, err = event.SmartUpdate(ctx, client, access, masterID, event.UpdateOptions{
		Start: ptr(newStart),
		End:   ptr(newEnd),
	}, 0)
	if err != nil {
		t.Fatalf("SmartUpdate (master start +1h): %v", err)
	}
	if outcome.RemovedExceptions != 1 {
		t.Errorf("RemovedExceptions = %d, want 1 (the exception row from step c)", outcome.RemovedExceptions)
	}

	// e. Delete the whole series: nothing with this UID remains.
	res, err = event.SmartDelete(ctx, client, access, masterID, 0)
	if err != nil {
		t.Fatalf("SmartDelete (series): %v", err)
	}
	if res.Kind != "series" {
		t.Errorf("SmartDelete kind = %q, want \"series\"", res.Kind)
	}
	markDeleted()
	listed = listUID(t, client, access, uid, winStart, winEnd, s.tzName)
	if len(listed) != 0 {
		t.Errorf("expected 0 occurrences after series delete, got %d", len(listed))
	}
	rows, err := event.GetByUID(ctx, client, access.CalendarID, uid)
	if err != nil {
		t.Fatalf("GetByUID after series delete: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("GetByUID still returns %d rows after series delete", len(rows))
	}
}

func containsTS(haystack []int64, needle int64) bool {
	for _, ts := range haystack {
		if ts == needle {
			return true
		}
	}
	return false
}
