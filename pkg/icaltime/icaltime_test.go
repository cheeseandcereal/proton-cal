package icaltime

import (
	"testing"
	"time"
)

func TestFormatUTC(t *testing.T) {
	utc := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := FormatUTC(utc); got != "20260102T030405Z" {
		t.Errorf("FormatUTC(utc) = %q", got)
	}
	// Aware non-UTC converts to UTC.
	plusTen := time.FixedZone("plus10", 10*3600)
	if got := FormatUTC(time.Date(2026, 1, 2, 13, 4, 5, 0, plusTen)); got != "20260102T030405Z" {
		t.Errorf("FormatUTC(+10) = %q", got)
	}
}

func TestParse(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		in     string
		loc    *time.Location
		want   time.Time
		wantOK bool
	}{
		{"utc form", "20260102T030405Z", time.UTC, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), true},
		{"local form in loc", "20260102T030405", paris, time.Date(2026, 1, 2, 3, 4, 5, 0, paris), true},
		{"date form", "20260102", paris, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), true},
		{"garbage", "not-a-date", time.UTC, time.Time{}, false},
		{"wrong length", "202601023", time.UTC, time.Time{}, false},
		{"bad month", "20269902T030405Z", time.UTC, time.Time{}, false},
		{"empty", "", time.UTC, time.Time{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse(tt.in, tt.loc)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && !got.Equal(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoadLocation(t *testing.T) {
	if loc, err := LoadLocation(""); err != nil || loc != time.UTC {
		t.Errorf("empty zone: got (%v, %v), want UTC", loc, err)
	}
	if loc, err := LoadLocation("Europe/Paris"); err != nil || loc.String() != "Europe/Paris" {
		t.Errorf("Paris: got (%v, %v)", loc, err)
	}
	if _, err := LoadLocation("Not/AZone"); err == nil {
		t.Error("invalid zone: want error")
	}
}

func TestOrUTC(t *testing.T) {
	if got := OrUTC(""); got != "UTC" {
		t.Errorf("OrUTC(\"\") = %q", got)
	}
	if got := OrUTC("Europe/Paris"); got != "Europe/Paris" {
		t.Errorf("OrUTC(Paris) = %q", got)
	}
}
