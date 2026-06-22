//go:build integration

package calsvc

import (
	"context"
	"testing"
	"time"
)

// occurrencesFor lists the window and returns the occurrences backed by the
// given event UID, in ListWindow order.
func occurrencesForUID(t *testing.T, svc *Service, calSel, uid string, from string, days int) []int64 {
	t.Helper()
	list, err := svc.ListEvents(context.Background(), ListEventsInput{Calendar: calSel, TZ: "UTC", From: from, Days: days})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var starts []int64
	for _, l := range list.Items {
		if l.Event != nil && l.Event.UID == uid {
			starts = append(starts, l.Occurrence.Start)
		}
	}
	return starts
}

// TestE2ERecurrenceVariants creates several recurrence shapes through the
// service layer and asserts the expanded occurrence counts, catching drift
// in how the structured options are translated to RRULEs the server accepts.
func TestE2ERecurrenceVariants(t *testing.T) {
	svc, cal := liveService(t)
	ctx := context.Background()

	date := e2eFutureDate()
	start := date + " 09:00"
	end := date + " 09:30"
	winFrom := date + " 00:00"

	cases := []struct {
		name     string
		rec      Recurrence
		allDay   bool
		wantOccs int
		winDays  int
	}{
		{name: "weekly count 3", rec: Recurrence{Repeat: "weekly", Count: 3}, wantOccs: 3, winDays: 22},
		{name: "daily interval 2 count 3", rec: Recurrence{Repeat: "daily", Every: 2, Count: 3}, wantOccs: 3, winDays: 7},
		{name: "daily until +2d", rec: Recurrence{Repeat: "daily", Until: addDays(date, 2)}, wantOccs: 3, winDays: 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary := e2eSummary("rec-" + tc.name)
			created, err := svc.CreateEvent(ctx, CreateEventInput{
				Summary: summary, Start: start, End: end, Calendar: cal, TZ: "UTC",
				Recurrence: tc.rec,
			})
			if err != nil {
				t.Fatalf("CreateEvent: %v", err)
			}
			defer func() {
				if _, err := svc.DeleteEvent(ctx, DeleteEventInput{EventID: created.ID, Calendar: cal}); err != nil {
					t.Logf("cleanup delete %s: %v", created.ID, err)
				}
			}()
			if created.UID == "" {
				t.Fatal("recurring create returned no UID")
			}
			starts := occurrencesForUID(t, svc, cal, created.UID, winFrom, tc.winDays)
			if len(starts) != tc.wantOccs {
				t.Errorf("%s: expanded %d occurrences, want %d (starts %v)", tc.name, len(starts), tc.wantOccs, starts)
			}
		})
	}
}

// TestE2EAllDayRecurring verifies an all-day daily series round-trips.
func TestE2EAllDayRecurring(t *testing.T) {
	svc, cal := liveService(t)
	ctx := context.Background()

	date := e2eFutureDate()
	summary := e2eSummary("allday-recurring")
	created, err := svc.CreateEvent(ctx, CreateEventInput{
		Summary: summary, Start: date, AllDay: true, Calendar: cal, TZ: "UTC",
		Recurrence: Recurrence{Repeat: "daily", Count: 3},
	})
	if err != nil {
		t.Fatalf("CreateEvent (all-day recurring): %v", err)
	}
	defer func() {
		_, _ = svc.DeleteEvent(ctx, DeleteEventInput{EventID: created.ID, Calendar: cal})
	}()
	got, err := svc.GetEvent(ctx, GetEventInput{EventID: created.ID, Calendar: cal})
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if !got.Event.AllDay {
		t.Error("recurring event lost all-day flag")
	}
	starts := occurrencesForUID(t, svc, cal, created.UID, date+" 00:00", 5)
	if len(starts) != 3 {
		t.Errorf("all-day series expanded %d, want 3", len(starts))
	}
}

func addDays(date string, n int) string {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic(err)
	}
	return d.AddDate(0, 0, n).Format("2006-01-02")
}
