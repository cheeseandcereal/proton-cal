package event

import (
	"strings"
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
)

func TestBuildICSReconstructsEvent(t *testing.T) {
	calKR, _ := testKeys(t)
	raw := fabricateRaw(t, "ev1", "uid1", 1000, 2000, "UTC", "FREQ=YEARLY", 0, nil, "Test Event", 0)
	raw.Color = "#EC3E7C"
	raw.Notifications = []caltypes.Notification{
		{Type: 1, Trigger: "-PT1H"},
		{Type: 0, Trigger: "-PT15M"},
	}

	ics, err := BuildICS(raw, calKR)
	if err != nil {
		t.Fatalf("BuildICS: %v", err)
	}

	for _, want := range []string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:" + ICSProdID,
		"BEGIN:VEVENT", "UID:uid1", "SUMMARY:Test Event",
		"RRULE:FREQ=YEARLY",
		"STATUS:CONFIRMED", "TRANSP:OPAQUE",
		"COLOR:#EC3E7C",
		"END:VEVENT", "END:VCALENDAR",
	} {
		if !strings.Contains(ics, want) {
			t.Errorf("ICS missing %q\n---\n%s", want, ics)
		}
	}

	// VALARMs injected from Notifications: one DISPLAY (Type 1), one EMAIL (Type 0).
	if n := strings.Count(ics, "BEGIN:VALARM"); n != 2 {
		t.Errorf("VALARM count = %d, want 2\n%s", n, ics)
	}
	if !strings.Contains(ics, "ACTION:DISPLAY") || !strings.Contains(ics, "TRIGGER:-PT1H") {
		t.Errorf("display alarm missing:\n%s", ics)
	}
	if !strings.Contains(ics, "ACTION:EMAIL") || !strings.Contains(ics, "TRIGGER:-PT15M") {
		t.Errorf("email alarm missing:\n%s", ics)
	}

	// VALARMs must sit inside the VEVENT (before END:VEVENT).
	endEvent := strings.Index(ics, "END:VEVENT")
	if a := strings.Index(ics, "BEGIN:VALARM"); a < 0 || a > endEvent {
		t.Errorf("VALARM not inside VEVENT")
	}
}

func TestBuildICSNilAndEmpty(t *testing.T) {
	if _, err := BuildICS(nil, nil); err == nil {
		t.Error("BuildICS(nil) should error")
	}
	// A row with only encrypted cards and no key yields nothing decryptable.
	calKR, _ := testKeys(t)
	raw := fabricateRaw(t, "ev1", "uid1", 1000, 2000, "UTC", "", 0, nil, "x", 0)
	// Drop the signed cards so only encrypted ones remain, then pass nil key.
	raw.SharedEvents = raw.SharedEvents[1:]
	raw.CalendarEvents = raw.CalendarEvents[1:]
	if _, err := BuildICS(raw, nil); err == nil {
		t.Error("BuildICS with no decryptable cards should return ErrDecryptDegraded")
	}
	_ = calKR
}
