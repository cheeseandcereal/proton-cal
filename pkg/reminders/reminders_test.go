package reminders

import (
	"strings"
	"testing"
)

func TestParseShorthand(t *testing.T) {
	tests := []struct {
		in       string
		wantType int
		wantTrig string
	}{
		{"15m", typeNotify, "-PT15M"},
		{"30m", typeNotify, "-PT30M"},
		{"1h", typeNotify, "-PT1H"},
		{"1h30m", typeNotify, "-PT1H30M"},
		{"2d", typeNotify, "-P2D"},
		{"1w", typeNotify, "-P1W"},
		{"2w", typeNotify, "-P2W"},
		{"90m", typeNotify, "-PT1H30M"},
		{"1d2h", typeNotify, "-P1DT2H"},
		{"45s", typeNotify, "-PT45S"},
		{"email:1h", typeEmail, "-PT1H"},
		{"notify:15m", typeNotify, "-PT15M"},
		{"e:1d", typeEmail, "-P1D"},
		// raw passthrough
		{"-PT15M", typeNotify, "-PT15M"},
		{"-P1D", typeNotify, "-P1D"},
		{"-P1DT2H", typeNotify, "-P1DT2H"},
		{"email:-PT30M", typeEmail, "-PT30M"},
		// case-insensitive
		{"1H", typeNotify, "-PT1H"},
		{"EMAIL:2D", typeEmail, "-P2D"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			n, err := Parse(tt.in)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.in, err)
			}
			if n.Type != tt.wantType {
				t.Errorf("Type = %d, want %d", n.Type, tt.wantType)
			}
			if n.Trigger != tt.wantTrig {
				t.Errorf("Trigger = %q, want %q", n.Trigger, tt.wantTrig)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "empty"},
		{"   ", "empty"},
		{"0m", "must be positive"},
		{"+5m", "before the event"},
		{"+PT5M", "before the event"},
		{"15", "missing a unit"},
		{"abc", "invalid reminder offset"},
		{"15x", "invalid reminder offset"},
		{"bogus:15m", "unknown reminder type"},
		{"email:", "missing reminder offset"},
		{"m", "expected e.g."},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			_, err := Parse(tt.in)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse(%q) error = %v, want containing %q", tt.in, err, tt.want)
			}
		})
	}
}

func TestParseList(t *testing.T) {
	if got, err := ParseList(nil); err != nil || got != nil {
		t.Errorf("empty list = (%v, %v), want (nil, nil)", got, err)
	}

	got, err := ParseList([]string{"15m", "email:1h", "-P1D"})
	if err != nil {
		t.Fatalf("ParseList: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d notifications, want 3", len(got))
	}
	if got[0].Trigger != "-PT15M" || got[1].Type != typeEmail || got[2].Trigger != "-P1D" {
		t.Errorf("parsed list = %+v", got)
	}

	// One bad entry fails the whole list.
	if _, err := ParseList([]string{"15m", "nope"}); err == nil {
		t.Error("want error from a bad entry")
	}
}

func TestParseListCap(t *testing.T) {
	specs := make([]string, MaxReminders+1)
	for i := range specs {
		specs[i] = "15m"
	}
	_, err := ParseList(specs)
	if err == nil || !strings.Contains(err.Error(), "too many reminders") {
		t.Fatalf("want cap error, got %v", err)
	}

	// Exactly at the cap is fine.
	if _, err := ParseList(specs[:MaxReminders]); err != nil {
		t.Errorf("at cap should pass: %v", err)
	}
}

func TestFormatICalDuration(t *testing.T) {
	// Round-trip a few via Parse to ensure formatting is stable.
	cases := map[string]string{
		"15m":    "-PT15M",
		"60m":    "-PT1H",
		"1440m":  "-P1D",
		"10080m": "-P1W", // 7 days = 1 week
		"25h":    "-P1DT1H",
	}
	for in, want := range cases {
		n, err := Parse(in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", in, err)
		}
		if n.Trigger != want {
			t.Errorf("Parse(%q).Trigger = %q, want %q", in, n.Trigger, want)
		}
	}
}
