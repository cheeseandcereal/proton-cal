package calsvc

import (
	"strings"
	"testing"
	"time"
)

func TestResolveCreateTimes(t *testing.T) {
	t.Run("timed without end errors", func(t *testing.T) {
		_, _, err := resolveCreateTimes("2026-06-15 09:00", "", false, "UTC")
		if err == nil || !strings.Contains(err.Error(), "end is required") {
			t.Fatalf("want 'end is required' error, got %v", err)
		}
	})

	t.Run("timed parses in zone", func(t *testing.T) {
		berlin, err := time.LoadLocation("Europe/Berlin")
		if err != nil {
			t.Fatal(err)
		}
		start, end, err := resolveCreateTimes("2026-06-15 09:00", "2026-06-15 09:30", false, "Europe/Berlin")
		if err != nil {
			t.Fatal(err)
		}
		want := time.Date(2026, 6, 15, 9, 0, 0, 0, berlin)
		if !start.Equal(want) {
			t.Errorf("start = %v, want %v", start, want)
		}
		if d := end.Sub(start); d != 30*time.Minute {
			t.Errorf("duration = %v, want 30m", d)
		}
	})

	t.Run("timed bad zone errors", func(t *testing.T) {
		if _, _, err := resolveCreateTimes("2026-06-15 09:00", "2026-06-15 09:30", false, "Not/AZone"); err == nil {
			t.Fatal("want error for bad zone")
		}
	})

	t.Run("all-day default end is start plus one day", func(t *testing.T) {
		start, end, err := resolveCreateTimes("2026-06-15", "", true, "UTC")
		if err != nil {
			t.Fatal(err)
		}
		if !start.Equal(time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("start = %v", start)
		}
		if d := end.Sub(start); d != 24*time.Hour {
			t.Errorf("duration = %v, want 24h", d)
		}
	})

	t.Run("all-day inclusive end converts to exclusive", func(t *testing.T) {
		start, end, err := resolveCreateTimes("2026-06-15", "2026-06-17", true, "UTC")
		if err != nil {
			t.Fatal(err)
		}
		if d := end.Sub(start); d != 3*24*time.Hour {
			t.Errorf("duration = %v, want 72h (inclusive end +24h)", d)
		}
	})

	t.Run("all-day end before start errors", func(t *testing.T) {
		if _, _, err := resolveCreateTimes("2026-06-15", "2026-06-10", true, "UTC"); err == nil {
			t.Fatal("want error for end before start")
		}
	})

	t.Run("all-day rejects datetime form", func(t *testing.T) {
		if _, _, err := resolveCreateTimes("2026-06-15 09:00", "", true, "UTC"); err == nil {
			t.Fatal("want error for datetime with all-day")
		}
	})
}

func TestParseWhen(t *testing.T) {
	t.Run("timed form", func(t *testing.T) {
		got, err := parseWhen("2026-06-15 09:00", "UTC")
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("date form anchors midnight UTC", func(t *testing.T) {
		got, err := parseWhen("2026-06-15", "Europe/Berlin")
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("garbage errors with hint", func(t *testing.T) {
		_, err := parseWhen("yesterday", "UTC")
		if err == nil || !strings.Contains(err.Error(), "YYYY-MM-DD") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestFormatOccurrenceStart(t *testing.T) {
	loc := time.FixedZone("UTC+2", 2*60*60)
	ts := time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC).Unix()
	if got := FormatOccurrenceStart(ts, false, loc); got != "2026-06-12 11:00" {
		t.Errorf("timed = %q", got)
	}
	if got := FormatOccurrenceStart(ts, true, loc); got != "2026-06-12" {
		t.Errorf("all-day = %q", got)
	}
	// Round trip: the shown value parses back to the same instant.
	shown := FormatOccurrenceStart(ts, false, loc)
	back, err := parseOccurrence(shown, "UTC+2")
	if err == nil && back != ts {
		t.Errorf("round trip: got %d, want %d (shown %q)", back, ts, shown)
	}
}

func TestRecurrenceBuildRRule(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got, err := Recurrence{}.buildRRule("UTC", false)
		if err != nil || got != "" {
			t.Errorf("got (%q, %v)", got, err)
		}
	})
	t.Run("structured", func(t *testing.T) {
		got, err := Recurrence{Repeat: "weekly", Every: 2, Count: 5}.buildRRule("UTC", false)
		if err != nil {
			t.Fatal(err)
		}
		if got != "FREQ=WEEKLY;INTERVAL=2;COUNT=5" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("raw rrule sanitized", func(t *testing.T) {
		got, err := Recurrence{RawRRule: "freq=daily;count=3"}.buildRRule("UTC", false)
		if err != nil {
			t.Fatal(err)
		}
		if got != "FREQ=DAILY;COUNT=3" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("raw conflicts with structured", func(t *testing.T) {
		_, err := Recurrence{RawRRule: "FREQ=DAILY", Repeat: "daily"}.buildRRule("UTC", false)
		if err == nil || !strings.Contains(err.Error(), "rrule cannot be combined") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("modifiers require repeat", func(t *testing.T) {
		_, err := Recurrence{Count: 3}.buildRRule("UTC", false)
		if err == nil || !strings.Contains(err.Error(), "require repeat") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("empty detection", func(t *testing.T) {
		if !(Recurrence{}).Empty() || !(Recurrence{Every: 1}).Empty() {
			t.Error("zero/interval-1 recurrence must be Empty")
		}
		if (Recurrence{Repeat: "daily"}).Empty() || (Recurrence{Count: 2}).Empty() {
			t.Error("non-zero recurrence must not be Empty")
		}
	})
}
