//go:build integration

package calsvc

import (
	"context"
	"testing"
	"time"
)

// TestE2ETimezoneWallTimeStable asserts a daily series across a DST transition
// keeps the same local wall time (09:00) even though unix spacing is 23h/25h.
func TestE2ETimezoneWallTimeStable(t *testing.T) {
	svc, cal := liveService(t)
	ctx := context.Background()

	const zone = "America/New_York"
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Skipf("zone %s unavailable: %v", zone, err)
	}

	// Find the next DST transition at least 7 days out, then start the
	// series the day before it so the window straddles the change.
	trans := nextDSTTransition(loc, time.Now().AddDate(0, 0, 7))
	if trans.IsZero() {
		t.Skip("no DST transition found in the search horizon")
	}
	startDay := trans.AddDate(0, 0, -1)
	startStr := time.Date(startDay.Year(), startDay.Month(), startDay.Day(), 9, 0, 0, 0, loc).Format("2006-01-02 15:04")
	endStr := time.Date(startDay.Year(), startDay.Month(), startDay.Day(), 9, 30, 0, 0, loc).Format("2006-01-02 15:04")

	summary := e2eSummary("dst")
	created, err := svc.CreateEvent(ctx, CreateEventInput{
		Summary: summary, Start: startStr, End: endStr, Calendar: cal, TZ: zone,
		Recurrence: Recurrence{Repeat: "daily", Count: 3},
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	defer func() { _, _ = svc.DeleteEvent(ctx, DeleteEventInput{EventID: created.ID, Calendar: cal}) }()

	list, err := svc.ListEvents(ctx, ListEventsInput{
		Calendars: []string{cal}, TZ: zone,
		From: startDay.Format("2006-01-02") + " 00:00", Days: 5,
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var hours []int
	for _, l := range list.Items {
		if l.Event == nil || l.Event.UID != created.UID {
			continue
		}
		local := time.Unix(l.Occurrence.Start, 0).In(loc)
		hours = append(hours, local.Hour())
	}
	if len(hours) != 3 {
		t.Fatalf("expanded %d occurrences, want 3 (hours %v)", len(hours), hours)
	}
	for i, h := range hours {
		if h != 9 {
			t.Errorf("occurrence %d local hour = %d, want 9 (DST wall-time drift)", i, h)
		}
	}
}

// nextDSTTransition returns the first instant at/after `from` where the zone's
// UTC offset changes, or the zero time if none within ~400 days.
func nextDSTTransition(loc *time.Location, from time.Time) time.Time {
	prev := from.In(loc)
	_, prevOff := prev.Zone()
	for i := 1; i <= 400; i++ {
		d := from.AddDate(0, 0, i).In(loc)
		_, off := d.Zone()
		if off != prevOff {
			return d
		}
		prevOff = off
	}
	return time.Time{}
}
