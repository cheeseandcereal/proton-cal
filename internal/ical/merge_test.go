package ical

import (
	"strings"
	"testing"
)

func TestMergeFragmentsUnionAndDedup(t *testing.T) {
	sharedSigned := joinCRLF(
		"BEGIN:VCALENDAR", "BEGIN:VEVENT",
		"UID:u1",
		"DTSTAMP:20260601T000000Z",
		"DTSTART;TZID=America/Los_Angeles:20260624T080000",
		"DTEND;TZID=America/Los_Angeles:20260624T083000",
		"SEQUENCE:0",
		"X-PM-CONFERENCE-ID;X-PM-PROVIDER=2:MQYTXG4HKC",
		"END:VEVENT", "END:VCALENDAR")
	sharedEnc := joinCRLF(
		"BEGIN:VCALENDAR", "BEGIN:VEVENT",
		"UID:u1", // duplicate, deduped
		"DTSTAMP:20260601T000000Z",
		"SUMMARY:Test Event",
		"DESCRIPTION:Some Test description",
		"CATEGORIES:work",
		"X-FOO-CUSTOM:third-party-value",
		"X-PM-SESSION-KEY:c2VjcmV0", // must be stripped
		"END:VEVENT", "END:VCALENDAR")
	calSigned := joinCRLF(
		"BEGIN:VCALENDAR", "BEGIN:VEVENT",
		"UID:u1", "DTSTAMP:20260601T000000Z",
		"STATUS:CONFIRMED", "TRANSP:OPAQUE",
		"END:VEVENT", "END:VCALENDAR")

	got := MergeFragments("-//test//EN",
		MergeCard{SharedSigned: true, Data: sharedSigned},
		MergeCard{Data: sharedEnc},
		MergeCard{Data: calSigned},
	)

	// Structural + wrapper.
	for _, want := range []string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//test//EN", "BEGIN:VEVENT",
		"UID:u1", "DTSTART;TZID=America/Los_Angeles:20260624T080000",
		"SUMMARY:Test Event", "DESCRIPTION:Some Test description",
		"STATUS:CONFIRMED", "TRANSP:OPAQUE",
		"X-PM-CONFERENCE-ID;X-PM-PROVIDER=2:MQYTXG4HKC",
		"CATEGORIES:work", "X-FOO-CUSTOM:third-party-value",
		"END:VEVENT", "END:VCALENDAR",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("merged output missing %q\n---\n%s", want, got)
		}
	}

	// Session key stripped.
	if strings.Contains(got, "X-PM-SESSION-KEY") {
		t.Errorf("X-PM-SESSION-KEY leaked into output:\n%s", got)
	}

	// UID appears exactly once despite being in three cards.
	if n := strings.Count(got, "\r\nUID:u1"); n != 1 {
		t.Errorf("UID appears %d times, want 1", n)
	}
}

func TestMergeFragmentsStructuralOverride(t *testing.T) {
	// A non-shared card lists DTSTART first; the shared-signed card must win.
	other := joinCRLF("BEGIN:VEVENT", "UID:u1", "DTSTART:20000101T000000Z", "END:VEVENT")
	shared := joinCRLF("BEGIN:VEVENT", "UID:u1", "DTSTART:20260624T150000Z", "END:VEVENT")

	got := MergeFragments("-//t//EN",
		MergeCard{Data: other},
		MergeCard{SharedSigned: true, Data: shared},
	)
	if !strings.Contains(got, "DTSTART:20260624T150000Z") {
		t.Errorf("shared-signed DTSTART did not win:\n%s", got)
	}
	if strings.Contains(got, "DTSTART:20000101T000000Z") {
		t.Errorf("stale DTSTART present:\n%s", got)
	}
	if n := strings.Count(got, "DTSTART:"); n != 1 {
		t.Errorf("DTSTART appears %d times, want 1", n)
	}
}

func TestMergeFragmentsExdateUnion(t *testing.T) {
	a := joinCRLF("BEGIN:VEVENT", "UID:u1",
		"EXDATE;VALUE=DATE:20260101", "EXDATE;VALUE=DATE:20260201", "END:VEVENT")
	b := joinCRLF("BEGIN:VEVENT", "UID:u1",
		"EXDATE;VALUE=DATE:20260201", "EXDATE;VALUE=DATE:20260301", "END:VEVENT")

	got := MergeFragments("-//t//EN", MergeCard{SharedSigned: true, Data: a}, MergeCard{Data: b})
	for _, d := range []string{"20260101", "20260201", "20260301"} {
		if !strings.Contains(got, "EXDATE;VALUE=DATE:"+d) {
			t.Errorf("missing EXDATE %s", d)
		}
	}
	if n := strings.Count(got, "EXDATE;VALUE=DATE:20260201"); n != 1 {
		t.Errorf("duplicate EXDATE not deduped (%d copies)", n)
	}
}

func TestMergeFragmentsPreservesValarm(t *testing.T) {
	withAlarm := joinCRLF("BEGIN:VEVENT", "UID:u1",
		"BEGIN:VALARM", "ACTION:DISPLAY", "TRIGGER:-PT10M", "DESCRIPTION:x", "END:VALARM",
		"END:VEVENT")
	got := MergeFragments("-//t//EN", MergeCard{SharedSigned: true, Data: withAlarm})
	if !strings.Contains(got, "BEGIN:VALARM") || !strings.Contains(got, "TRIGGER:-PT10M") {
		t.Errorf("VALARM not preserved:\n%s", got)
	}
}
