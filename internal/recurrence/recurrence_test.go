package recurrence

import (
	"strings"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal-go/internal/caltypes"
)

const (
	hourSec = 3600
	daySec  = 86400
)

// base is 2026-01-05 10:00 UTC, the anchor used by most expansion tests.
var base = time.Date(2026, time.January, 5, 10, 0, 0, 0, time.UTC).Unix()

// tsIn returns the unix timestamp for a wall-clock time in the given IANA zone.
func tsIn(t *testing.T, tz string, year int, month time.Month, day, hour int) int64 {
	t.Helper()
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", tz, err)
	}
	return time.Date(year, month, day, hour, 0, 0, 0, loc).Unix()
}

// eventOpts mirrors the Python make_event keyword arguments.
type eventOpts struct {
	id           string
	uid          string
	start        int64
	end          int64
	tz           string
	fullDay      int
	rrule        string
	recurrenceID int64
	exdates      []int64
}

// makeEvent builds a raw Proton event row with the plaintext recurrence
// metadata fields populated.
func makeEvent(o eventOpts) *caltypes.RawEvent {
	if o.id == "" {
		o.id = "ev1"
	}
	if o.uid == "" {
		o.uid = "uid-" + o.id
	}
	if o.tz == "" {
		o.tz = "UTC"
	}
	return &caltypes.RawEvent{
		ID:            o.id,
		UID:           o.uid,
		StartTime:     o.start,
		EndTime:       o.end,
		StartTimezone: o.tz,
		FullDay:       o.fullDay,
		RRule:         o.rrule,
		RecurrenceID:  o.recurrenceID,
		Exdates:       o.exdates,
	}
}

func dailyMaster(o eventOpts) *caltypes.RawEvent {
	if o.id == "" {
		o.id = "master1"
	}
	if o.start == 0 {
		o.start = base
	}
	if o.end == 0 {
		o.end = base + hourSec
	}
	if o.rrule == "" {
		o.rrule = "FREQ=DAILY;COUNT=3"
	}
	return makeEvent(o)
}

func mustBuild(t *testing.T, repeat string, every, count int, until, tzName string, allDay bool) string {
	t.Helper()
	got, err := BuildRRule(repeat, every, count, until, tzName, allDay)
	if err != nil {
		t.Fatalf("BuildRRule(%q, %d, %d, %q, %q, %v): %v", repeat, every, count, until, tzName, allDay, err)
	}
	return got
}

func wantBuildErr(t *testing.T, repeat string, every, count int, until, tzName string, allDay bool, substrs ...string) {
	t.Helper()
	got, err := BuildRRule(repeat, every, count, until, tzName, allDay)
	if err == nil {
		t.Fatalf("BuildRRule(%q, %d, %d, %q, %q, %v) = %q, want error", repeat, every, count, until, tzName, allDay, got)
	}
	for _, s := range substrs {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not contain %q", err, s)
		}
	}
}

func TestBuildRRule(t *testing.T) {
	t.Run("simple daily", func(t *testing.T) {
		if got := mustBuild(t, "daily", 1, 0, "", "UTC", false); got != "FREQ=DAILY" {
			t.Errorf("got %q, want FREQ=DAILY", got)
		}
	})
	t.Run("weekly interval and count", func(t *testing.T) {
		if got := mustBuild(t, "weekly", 2, 10, "", "UTC", false); got != "FREQ=WEEKLY;INTERVAL=2;COUNT=10" {
			t.Errorf("got %q, want FREQ=WEEKLY;INTERVAL=2;COUNT=10", got)
		}
	})
	t.Run("interval omitted when one", func(t *testing.T) {
		if got := mustBuild(t, "MONTHLY", 1, 5, "", "UTC", false); got != "FREQ=MONTHLY;COUNT=5" {
			t.Errorf("got %q, want FREQ=MONTHLY;COUNT=5", got)
		}
	})
	t.Run("until timed serializes end of day in UTC", func(t *testing.T) {
		// 2026-07-15 23:59:59 America/Los_Angeles is PDT (UTC-7) -> 06:59:59Z next day.
		got := mustBuild(t, "daily", 1, 0, "2026-07-15", "America/Los_Angeles", false)
		if got != "FREQ=DAILY;UNTIL=20260716T065959Z" {
			t.Errorf("got %q, want FREQ=DAILY;UNTIL=20260716T065959Z", got)
		}
	})
	t.Run("until timed with empty tz defaults to UTC", func(t *testing.T) {
		got := mustBuild(t, "daily", 1, 0, "2026-07-15", "", false)
		if got != "FREQ=DAILY;UNTIL=20260715T235959Z" {
			t.Errorf("got %q, want FREQ=DAILY;UNTIL=20260715T235959Z", got)
		}
	})
	t.Run("until all-day floating date", func(t *testing.T) {
		got := mustBuild(t, "daily", 1, 0, "2026-07-15", "UTC", true)
		if got != "FREQ=DAILY;UNTIL=20260715" {
			t.Errorf("got %q, want FREQ=DAILY;UNTIL=20260715", got)
		}
	})
	t.Run("until at max accepted", func(t *testing.T) {
		got := mustBuild(t, "daily", 1, 0, "2037-12-31", "UTC", true)
		if got != "FREQ=DAILY;UNTIL=20371231" {
			t.Errorf("got %q, want FREQ=DAILY;UNTIL=20371231", got)
		}
	})
	t.Run("bad frequency rejected", func(t *testing.T) {
		wantBuildErr(t, "hourly", 1, 0, "", "UTC", false, "daily", "weekly", "monthly", "yearly")
	})
	t.Run("count above max rejected", func(t *testing.T) {
		wantBuildErr(t, "daily", 1, 50, "", "UTC", false, "between 1 and 49")
	})
	t.Run("count below one rejected", func(t *testing.T) {
		// count=0 means "unset" in the Go API, so use a negative count.
		wantBuildErr(t, "daily", 1, -1, "", "UTC", false, "between 1 and 49")
	})
	t.Run("count and until mutually exclusive", func(t *testing.T) {
		wantBuildErr(t, "daily", 1, 3, "2026-07-15", "UTC", false, "mutually exclusive")
	})
	t.Run("until past 2037 rejected", func(t *testing.T) {
		wantBuildErr(t, "daily", 1, 0, "2038-01-01", "UTC", false, "2037-12-31")
	})
	t.Run("every below one rejected", func(t *testing.T) {
		wantBuildErr(t, "daily", 0, 0, "", "UTC", false, "at least 1")
	})
	t.Run("malformed until date rejected", func(t *testing.T) {
		wantBuildErr(t, "daily", 1, 0, "July 15 2026", "UTC", false, "invalid until date")
	})
	t.Run("bad timezone rejected", func(t *testing.T) {
		wantBuildErr(t, "daily", 1, 0, "2026-07-15", "Not/AZone", false, "invalid timezone")
	})
}

func mustSanitize(t *testing.T, raw string) string {
	t.Helper()
	got, err := SanitizeRRule(raw)
	if err != nil {
		t.Fatalf("SanitizeRRule(%q): %v", raw, err)
	}
	return got
}

func wantSanitizeErr(t *testing.T, raw string, substrs ...string) {
	t.Helper()
	got, err := SanitizeRRule(raw)
	if err == nil {
		t.Fatalf("SanitizeRRule(%q) = %q, want error", raw, got)
	}
	for _, s := range substrs {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not contain %q", err, s)
		}
	}
}

func TestSanitizeRRule(t *testing.T) {
	t.Run("valid passthrough", func(t *testing.T) {
		if got := mustSanitize(t, "FREQ=DAILY;COUNT=3"); got != "FREQ=DAILY;COUNT=3" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("strips RRULE prefix", func(t *testing.T) {
		if got := mustSanitize(t, "RRULE:FREQ=DAILY"); got != "FREQ=DAILY" {
			t.Errorf("got %q", got)
		}
		if got := mustSanitize(t, "rrule:FREQ=DAILY"); got != "FREQ=DAILY" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("canonicalizes lowercase input", func(t *testing.T) {
		if got := mustSanitize(t, "freq=weekly;interval=2"); got != "FREQ=WEEKLY;INTERVAL=2" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("CRLF injection rejected", func(t *testing.T) {
		wantSanitizeErr(t, "FREQ=DAILY\r\nSUMMARY:evil", "newline")
	})
	t.Run("garbage rejected", func(t *testing.T) {
		wantSanitizeErr(t, "hello world")
		wantSanitizeErr(t, "FREQ=DAILY;COUNT=abc", "invalid RRULE")
	})
	t.Run("missing FREQ rejected", func(t *testing.T) {
		wantSanitizeErr(t, "INTERVAL=2", "FREQ")
	})
	t.Run("unsupported FREQ rejected", func(t *testing.T) {
		wantSanitizeErr(t, "FREQ=HOURLY", "HOURLY")
	})
	t.Run("count above max rejected", func(t *testing.T) {
		wantSanitizeErr(t, "FREQ=DAILY;COUNT=50", "at most 49")
	})
	t.Run("count and until rejected", func(t *testing.T) {
		wantSanitizeErr(t, "FREQ=DAILY;COUNT=3;UNTIL=20261231T000000Z", "COUNT and UNTIL")
	})
	t.Run("until past 2037 rejected", func(t *testing.T) {
		wantSanitizeErr(t, "FREQ=DAILY;UNTIL=20380101T000000Z", "2037")
		wantSanitizeErr(t, "FREQ=DAILY;UNTIL=20380101", "2037")
	})
	t.Run("count at max accepted", func(t *testing.T) {
		if got := mustSanitize(t, "FREQ=DAILY;COUNT=49"); !strings.Contains(got, "COUNT=49") {
			t.Errorf("got %q, want COUNT=49 present", got)
		}
	})
	t.Run("BYSETPOS monthly preserved", func(t *testing.T) {
		in := "FREQ=MONTHLY;BYDAY=MO;BYSETPOS=2"
		if got := mustSanitize(t, in); got != in {
			t.Errorf("got %q, want %q", got, in)
		}
	})
	t.Run("DATE-form UNTIL preserved exactly", func(t *testing.T) {
		// The critical canonicalization property: a floating DATE-form
		// UNTIL (all-day) must NOT be re-serialized as a UTC datetime.
		in := "FREQ=DAILY;UNTIL=20371231"
		if got := mustSanitize(t, in); got != in {
			t.Errorf("got %q, want %q", got, in)
		}
	})
	t.Run("local datetime UNTIL preserved", func(t *testing.T) {
		in := "FREQ=DAILY;UNTIL=20261231T235959"
		if got := mustSanitize(t, in); got != in {
			t.Errorf("got %q, want %q", got, in)
		}
	})
	t.Run("other parts preserved verbatim", func(t *testing.T) {
		in := "freq=yearly;byMonth=3;byday=2su;wkst=su"
		want := "FREQ=YEARLY;BYMONTH=3;BYDAY=2SU;WKST=SU"
		if got := mustSanitize(t, in); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("duplicate keys rejected", func(t *testing.T) {
		wantSanitizeErr(t, "FREQ=DAILY;FREQ=WEEKLY", "duplicate")
	})
	t.Run("empty value rejected", func(t *testing.T) {
		wantSanitizeErr(t, "FREQ=DAILY;COUNT=", "invalid RRULE")
	})
	t.Run("empty input rejected", func(t *testing.T) {
		wantSanitizeErr(t, "", "invalid RRULE")
		wantSanitizeErr(t, "RRULE:", "invalid RRULE")
	})
	t.Run("malformed UNTIL rejected", func(t *testing.T) {
		wantSanitizeErr(t, "FREQ=DAILY;UNTIL=2026-12-31", "UNTIL")
	})
}

func occTimes(out []Occurrence) [][2]int64 {
	res := make([][2]int64, len(out))
	for i, o := range out {
		res[i] = [2]int64{o.Start, o.End}
	}
	return res
}

func occStarts(out []Occurrence) []int64 {
	res := make([]int64, len(out))
	for i, o := range out {
		res[i] = o.Start
	}
	return res
}

func eqInt64s(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestExpandOccurrences(t *testing.T) {
	t.Run("daily count inside window", func(t *testing.T) {
		master := dailyMaster(eventOpts{})
		out := ExpandOccurrences([]*caltypes.RawEvent{master}, base-daySec, base+10*daySec)
		want := [][2]int64{
			{base, base + hourSec},
			{base + daySec, base + daySec + hourSec},
			{base + 2*daySec, base + 2*daySec + hourSec},
		}
		got := occTimes(out)
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("occurrence %d: got %v, want %v", i, got[i], want[i])
			}
		}
		for i, o := range out {
			if o.Event != master {
				t.Errorf("occurrence %d backed by %v, want master", i, o.Event)
			}
		}
	})

	t.Run("window clipping keeps only middle occurrence", func(t *testing.T) {
		master := dailyMaster(eventOpts{})
		out := ExpandOccurrences([]*caltypes.RawEvent{master}, base+daySec, base+2*daySec)
		if len(out) != 1 || out[0].Start != base+daySec || out[0].End != base+daySec+hourSec {
			t.Errorf("got %v, want single occurrence at base+1d", occTimes(out))
		}
	})

	t.Run("exdate removes occurrence", func(t *testing.T) {
		master := dailyMaster(eventOpts{exdates: []int64{base + daySec}})
		out := ExpandOccurrences([]*caltypes.RawEvent{master}, base-daySec, base+10*daySec)
		if !eqInt64s(occStarts(out), []int64{base, base + 2*daySec}) {
			t.Errorf("got starts %v, want [base, base+2d]", occStarts(out))
		}
	})

	t.Run("exception row shadows master occurrence", func(t *testing.T) {
		master := dailyMaster(eventOpts{uid: "shared-uid"})
		exception := makeEvent(eventOpts{
			id:           "exc1",
			uid:          "shared-uid",
			start:        base + daySec + 2*hourSec, // moved 2h later
			end:          base + daySec + 3*hourSec,
			recurrenceID: base + daySec,
		})
		out := ExpandOccurrences([]*caltypes.RawEvent{master, exception}, base-daySec, base+10*daySec)
		var masterStarts []int64
		var exceptionOccs []Occurrence
		for _, o := range out {
			if o.Event == master {
				masterStarts = append(masterStarts, o.Start)
			} else if o.Event == exception {
				exceptionOccs = append(exceptionOccs, o)
			}
		}
		if !eqInt64s(masterStarts, []int64{base, base + 2*daySec}) {
			t.Errorf("master starts %v, want [base, base+2d]", masterStarts)
		}
		if len(exceptionOccs) != 1 ||
			exceptionOccs[0].Start != base+daySec+2*hourSec ||
			exceptionOccs[0].End != base+daySec+3*hourSec {
			t.Errorf("exception occurrences %v, want one at moved time", exceptionOccs)
		}
	})

	t.Run("plain event passthrough and overlap filtering", func(t *testing.T) {
		inside := makeEvent(eventOpts{id: "plain-in", start: base, end: base + hourSec})
		outside := makeEvent(eventOpts{id: "plain-out", start: base + 30*daySec, end: base + 30*daySec + hourSec})
		out := ExpandOccurrences([]*caltypes.RawEvent{inside, outside}, base-daySec, base+daySec)
		if len(out) != 1 || out[0].Event != inside || out[0].Start != base || out[0].End != base+hourSec {
			t.Errorf("got %v, want only the inside event", out)
		}
	})

	t.Run("first occurrence before window", func(t *testing.T) {
		master := dailyMaster(eventOpts{})
		out := ExpandOccurrences([]*caltypes.RawEvent{master}, base+daySec-hourSec, base+10*daySec)
		if !eqInt64s(occStarts(out), []int64{base + daySec, base + 2*daySec}) {
			t.Errorf("got starts %v, want [base+1d, base+2d]", occStarts(out))
		}
	})

	t.Run("long occurrence starting before window still included", func(t *testing.T) {
		// 2-day occurrences; the first starts a day before the window but overlaps it.
		master := makeEvent(eventOpts{
			id:    "long1",
			start: base,
			end:   base + 2*daySec,
			rrule: "FREQ=WEEKLY;COUNT=2",
		})
		out := ExpandOccurrences([]*caltypes.RawEvent{master}, base+daySec, base+3*daySec)
		if !eqInt64s(occStarts(out), []int64{base}) {
			t.Errorf("got starts %v, want [base]", occStarts(out))
		}
	})

	t.Run("DST transition keeps local time", func(t *testing.T) {
		// Weekly 09:00 America/Los_Angeles starting Mon 2026-02-02 (PST, UTC-8).
		// US DST starts 2026-03-08, so the 2026-03-09 occurrence is 09:00 PDT (UTC-7).
		la := "America/Los_Angeles"
		start := tsIn(t, la, 2026, time.February, 2, 9)
		master := makeEvent(eventOpts{
			id:    "dst1",
			start: start,
			end:   start + hourSec,
			tz:    la,
			rrule: "FREQ=WEEKLY;COUNT=6",
		})
		out := ExpandOccurrences([]*caltypes.RawEvent{master}, start-daySec, tsIn(t, la, 2026, time.March, 16, 0))
		expected := []int64{
			tsIn(t, la, 2026, time.February, 2, 9),
			tsIn(t, la, 2026, time.February, 9, 9),
			tsIn(t, la, 2026, time.February, 16, 9),
			tsIn(t, la, 2026, time.February, 23, 9),
			tsIn(t, la, 2026, time.March, 2, 9),
			tsIn(t, la, 2026, time.March, 9, 9),
		}
		if !eqInt64s(occStarts(out), expected) {
			t.Fatalf("got starts %v, want %v", occStarts(out), expected)
		}
		// The post-DST occurrence is NOT naive +7d arithmetic from the previous one.
		last, prev := out[len(out)-1].Start, out[len(out)-2].Start
		if last != prev+7*daySec-hourSec {
			t.Errorf("post-DST occurrence is %d, want prev+7d-1h", last)
		}
		if last == prev+7*daySec {
			t.Errorf("post-DST occurrence used naive +7d arithmetic")
		}
	})

	// DST SPIKE TEST (required by the port spec). Outcome: rrule-go DOES
	// preserve wall-clock time across DST transitions. Its iterator builds
	// each candidate occurrence with time.Date(y, m, d, hh, mm, ss, 0, loc)
	// in DTSTART's location, so the local wall-clock fields are held fixed
	// and the UTC offset is re-derived per occurrence. No workaround (e.g.
	// iterating in a fixed-offset wall-clock domain like the Python naive
	// approach) is needed: anchoring DTSTART in the event's StartTimezone
	// is sufficient.
	t.Run("DST spike: weekly 09:00 LA across 2026-03-08 transition", func(t *testing.T) {
		la := "America/Los_Angeles"
		// DTSTART in winter: Sun 2026-03-01 09:00 PST (UTC-8) = 17:00 UTC.
		start := tsIn(t, la, 2026, time.March, 1, 9)
		master := makeEvent(eventOpts{
			id:    "dst-spike",
			start: start,
			end:   start + hourSec,
			tz:    la,
			rrule: "FREQ=WEEKLY;COUNT=3",
		})
		out := ExpandOccurrences([]*caltypes.RawEvent{master}, start-daySec, start+30*daySec)
		if len(out) != 3 {
			t.Fatalf("got %d occurrences, want 3 (%v)", len(out), occStarts(out))
		}
		utc := func(i int) string { return time.Unix(out[i].Start, 0).UTC().Format("2006-01-02T15:04:05Z") }
		// Pre-transition occurrence: 09:00 PST = 17:00 UTC.
		if want := "2026-03-01T17:00:00Z"; utc(0) != want {
			t.Errorf("occurrence 0 = %s, want %s", utc(0), want)
		}
		// 2026-03-08 is the transition day itself; 09:00 local is already PDT.
		if want := "2026-03-08T16:00:00Z"; utc(1) != want {
			t.Errorf("occurrence 1 = %s, want %s", utc(1), want)
		}
		// Post-transition: 2026-03-15 must be 09:00 PDT = 16:00 UTC.
		if want := "2026-03-15T16:00:00Z"; utc(2) != want {
			t.Errorf("occurrence 2 = %s, want %s (wall-clock not preserved across DST)", utc(2), want)
		}
		if out[2].Start != tsIn(t, la, 2026, time.March, 15, 9) {
			t.Errorf("occurrence 2 is not 09:00 local PDT")
		}
	})

	t.Run("all-day master with floating UNTIL", func(t *testing.T) {
		// Regression: floating DATE-form UNTIL must not break expansion.
		start := time.Date(2026, time.July, 8, 0, 0, 0, 0, time.UTC).Unix() // midnight UTC
		master := makeEvent(eventOpts{
			id:      "allday1",
			start:   start,
			end:     start + daySec,
			fullDay: 1,
			rrule:   "FREQ=DAILY;UNTIL=20260712",
		})
		out := ExpandOccurrences([]*caltypes.RawEvent{master}, start-daySec, start+30*daySec)
		want := make([]int64, 5)
		for i := range want {
			want[i] = start + int64(i)*daySec
		}
		if !eqInt64s(occStarts(out), want) {
			t.Errorf("got starts %v, want %v", occStarts(out), want)
		}
		for i, o := range out {
			if o.End-o.Start != daySec {
				t.Errorf("occurrence %d duration %d, want 1 day", i, o.End-o.Start)
			}
		}
	})

	t.Run("malformed RRULE falls back to master row", func(t *testing.T) {
		master := dailyMaster(eventOpts{rrule: "FREQ=BOGUS"})
		out := ExpandOccurrences([]*caltypes.RawEvent{master}, base-daySec, base+10*daySec)
		if len(out) != 1 || out[0].Event != master || out[0].Start != base || out[0].End != base+hourSec {
			t.Errorf("got %v, want single master pass-through row", out)
		}
	})

	t.Run("bad timezone falls back to master row", func(t *testing.T) {
		master := dailyMaster(eventOpts{tz: "Not/AZone"})
		out := ExpandOccurrences([]*caltypes.RawEvent{master}, base-daySec, base+10*daySec)
		if len(out) != 1 || out[0].Event != master || out[0].Start != base {
			t.Errorf("got %v, want single master pass-through row", out)
		}
	})

	t.Run("results sorted across events", func(t *testing.T) {
		plain := makeEvent(eventOpts{id: "aaa-plain", start: base + daySec + hourSec, end: base + daySec + 2*hourSec})
		master := dailyMaster(eventOpts{id: "zzz-master"})
		out := ExpandOccurrences([]*caltypes.RawEvent{plain, master}, base-daySec, base+10*daySec)
		starts := occStarts(out)
		for i := 1; i < len(starts); i++ {
			if starts[i] < starts[i-1] {
				t.Errorf("starts not sorted: %v", starts)
			}
		}
		gotIDs := make([]string, len(out))
		for i, o := range out {
			gotIDs[i] = o.Event.ID
		}
		wantIDs := []string{"zzz-master", "zzz-master", "aaa-plain", "zzz-master"}
		if len(gotIDs) != len(wantIDs) {
			t.Fatalf("got IDs %v, want %v", gotIDs, wantIDs)
		}
		for i := range wantIDs {
			if gotIDs[i] != wantIDs[i] {
				t.Errorf("ID %d: got %q, want %q", i, gotIDs[i], wantIDs[i])
			}
		}
	})

	t.Run("ties sorted by event ID", func(t *testing.T) {
		a := makeEvent(eventOpts{id: "bbb", start: base, end: base + hourSec})
		b := makeEvent(eventOpts{id: "aaa", start: base, end: base + hourSec})
		out := ExpandOccurrences([]*caltypes.RawEvent{a, b}, base-daySec, base+daySec)
		if len(out) != 2 || out[0].Event.ID != "aaa" || out[1].Event.ID != "bbb" {
			t.Errorf("tie not broken by event ID: %v", out)
		}
	})

	t.Run("expansion cap", func(t *testing.T) {
		master := makeEvent(eventOpts{id: "cap1", start: daySec, end: daySec + hourSec, rrule: "FREQ=DAILY"})
		out := ExpandOccurrences([]*caltypes.RawEvent{master}, 0, 4000*daySec) // ~11 years
		if len(out) != MaxOccurrencesPerMaster {
			t.Errorf("got %d occurrences, want %d", len(out), MaxOccurrencesPerMaster)
		}
	})
}

func resolveMaster(o eventOpts) *caltypes.RawEvent {
	if o.id == "" {
		o.id = "master1"
	}
	if o.uid == "" {
		o.uid = "shared-uid"
	}
	if o.start == 0 {
		o.start = base
	}
	if o.end == 0 {
		o.end = base + hourSec
	}
	return makeEvent(o)
}

func TestResolveOccurrence(t *testing.T) {
	const rrule = "FREQ=DAILY;COUNT=3"

	t.Run("exception row match", func(t *testing.T) {
		master := resolveMaster(eventOpts{rrule: rrule})
		exception := makeEvent(eventOpts{
			id:           "exc1",
			uid:          "shared-uid",
			start:        base + daySec + hourSec,
			end:          base + daySec + 2*hourSec,
			recurrenceID: base + daySec,
		})
		kind, row, err := ResolveOccurrence(master, []*caltypes.RawEvent{master, exception}, base+daySec)
		if err != nil {
			t.Fatalf("ResolveOccurrence: %v", err)
		}
		if kind != KindException || row != exception {
			t.Errorf("got (%v, %v), want (KindException, exception row)", kind, row)
		}
	})

	t.Run("plain generated occurrence", func(t *testing.T) {
		master := resolveMaster(eventOpts{rrule: rrule})
		kind, row, err := ResolveOccurrence(master, []*caltypes.RawEvent{master}, base+daySec)
		if err != nil {
			t.Fatalf("ResolveOccurrence: %v", err)
		}
		if kind != KindOccurrence || row != nil {
			t.Errorf("got (%v, %v), want (KindOccurrence, nil)", kind, row)
		}
	})

	t.Run("exdated occurrence errors as deleted", func(t *testing.T) {
		master := resolveMaster(eventOpts{rrule: rrule, exdates: []int64{base + daySec}})
		_, _, err := ResolveOccurrence(master, []*caltypes.RawEvent{master}, base+daySec)
		if err == nil || !strings.Contains(err.Error(), "deleted") {
			t.Errorf("got err %v, want mention of deleted", err)
		}
	})

	t.Run("non-occurrence timestamp errors", func(t *testing.T) {
		master := resolveMaster(eventOpts{rrule: rrule})
		_, _, err := ResolveOccurrence(master, []*caltypes.RawEvent{master}, base+daySec+1234)
		if err == nil || !strings.Contains(err.Error(), "not an occurrence") {
			t.Errorf("got err %v, want mention of not an occurrence", err)
		}
	})

	t.Run("non-recurring master errors", func(t *testing.T) {
		master := resolveMaster(eventOpts{})
		_, _, err := ResolveOccurrence(master, []*caltypes.RawEvent{master}, base)
		if err == nil || !strings.Contains(err.Error(), "not a recurring event") {
			t.Errorf("got err %v, want mention of not a recurring event", err)
		}
	})

	t.Run("all-day master resolution", func(t *testing.T) {
		start := time.Date(2026, time.July, 8, 0, 0, 0, 0, time.UTC).Unix()
		master := makeEvent(eventOpts{
			id:      "allday1",
			start:   start,
			end:     start + daySec,
			fullDay: 1,
			rrule:   "FREQ=DAILY;UNTIL=20260712",
		})
		kind, row, err := ResolveOccurrence(master, []*caltypes.RawEvent{master}, start+2*daySec)
		if err != nil {
			t.Fatalf("ResolveOccurrence: %v", err)
		}
		if kind != KindOccurrence || row != nil {
			t.Errorf("got (%v, %v), want (KindOccurrence, nil)", kind, row)
		}
	})

	t.Run("malformed RRULE errors", func(t *testing.T) {
		master := resolveMaster(eventOpts{rrule: "FREQ=BOGUS"})
		_, _, err := ResolveOccurrence(master, []*caltypes.RawEvent{master}, base+daySec)
		if err == nil {
			t.Errorf("got nil err, want parse error")
		}
	})
}

func TestOccurrenceKindString(t *testing.T) {
	if KindException.String() != "exception" {
		t.Errorf("KindException.String() = %q", KindException.String())
	}
	if KindOccurrence.String() != "occurrence" {
		t.Errorf("KindOccurrence.String() = %q", KindOccurrence.String())
	}
	if got := OccurrenceKind(42).String(); got != "OccurrenceKind(42)" {
		t.Errorf("OccurrenceKind(42).String() = %q", got)
	}
}
