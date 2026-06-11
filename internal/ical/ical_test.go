package ical

import (
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // embed tzdata so zone lookups work everywhere tests run
	"unicode/utf8"
)

// ptr returns a pointer to v (test helper for expected pointer fields).
func ptr(s string) *string { return &s }

func tsUTC(year int, month time.Month, day, hour, minute, sec int) int64 {
	return time.Date(year, month, day, hour, minute, sec, 0, time.UTC).Unix()
}

func TestEscapeText(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"plain", "hello world", "hello world"},
		{"comma", "a,b", `a\,b`},
		{"semicolon", "a;b", `a\;b`},
		{"backslash", `a\b`, `a\\b`},
		{"newline", "a\nb", `a\nb`},
		{"crlf collapses to newline", "a\r\nb", `a\nb`},
		{"bare cr dropped", "a\rb", "ab"},
		{"everything", "x,y;z\\w\nq", `x\,y\;z\\w\nq`},
		{"unicode passthrough", "café ☕", "café ☕"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeText(tt.in); got != tt.want {
				t.Errorf("escapeText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEscapeUnescapeRoundTrip(t *testing.T) {
	inputs := []string{
		"",
		"plain",
		"a,b;c\\d\ne",
		"trailing backslash not produced by escape",
		"multi\nline\nvalue",
		"unicode: héllo, wörld; ☕\\n",
	}
	for _, in := range inputs {
		// CR is destroyed by escapeText by design, so only CR-free inputs round-trip.
		if got := unescapeText(escapeText(in)); got != in {
			t.Errorf("round-trip %q -> %q", in, got)
		}
	}
}

func TestUnescapeTextTolerance(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{`a\nb`, "a\nb"},
		{`a\Nb`, "a\nb"},
		{`a\,b`, "a,b"},
		{`a\;b`, "a;b"},
		{`a\\b`, `a\b`},
		{`trailing\`, `trailing\`},             // lone trailing backslash kept
		{`unknown\xescape`, `unknown\xescape`}, // unknown escape kept verbatim
	}
	for _, tt := range tests {
		if got := unescapeText(tt.in); got != tt.want {
			t.Errorf("unescapeText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDTProp(t *testing.T) {
	utc4pm := time.Date(2026, 7, 9, 16, 0, 0, 0, time.UTC)
	melbourne, err := time.LoadLocation("Australia/Melbourne")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	tests := []struct {
		name   string
		prop   string
		t      time.Time
		tzName string
		allDay bool
		want   string
	}{
		{"utc z form empty tz", "DTSTART", utc4pm, "", false, "DTSTART:20260709T160000Z"},
		{"utc z form explicit", "DTSTART", utc4pm, "UTC", false, "DTSTART:20260709T160000Z"},
		{"tzid local form", "DTSTART", utc4pm, "America/Los_Angeles", false,
			"DTSTART;TZID=America/Los_Angeles:20260709T090000"},
		// Melbourne midnight is the previous day in UTC; the date must NOT shift.
		{"all day uses own wall date", "DTSTART",
			time.Date(2026, 7, 9, 0, 0, 0, 0, melbourne), "Australia/Melbourne", true,
			"DTSTART;VALUE=DATE:20260709"},
		{"all day ignores tz entirely", "DTEND",
			time.Date(2026, 7, 9, 0, 0, 0, 0, melbourne), "UTC", true,
			"DTEND;VALUE=DATE:20260709"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := dtProp(tt.prop, tt.t, tt.tzName, tt.allDay)
			if err != nil {
				t.Fatalf("dtProp: %v", err)
			}
			if got != tt.want {
				t.Errorf("dtProp = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("invalid zone errors", func(t *testing.T) {
		if _, err := dtProp("DTSTART", utc4pm, "Not/AZone", false); err == nil {
			t.Error("expected error for invalid zone")
		}
	})
	t.Run("invalid zone ignored when all day", func(t *testing.T) {
		got, err := dtProp("DTSTART", utc4pm, "Not/AZone", true)
		if err != nil || got != "DTSTART;VALUE=DATE:20260709" {
			t.Errorf("got %q, %v", got, err)
		}
	})
}

// baseFields is the shared fixture for fragment-building tests.
func baseFields() EventFields {
	return EventFields{
		UID:     "uid-123",
		DTStamp: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Start:   time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC),
		End:     time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC),
	}
}

func joinCRLF(lines ...string) string { return strings.Join(lines, "\r\n") }

func TestBuildFragmentsGolden(t *testing.T) {
	recID := time.Date(2026, 1, 9, 3, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		mutate func(*EventFields)
		want   Fragments
	}{
		{
			name:   "utc timed event",
			mutate: func(_ *EventFields) {},
			want: Fragments{
				SharedSigned: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"DTSTART:20260102T030000Z",
					"DTEND:20260102T040000Z",
					"SEQUENCE:0",
					"END:VEVENT", "END:VCALENDAR"),
				SharedEncrypted: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"CREATED:20260101T000000Z",
					"END:VEVENT", "END:VCALENDAR"),
				CalendarSigned: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"STATUS:CONFIRMED",
					"TRANSP:OPAQUE",
					"END:VEVENT", "END:VCALENDAR"),
				CalendarEncrypted: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"COMMENT:",
					"END:VEVENT", "END:VCALENDAR"),
			},
		},
		{
			name: "timed event with tzid",
			mutate: func(f *EventFields) {
				f.TZName = "Europe/Paris"
				f.Summary = "S"
			},
			want: Fragments{
				SharedSigned: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"DTSTART;TZID=Europe/Paris:20260102T040000",
					"DTEND;TZID=Europe/Paris:20260102T050000",
					"SEQUENCE:0",
					"END:VEVENT", "END:VCALENDAR"),
				SharedEncrypted: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"CREATED:20260101T000000Z",
					"SUMMARY:S",
					"END:VEVENT", "END:VCALENDAR"),
				CalendarSigned: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"STATUS:CONFIRMED",
					"TRANSP:OPAQUE",
					"END:VEVENT", "END:VCALENDAR"),
				CalendarEncrypted: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"COMMENT:",
					"END:VEVENT", "END:VCALENDAR"),
			},
		},
		{
			name: "all day event exclusive dtend",
			mutate: func(f *EventFields) {
				f.AllDay = true
				f.Start = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
				f.End = time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC) // exclusive
			},
			want: Fragments{
				SharedSigned: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"DTSTART;VALUE=DATE:20260102",
					"DTEND;VALUE=DATE:20260103",
					"SEQUENCE:0",
					"END:VEVENT", "END:VCALENDAR"),
				SharedEncrypted: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"CREATED:20260101T000000Z",
					"END:VEVENT", "END:VCALENDAR"),
				CalendarSigned: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"STATUS:CONFIRMED",
					"TRANSP:OPAQUE",
					"END:VEVENT", "END:VCALENDAR"),
				CalendarEncrypted: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"COMMENT:",
					"END:VEVENT", "END:VCALENDAR"),
			},
		},
		{
			name: "recurring master with exdates",
			mutate: func(f *EventFields) {
				f.TZName = "Europe/Paris"
				f.RRule = "FREQ=DAILY;COUNT=3"
				f.Exdates = []time.Time{
					time.Date(2026, 1, 9, 3, 0, 0, 0, time.UTC),
					time.Date(2026, 1, 10, 3, 0, 0, 0, time.UTC),
				}
				f.Sequence = 1
			},
			want: Fragments{
				SharedSigned: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"DTSTART;TZID=Europe/Paris:20260102T040000",
					"DTEND;TZID=Europe/Paris:20260102T050000",
					"RRULE:FREQ=DAILY;COUNT=3",
					"EXDATE;TZID=Europe/Paris:20260109T040000",
					"EXDATE;TZID=Europe/Paris:20260110T040000",
					"SEQUENCE:1",
					"END:VEVENT", "END:VCALENDAR"),
				SharedEncrypted: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"CREATED:20260101T000000Z",
					"END:VEVENT", "END:VCALENDAR"),
				CalendarSigned: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"EXDATE;TZID=Europe/Paris:20260109T040000",
					"EXDATE;TZID=Europe/Paris:20260110T040000",
					"STATUS:CONFIRMED",
					"TRANSP:OPAQUE",
					"END:VEVENT", "END:VCALENDAR"),
				CalendarEncrypted: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"COMMENT:",
					"END:VEVENT", "END:VCALENDAR"),
			},
		},
		{
			name: "exception row with recurrence id",
			mutate: func(f *EventFields) {
				f.RecurrenceID = &recID
				f.Sequence = 2
			},
			want: Fragments{
				SharedSigned: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"DTSTART:20260102T030000Z",
					"DTEND:20260102T040000Z",
					"RECURRENCE-ID:20260109T030000Z",
					"SEQUENCE:2",
					"END:VEVENT", "END:VCALENDAR"),
				SharedEncrypted: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"CREATED:20260101T000000Z",
					"END:VEVENT", "END:VCALENDAR"),
				CalendarSigned: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"STATUS:CONFIRMED",
					"TRANSP:OPAQUE",
					"END:VEVENT", "END:VCALENDAR"),
				CalendarEncrypted: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"COMMENT:",
					"END:VEVENT", "END:VCALENDAR"),
			},
		},
		{
			name: "text fields escaped",
			mutate: func(f *EventFields) {
				f.Summary = "lunch, brunch; dinner\\snack"
				f.Description = "line1\nline2"
				f.Location = "Cafe; back room, 2nd floor"
			},
			want: Fragments{
				SharedSigned: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"DTSTART:20260102T030000Z",
					"DTEND:20260102T040000Z",
					"SEQUENCE:0",
					"END:VEVENT", "END:VCALENDAR"),
				SharedEncrypted: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"CREATED:20260101T000000Z",
					`SUMMARY:lunch\, brunch\; dinner\\snack`,
					`DESCRIPTION:line1\nline2`,
					`LOCATION:Cafe\; back room\, 2nd floor`,
					"END:VEVENT", "END:VCALENDAR"),
				CalendarSigned: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"STATUS:CONFIRMED",
					"TRANSP:OPAQUE",
					"END:VEVENT", "END:VCALENDAR"),
				CalendarEncrypted: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"COMMENT:",
					"END:VEVENT", "END:VCALENDAR"),
			},
		},
		{
			name:   "sequence rendering",
			mutate: func(f *EventFields) { f.Sequence = 7 },
			want: Fragments{
				SharedSigned: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"DTSTART:20260102T030000Z",
					"DTEND:20260102T040000Z",
					"SEQUENCE:7",
					"END:VEVENT", "END:VCALENDAR"),
				SharedEncrypted: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"CREATED:20260101T000000Z",
					"END:VEVENT", "END:VCALENDAR"),
				CalendarSigned: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"STATUS:CONFIRMED",
					"TRANSP:OPAQUE",
					"END:VEVENT", "END:VCALENDAR"),
				CalendarEncrypted: joinCRLF(
					"BEGIN:VCALENDAR", "BEGIN:VEVENT",
					"UID:uid-123",
					"DTSTAMP:20260101T000000Z",
					"COMMENT:",
					"END:VEVENT", "END:VCALENDAR"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := baseFields()
			tt.mutate(&f)
			got, err := BuildFragments(f)
			if err != nil {
				t.Fatalf("BuildFragments: %v", err)
			}
			if got.SharedSigned != tt.want.SharedSigned {
				t.Errorf("SharedSigned:\ngot:  %q\nwant: %q", got.SharedSigned, tt.want.SharedSigned)
			}
			if got.SharedEncrypted != tt.want.SharedEncrypted {
				t.Errorf("SharedEncrypted:\ngot:  %q\nwant: %q", got.SharedEncrypted, tt.want.SharedEncrypted)
			}
			if got.CalendarSigned != tt.want.CalendarSigned {
				t.Errorf("CalendarSigned:\ngot:  %q\nwant: %q", got.CalendarSigned, tt.want.CalendarSigned)
			}
			if got.CalendarEncrypted != tt.want.CalendarEncrypted {
				t.Errorf("CalendarEncrypted:\ngot:  %q\nwant: %q", got.CalendarEncrypted, tt.want.CalendarEncrypted)
			}
		})
	}
}

func TestBuildFragmentsInvalidTimezone(t *testing.T) {
	f := baseFields()
	f.TZName = "Not/AZone"
	if _, err := BuildFragments(f); err == nil {
		t.Error("expected error for invalid timezone")
	}
}

func TestBuildFragmentsCRLFOnly(t *testing.T) {
	f := baseFields()
	f.Summary = "S"
	frags, err := BuildFragments(f)
	if err != nil {
		t.Fatal(err)
	}
	for name, frag := range map[string]string{
		"shared signed": frags.SharedSigned, "shared encrypted": frags.SharedEncrypted,
		"calendar signed": frags.CalendarSigned, "calendar encrypted": frags.CalendarEncrypted,
	} {
		lines := strings.Split(frag, "\r\n")
		if lines[0] != "BEGIN:VCALENDAR" || lines[len(lines)-1] != "END:VCALENDAR" {
			t.Errorf("%s: bad wrapper: %q", name, frag)
		}
		if strings.Contains(strings.ReplaceAll(frag, "\r\n", ""), "\n") {
			t.Errorf("%s: contains bare LF", name)
		}
		if strings.HasSuffix(frag, "\r\n") {
			t.Errorf("%s: has trailing CRLF", name)
		}
		if strings.Contains(frag, "VERSION") || strings.Contains(frag, "PRODID") {
			t.Errorf("%s: must not carry VERSION/PRODID", name)
		}
	}
}

func TestFoldLine(t *testing.T) {
	t.Run("short line unchanged", func(t *testing.T) {
		if got := foldLine("SUMMARY:hi"); got != "SUMMARY:hi" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("exactly 75 octets unchanged", func(t *testing.T) {
		line := "SUMMARY:" + strings.Repeat("a", 67)
		if len(line) != 75 {
			t.Fatalf("setup: len=%d", len(line))
		}
		if got := foldLine(line); got != line {
			t.Errorf("got %q", got)
		}
	})
	t.Run("long ascii folded at 75 octets", func(t *testing.T) {
		line := "SUMMARY:" + strings.Repeat("a", 200)
		got := foldLine(line)
		physical := strings.Split(got, "\r\n")
		if len(physical) < 2 {
			t.Fatal("expected folding")
		}
		if len(physical[0]) != 75 {
			t.Errorf("first physical line is %d octets, want 75", len(physical[0]))
		}
		for i, p := range physical {
			if len(p) > 75 {
				t.Errorf("physical line %d exceeds 75 octets (%d)", i, len(p))
			}
			if i > 0 && (len(p) == 0 || p[0] != ' ') {
				t.Errorf("continuation %d does not start with space: %q", i, p)
			}
		}
		if unfolded := unfoldLines(got); len(unfolded) != 1 || unfolded[0] != line {
			t.Errorf("unfold mismatch: %q", unfolded)
		}
	})
	t.Run("multi byte runes not split", func(t *testing.T) {
		line := "SUMMARY:" + strings.Repeat("é☕日", 40)
		got := foldLine(line)
		for i, p := range strings.Split(got, "\r\n") {
			if !utf8.ValidString(p) {
				t.Errorf("physical line %d splits a UTF-8 rune: %q", i, p)
			}
			if len(p) > 75 {
				t.Errorf("physical line %d exceeds 75 octets (%d)", i, len(p))
			}
		}
		if unfolded := unfoldLines(got); len(unfolded) != 1 || unfolded[0] != line {
			t.Errorf("unfold mismatch: %q", unfolded)
		}
	})
	t.Run("build then parse round trip", func(t *testing.T) {
		f := baseFields()
		f.Summary = strings.Repeat("a long summary, with; punctuation\\marks ", 8)
		f.Description = strings.Repeat("日本語テキスト☕", 30)
		frags, err := BuildFragments(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range strings.Split(frags.SharedEncrypted, "\r\n") {
			if len(p) > 75 {
				t.Errorf("physical line exceeds 75 octets: %q", p)
			}
		}
		ev, err := ParseFragment(frags.SharedEncrypted)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Summary == nil || *ev.Summary != f.Summary {
			t.Errorf("summary round-trip failed:\ngot:  %q\nwant: %q", deref(ev.Summary), f.Summary)
		}
		if ev.Description == nil || *ev.Description != f.Description {
			t.Errorf("description round-trip failed:\ngot:  %q\nwant: %q", deref(ev.Description), f.Description)
		}
	})
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestParseFragment(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    ParsedEvent
		wantErr bool
	}{
		{
			// A complete VCALENDAR populates every parsed field.
			name: "full vcalendar crlf",
			in: joinCRLF(
				"BEGIN:VCALENDAR", "BEGIN:VEVENT",
				"UID:u1",
				"DTSTAMP:20260101T000000Z",
				"DTSTART:20260102T030405Z",
				"DTEND:20260102T040405Z",
				"SUMMARY:Hello",
				"DESCRIPTION:World",
				"LOCATION:There",
				"STATUS:TENTATIVE",
				"END:VEVENT", "END:VCALENDAR"),
			want: ParsedEvent{
				UID: "u1", Summary: ptr("Hello"), Description: ptr("World"),
				Location: ptr("There"), Status: ptr("TENTATIVE"),
				StartTS: tsUTC(2026, 1, 2, 3, 4, 5), EndTS: tsUTC(2026, 1, 2, 4, 4, 5),
			},
		},
		{
			name: "bare lf line endings",
			in:   "BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:u2\nSUMMARY:LF style\nEND:VEVENT\nEND:VCALENDAR",
			want: ParsedEvent{UID: "u2", Summary: ptr("LF style")},
		},
		{
			name: "bare vevent without vcalendar",
			in:   "BEGIN:VEVENT\r\nUID:u3\r\nSUMMARY:bare\r\nEND:VEVENT",
			want: ParsedEvent{UID: "u3", Summary: ptr("bare")},
		},
		{
			name: "bare property list",
			in:   "UID:u4\r\nSUMMARY:no wrapper at all",
			want: ParsedEvent{UID: "u4", Summary: ptr("no wrapper at all")},
		},
		{
			name: "tzid datetime form",
			in: joinCRLF(
				"BEGIN:VEVENT",
				"DTSTART;TZID=Europe/Paris:20260102T040000",
				"DTEND;TZID=Europe/Paris:20260102T050000",
				"END:VEVENT"),
			want: ParsedEvent{StartTS: tsUTC(2026, 1, 2, 3, 0, 0), EndTS: tsUTC(2026, 1, 2, 4, 0, 0)},
		},
		{
			name: "value date form anchors at utc midnight",
			in: joinCRLF(
				"BEGIN:VEVENT",
				"DTSTART;VALUE=DATE:20260102",
				"DTEND;VALUE=DATE:20260103",
				"END:VEVENT"),
			want: ParsedEvent{StartTS: tsUTC(2026, 1, 2, 0, 0, 0), EndTS: tsUTC(2026, 1, 3, 0, 0, 0)},
		},
		{
			name: "naked date string",
			in:   "BEGIN:VEVENT\r\nDTSTART:20260102\r\nEND:VEVENT",
			want: ParsedEvent{StartTS: tsUTC(2026, 1, 2, 0, 0, 0)},
		},
		{
			name: "floating datetime without tzid treated as utc",
			in:   "BEGIN:VEVENT\r\nDTSTART:20260102T030000\r\nEND:VEVENT",
			want: ParsedEvent{StartTS: tsUTC(2026, 1, 2, 3, 0, 0)},
		},
		{
			name: "absent fields are nil pointers",
			in:   "BEGIN:VEVENT\r\nUID:u5\r\nEND:VEVENT",
			want: ParsedEvent{UID: "u5"},
		},
		{
			name: "empty fields are non-nil empty strings",
			in:   "BEGIN:VEVENT\r\nSUMMARY:\r\nLOCATION:\r\nEND:VEVENT",
			want: ParsedEvent{Summary: ptr(""), Location: ptr("")},
		},
		{
			name: "folded summary",
			in: "BEGIN:VEVENT\r\nSUMMARY:part one \r\n part two\r\n" +
				"DESCRIPTION:tab\r\n\tfolded\r\nEND:VEVENT",
			want: ParsedEvent{Summary: ptr("part one part two"), Description: ptr("tabfolded")},
		},
		{
			name: "escaped text values",
			in:   `BEGIN:VEVENT` + "\r\n" + `SUMMARY:a\,b\;c\\d\ne` + "\r\n" + `END:VEVENT`,
			want: ParsedEvent{Summary: ptr("a,b;c\\d\ne")},
		},
		{
			name: "sequence present",
			in:   "BEGIN:VEVENT\r\nSEQUENCE:7\r\nEND:VEVENT",
			want: ParsedEvent{Sequence: 7, HasSequence: true},
		},
		{
			name: "malformed sequence skipped",
			in:   "BEGIN:VEVENT\r\nSEQUENCE:abc\r\nEND:VEVENT",
			want: ParsedEvent{},
		},
		{
			name: "malformed datetimes skipped",
			in: joinCRLF(
				"BEGIN:VEVENT",
				"DTSTART:not-a-date",
				"DTEND;TZID=Nope/Nope:20260102T030000",
				"SUMMARY:still parsed",
				"END:VEVENT"),
			want: ParsedEvent{Summary: ptr("still parsed")},
		},
		{
			name: "valarm description does not leak",
			in: joinCRLF(
				"BEGIN:VCALENDAR", "BEGIN:VEVENT",
				"SUMMARY:outer",
				"BEGIN:VALARM",
				"DESCRIPTION:alarm text",
				"END:VALARM",
				"END:VEVENT", "END:VCALENDAR"),
			want: ParsedEvent{Summary: ptr("outer")},
		},
		{
			name: "vtimezone dtstart does not leak",
			in: joinCRLF(
				"BEGIN:VCALENDAR",
				"BEGIN:VTIMEZONE",
				"TZID:Europe/Paris",
				"BEGIN:STANDARD",
				"DTSTART:19701025T030000",
				"END:STANDARD",
				"END:VTIMEZONE",
				"BEGIN:VEVENT",
				"DTSTART:20260102T030000Z",
				"END:VEVENT",
				"END:VCALENDAR"),
			want: ParsedEvent{StartTS: tsUTC(2026, 1, 2, 3, 0, 0)},
		},
		{
			name: "unknown properties ignored",
			in: joinCRLF(
				"BEGIN:VEVENT",
				"X-CUSTOM;FOO=bar:whatever",
				"TRANSP:OPAQUE",
				"UID:u6",
				"END:VEVENT"),
			want: ParsedEvent{UID: "u6"},
		},
		{
			name: "quoted tzid parameter",
			in:   "BEGIN:VEVENT\r\nDTSTART;TZID=\"Europe/Paris\":20260102T040000\r\nEND:VEVENT",
			want: ParsedEvent{StartTS: tsUTC(2026, 1, 2, 3, 0, 0)},
		},
		{
			name:    "empty input errors",
			in:      "",
			wantErr: true,
		},
		{
			name:    "whitespace only errors",
			in:      "\r\n\r\n\n",
			wantErr: true,
		},
		{
			name:    "garbage without properties errors",
			in:      "this is not ical at all %%%",
			wantErr: true,
		},
		{
			name: "garbage with vevent wrapper is tolerated",
			in:   "BEGIN:VEVENT\r\n%%%garbage line\r\nEND:VEVENT",
			want: ParsedEvent{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseFragment(tt.in) // must never panic
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				if got != (ParsedEvent{}) {
					t.Errorf("error case should return zero ParsedEvent, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFragment: %v", err)
			}
			assertParsedEqual(t, got, tt.want)
		})
	}
}

func assertParsedEqual(t *testing.T, got, want ParsedEvent) {
	t.Helper()
	if got.UID != want.UID {
		t.Errorf("UID = %q, want %q", got.UID, want.UID)
	}
	assertStrPtr(t, "Summary", got.Summary, want.Summary)
	assertStrPtr(t, "Description", got.Description, want.Description)
	assertStrPtr(t, "Location", got.Location, want.Location)
	assertStrPtr(t, "Status", got.Status, want.Status)
	if got.StartTS != want.StartTS {
		t.Errorf("StartTS = %d, want %d", got.StartTS, want.StartTS)
	}
	if got.EndTS != want.EndTS {
		t.Errorf("EndTS = %d, want %d", got.EndTS, want.EndTS)
	}
	if got.Sequence != want.Sequence || got.HasSequence != want.HasSequence {
		t.Errorf("Sequence = (%d, %v), want (%d, %v)",
			got.Sequence, got.HasSequence, want.Sequence, want.HasSequence)
	}
}

func assertStrPtr(t *testing.T, field string, got, want *string) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s = %s, want %s", field, deref(got), deref(want))
	case *got != *want:
		t.Errorf("%s = %q, want %q", field, *got, *want)
	}
}

func TestParseFragmentRoundTripsBuiltFragments(t *testing.T) {
	f := baseFields()
	f.Summary = "Team sync, weekly; planning\\review"
	f.Description = "agenda:\nitem one\nitem two"
	f.Location = "Room 2; building A"
	f.Sequence = 3
	frags, err := BuildFragments(f)
	if err != nil {
		t.Fatal(err)
	}

	shared, err := ParseFragment(frags.SharedSigned)
	if err != nil {
		t.Fatal(err)
	}
	if shared.UID != f.UID {
		t.Errorf("UID = %q", shared.UID)
	}
	if shared.StartTS != f.Start.Unix() || shared.EndTS != f.End.Unix() {
		t.Errorf("times = (%d, %d), want (%d, %d)",
			shared.StartTS, shared.EndTS, f.Start.Unix(), f.End.Unix())
	}
	if !shared.HasSequence || shared.Sequence != 3 {
		t.Errorf("sequence = (%d, %v)", shared.Sequence, shared.HasSequence)
	}

	enc, err := ParseFragment(frags.SharedEncrypted)
	if err != nil {
		t.Fatal(err)
	}
	if enc.Summary == nil || *enc.Summary != f.Summary {
		t.Errorf("Summary = %s, want %q", deref(enc.Summary), f.Summary)
	}
	if enc.Description == nil || *enc.Description != f.Description {
		t.Errorf("Description = %s, want %q", deref(enc.Description), f.Description)
	}
	if enc.Location == nil || *enc.Location != f.Location {
		t.Errorf("Location = %s, want %q", deref(enc.Location), f.Location)
	}

	cal, err := ParseFragment(frags.CalendarSigned)
	if err != nil {
		t.Fatal(err)
	}
	if cal.Status == nil || *cal.Status != "CONFIRMED" {
		t.Errorf("Status = %s", deref(cal.Status))
	}
}

func TestParseFragmentSequence(t *testing.T) {
	wrap := func(lines ...string) string {
		all := append([]string{"BEGIN:VCALENDAR", "BEGIN:VEVENT"}, lines...)
		all = append(all, "END:VEVENT", "END:VCALENDAR")
		return joinCRLF(all...)
	}
	tests := []struct {
		name    string
		in      string
		want    int
		wantHas bool
	}{
		{"present", wrap("UID:u1", "SEQUENCE:5"), 5, true},
		{"zero", wrap("UID:u1", "SEQUENCE:0"), 0, true},
		{"absent", wrap("UID:u1"), 0, false},
		{"malformed value", wrap("SEQUENCE:abc"), 0, false},
		{"empty value", wrap("SEQUENCE:"), 0, false},
		{"folded sequence", wrap("SEQUENCE:1", "X-PAD:"+strings.Repeat("x", 100)), 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, err := ParseFragment(tt.in)
			if err != nil {
				t.Fatalf("ParseFragment: %v", err)
			}
			if ev.Sequence != tt.want || ev.HasSequence != tt.wantHas {
				t.Errorf("got (%d, %v), want (%d, %v)", ev.Sequence, ev.HasSequence, tt.want, tt.wantHas)
			}
		})
	}
}

func TestSequenceFromBuiltFragment(t *testing.T) {
	f := baseFields()
	f.Sequence = 9
	frags, err := BuildFragments(f)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := ParseFragment(frags.SharedSigned)
	if err != nil {
		t.Fatal(err)
	}
	if !ev.HasSequence || ev.Sequence != 9 {
		t.Errorf("got %d (has=%v), want 9", ev.Sequence, ev.HasSequence)
	}
}
