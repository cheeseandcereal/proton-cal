package event

import (
	"errors"
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

	ics, err := BuildICS(raw, calKR, ExtrasFromRow(raw))
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
	if _, err := BuildICS(nil, nil, RowExtras{}); err == nil {
		t.Error("BuildICS(nil) should error")
	}
	// A row with only encrypted cards and no key yields nothing decryptable.
	calKR, _ := testKeys(t)
	raw := fabricateRaw(t, "ev1", "uid1", 1000, 2000, "UTC", "", 0, nil, "x", 0)
	// Drop the signed cards so only encrypted ones remain, then pass nil key.
	raw.SharedEvents = raw.SharedEvents[1:]
	raw.CalendarEvents = raw.CalendarEvents[1:]
	if _, err := BuildICS(raw, nil, RowExtras{}); err == nil {
		t.Error("BuildICS with no decryptable cards should return ErrDecryptDegraded")
	}
	_ = calKR
}

func TestBuildICSEffectiveExtras(t *testing.T) {
	calKR, _ := testKeys(t)
	// A row with no own color/reminders: BuildICS injects whatever effective
	// extras the caller resolves (e.g. the calendar defaults).
	raw := fabricateRaw(t, "ev1", "uid1", 1000, 2000, "UTC", "", 0, nil, "Inherits", 0)
	extras := RowExtras{
		Color:         "#AABBCC",
		Notifications: []caltypes.Notification{{Type: 1, Trigger: "-PT15M"}},
	}
	ics, err := BuildICS(raw, calKR, extras)
	if err != nil {
		t.Fatalf("BuildICS: %v", err)
	}
	if !strings.Contains(ics, "COLOR:#AABBCC") {
		t.Errorf("effective COLOR missing:\n%s", ics)
	}
	if !strings.Contains(ics, "ACTION:DISPLAY") || !strings.Contains(ics, "TRIGGER:-PT15M") {
		t.Errorf("effective VALARM missing:\n%s", ics)
	}
}

func TestBuildSeriesICS(t *testing.T) {
	calKR, _ := testKeys(t)
	// recurrenceID timestamps for two edited occurrences.
	occ1 := int64(1_700_000_000)
	occ2 := int64(1_700_600_000)
	master := fabricateRaw(t, "m1", "uidS", 1000, 2000, "UTC", "FREQ=WEEKLY", 0, []int64{1_699_000_000}, "Weekly", 0)
	ex1 := fabricateRaw(t, "e1", "uidS", 1000, 2000, "UTC", "", occ1, nil, "Weekly edited 1", 1)
	ex2 := fabricateRaw(t, "e2", "uidS", 1000, 2000, "UTC", "", occ2, nil, "Weekly edited 2", 1)

	// Pass exceptions before the master to prove master-first ordering.
	rows := []*caltypes.RawEvent{ex2, master, ex1}
	ics, anyFailed, err := BuildSeriesICS(rows, calKR, nil)
	if err != nil {
		t.Fatalf("BuildSeriesICS: %v", err)
	}
	if anyFailed {
		t.Error("anyDecryptFailed should be false when all rows decrypt")
	}

	// One VCALENDAR, three VEVENTs.
	if strings.Count(ics, "BEGIN:VCALENDAR") != 1 || strings.Count(ics, "END:VCALENDAR") != 1 {
		t.Errorf("want exactly one VCALENDAR:\n%s", ics)
	}
	if n := strings.Count(ics, "BEGIN:VEVENT"); n != 3 {
		t.Errorf("VEVENT count = %d, want 3\n%s", n, ics)
	}
	// Master has the RRULE and an EXDATE; exceptions carry RECURRENCE-ID.
	if !strings.Contains(ics, "RRULE:FREQ=WEEKLY") {
		t.Errorf("master RRULE missing:\n%s", ics)
	}
	if n := strings.Count(ics, "RECURRENCE-ID"); n != 2 {
		t.Errorf("RECURRENCE-ID count = %d, want 2\n%s", n, ics)
	}
	// Master VEVENT comes first (its block has no RECURRENCE-ID).
	firstEvent := strings.Index(ics, "BEGIN:VEVENT")
	secondEvent := strings.Index(ics[firstEvent+1:], "BEGIN:VEVENT") + firstEvent + 1
	if strings.Contains(ics[firstEvent:secondEvent], "RECURRENCE-ID") {
		t.Errorf("first VEVENT should be the master (no RECURRENCE-ID):\n%s", ics[firstEvent:secondEvent])
	}
}

func TestBuildSeriesICSPartialDecrypt(t *testing.T) {
	calKR, _ := testKeys(t)
	master := fabricateRaw(t, "m1", "uidS", 1000, 2000, "UTC", "FREQ=WEEKLY", 0, nil, "Weekly", 0)
	ex := fabricateRaw(t, "e1", "uidS", 1000, 2000, "UTC", "", 1_700_000_000, nil, "edited", 1)
	// Make the exception undecryptable by removing all its cards.
	ex.SharedEvents = nil
	ex.CalendarEvents = nil
	ex.AttendeesEvents = nil

	ics, anyFailed, err := BuildSeriesICS([]*caltypes.RawEvent{master, ex}, calKR, nil)
	if err != nil {
		t.Fatalf("BuildSeriesICS: %v", err)
	}
	if !anyFailed {
		t.Error("anyDecryptFailed should be true when a row is skipped")
	}
	if n := strings.Count(ics, "BEGIN:VEVENT"); n != 1 {
		t.Errorf("VEVENT count = %d, want 1 (master only)\n%s", n, ics)
	}
}

func TestBuildSeriesICSAllUndecryptable(t *testing.T) {
	master := fabricateRaw(t, "m1", "uidS", 1000, 2000, "UTC", "FREQ=WEEKLY", 0, nil, "Weekly", 0)
	master.SharedEvents = master.SharedEvents[1:] // drop signed
	master.CalendarEvents = master.CalendarEvents[1:]
	master.AttendeesEvents = nil
	// nil key => encrypted cards unreadable.
	_, anyFailed, err := BuildSeriesICS([]*caltypes.RawEvent{master}, nil, nil)
	if !errors.Is(err, ErrDecryptDegraded) {
		t.Errorf("err = %v, want ErrDecryptDegraded", err)
	}
	if !anyFailed {
		t.Error("anyDecryptFailed should be true")
	}
}
