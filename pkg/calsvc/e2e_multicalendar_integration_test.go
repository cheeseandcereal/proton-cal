//go:build integration

package calsvc

import (
	"context"
	"testing"
)

// TestE2EListEventsAllCalendars exercises the multi-calendar fan-out: an event
// created in the first calendar must appear in an AllCalendars listing, tagged
// with the calendar it came from, and the merged window must stay sorted.
func TestE2EListEventsAllCalendars(t *testing.T) {
	svc, cal := liveService(t)
	ctx := context.Background()

	start, end := e2eFutureSlot()
	summary := e2eSummary("svc-multical")
	created, err := svc.CreateEvent(ctx, CreateEventInput{
		Summary:  summary,
		Start:    start,
		End:      end,
		Calendar: cal,
		TZ:       "UTC",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	trackDelete(t, svc, cal, created.ID) // t.Cleanup deletes it after the test

	list, err := svc.ListEvents(ctx, ListEventsInput{
		AllCalendars: true,
		TZ:           "UTC",
		From:         e2eFutureDate() + " 00:00",
		Days:         2,
	})
	if err != nil {
		t.Fatalf("ListEvents(AllCalendars): %v", err)
	}
	if len(list.Calendars) == 0 {
		t.Fatal("AllCalendars resolved no calendars")
	}

	var item *ListedItem
	for i := range list.Items {
		if list.Items[i].Event != nil && list.Items[i].Event.EventID == created.ID {
			item = &list.Items[i]
			break
		}
	}
	if item == nil {
		t.Fatalf("event %s not found across %d calendars (%d items)",
			created.ID, len(list.Calendars), len(list.Items))
	}
	// The item must be tagged with a real calendar (its name resolved).
	if item.Calendar.ID == "" || item.Calendar.Name == "" {
		t.Errorf("listed item missing calendar attribution: %+v", item.Calendar)
	}

	// The merged window is sorted by occurrence start.
	for i := 1; i < len(list.Items); i++ {
		if list.Items[i-1].Occurrence.Start > list.Items[i].Occurrence.Start {
			t.Fatalf("merged listing not sorted at index %d", i)
		}
	}
}
