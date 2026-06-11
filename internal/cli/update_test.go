package cli

import (
	"testing"

	"github.com/cheeseandcereal/proton-cal-go/internal/front"
)

func TestValidateUpdateFlags(t *testing.T) {
	tests := []struct {
		name       string
		occurrence string
		noRepeat   bool
		rec        front.RecurrenceFlags
		wantErr    bool
	}{
		{name: "nothing set", wantErr: false},
		{name: "repeat alone", rec: front.RecurrenceFlags{Repeat: "daily"}, wantErr: false},
		{name: "rrule alone", rec: front.RecurrenceFlags{RawRRule: "FREQ=DAILY"}, wantErr: false},
		{name: "no-repeat alone", noRepeat: true, wantErr: false},
		{name: "occurrence alone", occurrence: "2026-06-12 09:00", wantErr: false},
		{name: "no-repeat with repeat", noRepeat: true, rec: front.RecurrenceFlags{Repeat: "daily"}, wantErr: true},
		{name: "no-repeat with rrule", noRepeat: true, rec: front.RecurrenceFlags{RawRRule: "FREQ=DAILY"}, wantErr: true},
		{name: "occurrence with repeat", occurrence: "2026-06-12 09:00", rec: front.RecurrenceFlags{Repeat: "daily"}, wantErr: true},
		{name: "occurrence with rrule", occurrence: "2026-06-12 09:00", rec: front.RecurrenceFlags{RawRRule: "FREQ=DAILY"}, wantErr: true},
		{name: "occurrence with no-repeat", occurrence: "2026-06-12 09:00", noRepeat: true, wantErr: true},
		// Only --repeat/--rrule conflict with --occurrence;
		// --count/--until/--every alone do not conflict (and are ignored
		// on the occurrence path).
		{name: "occurrence with count only", occurrence: "2026-06-12 09:00", rec: front.RecurrenceFlags{Count: 5}, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUpdateFlags(tt.occurrence, tt.noRepeat, tt.rec)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateUpdateFlags() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestUpdateTZName(t *testing.T) {
	tests := []struct {
		name       string
		tzFlag     string
		defaultTZ  string
		timesGiven bool
		want       string
	}{
		{name: "explicit tz wins", tzFlag: "Europe/Berlin", defaultTZ: "UTC", timesGiven: false, want: "Europe/Berlin"},
		{name: "explicit tz wins with times", tzFlag: "Europe/Berlin", defaultTZ: "UTC", timesGiven: true, want: "Europe/Berlin"},
		{name: "default tz when times given", tzFlag: "", defaultTZ: "America/New_York", timesGiven: true, want: "America/New_York"},
		{name: "empty when no times and no explicit tz", tzFlag: "", defaultTZ: "America/New_York", timesGiven: false, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := updateTZName(tt.tzFlag, tt.defaultTZ, tt.timesGiven); got != tt.want {
				t.Errorf("updateTZName(%q, %q, %v) = %q, want %q", tt.tzFlag, tt.defaultTZ, tt.timesGiven, got, tt.want)
			}
		})
	}
}
