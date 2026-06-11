package cli

import (
	"strings"
	"testing"
	"time"
)

func TestResolveCreateTimesAllDayDefaultEnd(t *testing.T) {
	start, end, err := resolveCreateTimes("2026-06-10", "", true, "UTC")
	if err != nil {
		t.Fatalf("resolveCreateTimes: %v", err)
	}
	wantStart := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	if !start.Equal(wantStart) {
		t.Errorf("start = %v, want %v", start, wantStart)
	}
	// End defaults to the start date, +24h exclusive.
	if !end.Equal(wantStart.Add(24 * time.Hour)) {
		t.Errorf("end = %v, want %v", end, wantStart.Add(24*time.Hour))
	}
}

func TestResolveCreateTimesAllDayInclusiveEnd(t *testing.T) {
	start, end, err := resolveCreateTimes("2026-06-10", "2026-06-12", true, "UTC")
	if err != nil {
		t.Fatalf("resolveCreateTimes: %v", err)
	}
	if !start.Equal(time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v", start)
	}
	// Inclusive 06-12 becomes exclusive 06-13.
	if !end.Equal(time.Date(2026, 6, 13, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("end = %v, want exclusive 2026-06-13", end)
	}
}

func TestResolveCreateTimesAllDayEndBeforeStart(t *testing.T) {
	_, _, err := resolveCreateTimes("2026-06-10", "2026-06-08", true, "UTC")
	if err == nil || !strings.Contains(err.Error(), "end date must not be before start date") {
		t.Errorf("err = %v, want end-before-start error", err)
	}
}

func TestResolveCreateTimesAllDayBadDate(t *testing.T) {
	if _, _, err := resolveCreateTimes("06/10/2026", "", true, "UTC"); err == nil {
		t.Error("expected parse error for bad all-day start")
	}
	if _, _, err := resolveCreateTimes("2026-06-10", "not-a-date", true, "UTC"); err == nil {
		t.Error("expected parse error for bad all-day end")
	}
}

func TestResolveCreateTimesTimed(t *testing.T) {
	start, end, err := resolveCreateTimes("2026-06-10 09:00", "2026-06-10 09:30", false, "UTC")
	if err != nil {
		t.Fatalf("resolveCreateTimes: %v", err)
	}
	if !start.Equal(time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v", start)
	}
	if !end.Equal(time.Date(2026, 6, 10, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("end = %v", end)
	}
}

func TestResolveCreateTimesTimedRequiresEnd(t *testing.T) {
	_, _, err := resolveCreateTimes("2026-06-10 09:00", "", false, "UTC")
	if err == nil || !strings.Contains(err.Error(), "--end is required for timed events") {
		t.Errorf("err = %v, want missing --end error", err)
	}
}

func TestResolveCreateTimesTimedBadFormat(t *testing.T) {
	// A bare date is not a valid timed start.
	if _, _, err := resolveCreateTimes("2026-06-10", "2026-06-10 09:30", false, "UTC"); err == nil {
		t.Error("expected parse error for date-only timed start")
	}
	if _, _, err := resolveCreateTimes("2026-06-10 09:00", "2026-06-10", false, "UTC"); err == nil {
		t.Error("expected parse error for date-only timed end")
	}
}
