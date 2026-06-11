package calsvc

import (
	"context"
	"strings"
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/config"
)

func TestUpdateEventInputValidate(t *testing.T) {
	tests := []struct {
		name    string
		in      UpdateEventInput
		wantErr bool
	}{
		{name: "nothing set", in: UpdateEventInput{}, wantErr: false},
		{name: "repeat alone", in: UpdateEventInput{Recurrence: Recurrence{Repeat: "daily"}}, wantErr: false},
		{name: "rrule alone", in: UpdateEventInput{Recurrence: Recurrence{RawRRule: "FREQ=DAILY"}}, wantErr: false},
		{name: "no-repeat alone", in: UpdateEventInput{NoRepeat: true}, wantErr: false},
		{name: "occurrence alone", in: UpdateEventInput{Occurrence: "2026-06-12 09:00"}, wantErr: false},
		{name: "no-repeat with repeat", in: UpdateEventInput{NoRepeat: true, Recurrence: Recurrence{Repeat: "daily"}}, wantErr: true},
		{name: "no-repeat with rrule", in: UpdateEventInput{NoRepeat: true, Recurrence: Recurrence{RawRRule: "FREQ=DAILY"}}, wantErr: true},
		{name: "occurrence with repeat", in: UpdateEventInput{Occurrence: "2026-06-12 09:00", Recurrence: Recurrence{Repeat: "daily"}}, wantErr: true},
		{name: "occurrence with rrule", in: UpdateEventInput{Occurrence: "2026-06-12 09:00", Recurrence: Recurrence{RawRRule: "FREQ=DAILY"}}, wantErr: true},
		{name: "occurrence with no-repeat", in: UpdateEventInput{Occurrence: "2026-06-12 09:00", NoRepeat: true}, wantErr: true},
		// Only repeat/rrule conflict with occurrence; count/until/every
		// alone do not (and are ignored on the occurrence path).
		{name: "occurrence with count only", in: UpdateEventInput{Occurrence: "2026-06-12 09:00", Recurrence: Recurrence{Count: 5}}, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestUpdateTZName(t *testing.T) {
	tests := []struct {
		name       string
		override   string
		defaultTZ  string
		timesGiven bool
		want       string
	}{
		{name: "explicit tz wins", override: "Europe/Berlin", defaultTZ: "UTC", timesGiven: false, want: "Europe/Berlin"},
		{name: "explicit tz wins with times", override: "Europe/Berlin", defaultTZ: "UTC", timesGiven: true, want: "Europe/Berlin"},
		{name: "default tz when times given", override: "", defaultTZ: "America/New_York", timesGiven: true, want: "America/New_York"},
		{name: "empty when no times and no explicit tz", override: "", defaultTZ: "America/New_York", timesGiven: false, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := updateTZName(tt.override, tt.defaultTZ, tt.timesGiven); got != tt.want {
				t.Errorf("updateTZName(%q, %q, %v) = %q, want %q", tt.override, tt.defaultTZ, tt.timesGiven, got, tt.want)
			}
		})
	}
}

// Validation paths fail before any network use, so a detached Service is
// enough to exercise them end to end.
func TestDetachedServiceValidationPaths(t *testing.T) {
	svc := NewDetached(config.Config{Timezone: "UTC"})
	ctx := context.Background()

	if _, err := svc.UpdateEvent(ctx, UpdateEventInput{EventID: "x", NoRepeat: true, Recurrence: Recurrence{Repeat: "daily"}}); err == nil || !strings.Contains(err.Error(), "no-repeat cannot be combined") {
		t.Errorf("update validation: %v", err)
	}
	if _, err := svc.CreateEvent(ctx, CreateEventInput{Summary: "X", Start: "2026-06-15 09:00"}); err == nil || !strings.Contains(err.Error(), "end is required") {
		t.Errorf("create validation: %v", err)
	}
	if _, err := svc.UpdateEvent(ctx, UpdateEventInput{EventID: "x", Start: "bogus"}); err == nil || !strings.Contains(err.Error(), "invalid date/time") {
		t.Errorf("update parse validation: %v", err)
	}
	if _, err := svc.DeleteEvent(ctx, DeleteEventInput{EventID: "x", Occurrence: "bogus"}); err == nil || !strings.Contains(err.Error(), "invalid date/time") {
		t.Errorf("delete parse validation: %v", err)
	}
	if svc.EffectiveTimezone("") != "UTC" || svc.EffectiveTimezone("Europe/Berlin") != "Europe/Berlin" {
		t.Error("EffectiveTimezone override handling")
	}
}
