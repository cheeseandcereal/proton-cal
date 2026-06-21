package ical

import (
	"strings"
	"testing"
	"time"
)

func TestPatchCardReplacesPreservesAndAppends(t *testing.T) {
	card := strings.Join([]string{
		"BEGIN:VCALENDAR", "BEGIN:VEVENT",
		"UID:u1", "DTSTAMP:20260601T000000Z",
		"SUMMARY:Old",
		"LOCATION:here",
		"X-PM-CONFERENCE-URL;X-PM-HOST=a@b:https://meet/x",
		"X-THIRD-PARTY:keep",
		"END:VEVENT", "END:VCALENDAR",
	}, "\r\n")

	got := PatchCard(card, CardPatch{
		Set:    map[string]string{"SUMMARY": ":New", "DESCRIPTION": ":added"},
		Delete: map[string]bool{"LOCATION": true},
	})
	lines := unfoldLines(got)
	joined := strings.Join(lines, "\n")

	// Replaced in place (order preserved: SUMMARY stays where it was).
	if !strings.Contains(joined, "SUMMARY:New") || strings.Contains(joined, "SUMMARY:Old") {
		t.Errorf("SUMMARY not replaced in place:\n%s", joined)
	}
	// Deleted.
	if strings.Contains(joined, "LOCATION:") {
		t.Errorf("LOCATION not deleted:\n%s", joined)
	}
	// Preserved verbatim (the whole point of patch-not-rebuild).
	for _, want := range []string{
		"X-PM-CONFERENCE-URL;X-PM-HOST=a@b:https://meet/x",
		"X-THIRD-PARTY:keep",
		"UID:u1",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("lost %q:\n%s", want, joined)
		}
	}
	// Inserted absent property.
	if !strings.Contains(joined, "DESCRIPTION:added") {
		t.Errorf("DESCRIPTION not inserted:\n%s", joined)
	}
	// Wrapper intact, no VERSION/PRODID, CRLF separated.
	if !strings.HasPrefix(got, "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\n") ||
		!strings.HasSuffix(got, "\r\nEND:VEVENT\r\nEND:VCALENDAR") {
		t.Errorf("wrapper malformed:\n%q", got)
	}
	if strings.Contains(got, "VERSION:") || strings.Contains(got, "PRODID:") {
		t.Errorf("fragment must not carry VERSION/PRODID:\n%s", got)
	}
}

func TestPatchCardAppendDedupsAndPreservesComponents(t *testing.T) {
	card := strings.Join([]string{
		"BEGIN:VCALENDAR", "BEGIN:VEVENT",
		"UID:u1",
		"EXDATE:20260101T000000Z",
		"BEGIN:VALARM", "ACTION:DISPLAY", "TRIGGER:-PT10M", "END:VALARM",
		"END:VEVENT", "END:VCALENDAR",
	}, "\r\n")

	got := PatchCard(card, CardPatch{
		Append: []string{"EXDATE:20260101T000000Z", "EXDATE:20260102T000000Z"},
	})
	// Existing EXDATE not duplicated; new one added.
	if n := strings.Count(got, "EXDATE:20260101T000000Z"); n != 1 {
		t.Errorf("duplicate EXDATE (count=%d):\n%s", n, got)
	}
	if !strings.Contains(got, "EXDATE:20260102T000000Z") {
		t.Errorf("new EXDATE missing:\n%s", got)
	}
	// Nested VALARM preserved verbatim.
	if !strings.Contains(got, "BEGIN:VALARM") || !strings.Contains(got, "TRIGGER:-PT10M") {
		t.Errorf("VALARM not preserved:\n%s", got)
	}
}

func TestDateValueForms(t *testing.T) {
	ts := time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC)
	if v, err := DateValue(ts, "", false); err != nil || v != ":20260615T070000Z" {
		t.Errorf("UTC form = %q, %v", v, err)
	}
	if v, err := DateValue(ts, "America/Los_Angeles", false); err != nil || v != ";TZID=America/Los_Angeles:20260615T000000" {
		t.Errorf("zoned form = %q, %v", v, err)
	}
	if v, err := DateValue(ts, "America/Los_Angeles", true); err != nil || v != ";VALUE=DATE:20260615" {
		t.Errorf("all-day form = %q, %v", v, err)
	}
}
