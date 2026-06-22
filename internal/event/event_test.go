package event

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/config"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
	"github.com/cheeseandcereal/proton-cal/internal/pgp"
)

// ---------- shared fixtures ----------

var (
	fixtureOnce  sync.Once
	fixtureCalKR *crypto.KeyRing
	fixtureAdrKR *crypto.KeyRing
)

// testKeys generates (once) a calendar keyring and an address keyring.
func testKeys(t *testing.T) (calKR, addrKR *crypto.KeyRing) {
	t.Helper()
	fixtureOnce.Do(func() {
		ck, err := crypto.GenerateKey("calendar", "cal@test", "x25519", 0)
		if err != nil {
			panic(err)
		}
		ak, err := crypto.GenerateKey("address", "addr@test", "x25519", 0)
		if err != nil {
			panic(err)
		}
		fixtureCalKR, err = crypto.NewKeyRing(ck)
		if err != nil {
			panic(err)
		}
		fixtureAdrKR, err = crypto.NewKeyRing(ak)
		if err != nil {
			panic(err)
		}
	})
	return fixtureCalKR, fixtureAdrKR
}

func testAccess(t *testing.T) *calendar.Access {
	calKR, addrKR := testKeys(t)
	return &calendar.Access{
		CalendarID: "cal1",
		KR:         calKR,
		MemberID:   "member1",
		AddressID:  "addr1",
		AddrKR:     addrKR,
	}
}

func newTestClient(t *testing.T, handler http.Handler) *papi.Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv(config.EnvConfigDir, t.TempDir())
	store, err := config.NewSessionStore()
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	if err := store.Save(config.Session{UID: "u", AccessToken: "a", RefreshToken: "r"}); err != nil {
		t.Fatalf("saving session: %v", err)
	}
	client, err := papi.FromSession(store, srv.URL)
	if err != nil {
		t.Fatalf("FromSession: %v", err)
	}
	return client
}

func pinNow(t *testing.T, ts time.Time) {
	t.Helper()
	old := Now
	Now = func() time.Time { return ts }
	t.Cleanup(func() { Now = old })
}

func pinUID(t *testing.T, uid string) {
	t.Helper()
	old := NewUID
	NewUID = func() string { return uid }
	t.Cleanup(func() { NewUID = old })
}

// fixtureExtras are optional card properties for fabricating events shaped
// like web-client output (non-default status/transparency, original
// creation time, a comment).
type fixtureExtras struct {
	status  string // default CONFIRMED
	transp  string // default OPAQUE
	created string // iCal UTC datetime; "" = omit the CREATED line
	comment string // default empty
}

// fabricateRaw builds a server-side RawEvent encrypted with our fixtures,
// mirroring what a previous Create (or the web client, with extras) would
// have stored.
func fabricateRaw(t *testing.T, id, uid string, start, end int64, tz string, rrule string, recurrenceID int64, exdates []int64, summary string, sequence int, extras ...fixtureExtras) *caltypes.RawEvent {
	t.Helper()
	calKR, addrKR := testKeys(t)

	ex := fixtureExtras{status: "CONFIRMED", transp: "OPAQUE"}
	if len(extras) > 0 {
		ex = extras[0]
	}

	seqLine := "SEQUENCE:" + itoa(sequence)
	sharedSignedLines := []string{
		"BEGIN:VCALENDAR", "BEGIN:VEVENT",
		"UID:" + uid,
		"DTSTAMP:20260601T000000Z",
		"DTSTART;TZID=" + tz + ":20260615T090000",
		"DTEND;TZID=" + tz + ":20260615T093000",
	}
	if rrule != "" {
		sharedSignedLines = append(sharedSignedLines, "RRULE:"+rrule)
	}
	sharedSignedLines = append(sharedSignedLines, seqLine, "END:VEVENT", "END:VCALENDAR")
	sharedSigned := strings.Join(sharedSignedLines, "\r\n")

	sharedEncLines := []string{
		"BEGIN:VCALENDAR", "BEGIN:VEVENT",
		"UID:" + uid,
		"DTSTAMP:20260601T000000Z",
	}
	if ex.created != "" {
		sharedEncLines = append(sharedEncLines, "CREATED:"+ex.created)
	}
	sharedEncLines = append(sharedEncLines,
		"SUMMARY:"+summary,
		"DESCRIPTION:fixture description",
		"LOCATION:fixture location",
		"END:VEVENT", "END:VCALENDAR",
	)
	sharedEnc := strings.Join(sharedEncLines, "\r\n")
	calSigned := strings.Join([]string{
		"BEGIN:VCALENDAR", "BEGIN:VEVENT",
		"UID:" + uid, "DTSTAMP:20260601T000000Z",
		"STATUS:" + ex.status, "TRANSP:" + ex.transp,
		"END:VEVENT", "END:VCALENDAR",
	}, "\r\n")
	calEnc := strings.Join([]string{
		"BEGIN:VCALENDAR", "BEGIN:VEVENT",
		"UID:" + uid, "DTSTAMP:20260601T000000Z", "COMMENT:" + ex.comment,
		"END:VEVENT", "END:VCALENDAR",
	}, "\r\n")

	sharedKP, sharedData, sharedSig, err := pgp.EncryptAndSign(sharedEnc, calKR, addrKR)
	if err != nil {
		t.Fatalf("fabricating shared part: %v", err)
	}
	calKP, calData, calSig, err := pgp.EncryptAndSign(calEnc, calKR, addrKR)
	if err != nil {
		t.Fatalf("fabricating calendar part: %v", err)
	}
	sharedSignedSig, err := pgp.SignDetached(sharedSigned, addrKR)
	if err != nil {
		t.Fatal(err)
	}
	calSignedSig, err := pgp.SignDetached(calSigned, addrKR)
	if err != nil {
		t.Fatal(err)
	}

	return &caltypes.RawEvent{
		ID: id, UID: uid, CalendarID: "cal1",
		StartTime: start, EndTime: end,
		StartTimezone: tz, EndTimezone: tz,
		RRule: rrule, RecurrenceID: recurrenceID, Exdates: exdates,
		SharedKeyPacket:   sharedKP,
		CalendarKeyPacket: calKP,
		SharedEvents: []caltypes.EventPart{
			{Type: caltypes.CardSigned, Data: sharedSigned, Signature: sharedSignedSig},
			{Type: caltypes.CardEncryptedAndSigned, Data: sharedData, Signature: sharedSig},
		},
		CalendarEvents: []caltypes.EventPart{
			{Type: caltypes.CardSigned, Data: calSigned, Signature: calSignedSig},
			{Type: caltypes.CardEncryptedAndSigned, Data: calData, Signature: calSig},
		},
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// syncRecorder records sync PUT bodies and serves canned GET responses.
type syncRecorder struct {
	mu         sync.Mutex
	syncBodies []map[string]any
	respond    func(body map[string]any) any // per-PUT response factory; nil = default ok
	mux        *http.ServeMux
}

func newSyncRecorder() *syncRecorder {
	rec := &syncRecorder{mux: http.NewServeMux()}
	return rec
}

func (s *syncRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/events/sync") {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.syncBodies = append(s.syncBodies, body)
		respond := s.respond
		s.mu.Unlock()
		var resp any
		if respond != nil {
			resp = respond(body)
		} else {
			resp = map[string]any{
				"Code": 1001,
				"Responses": []any{map[string]any{
					"Index":    0,
					"Response": map[string]any{"Code": 1000},
				}},
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *syncRecorder) bodies() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.syncBodies...)
}

func (s *syncRecorder) handleJSON(path string, body any) {
	s.mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
}

func (s *syncRecorder) handleFunc(path string, fn http.HandlerFunc) {
	s.mux.HandleFunc(path, fn)
}

// ---------- Decrypt ----------

func TestDecryptRoundTrip(t *testing.T) {
	calKR, _ := testKeys(t)
	raw := fabricateRaw(t, "ev1", "uid1", 1000, 2000, "Europe/Berlin", "", 0, nil, "Team standup", 3)

	ev, err := Decrypt(raw, calKR)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if ev.Summary != "Team standup" {
		t.Errorf("summary = %q", ev.Summary)
	}
	if ev.Description != "fixture description" || ev.Location != "fixture location" {
		t.Errorf("desc/location = %q/%q", ev.Description, ev.Location)
	}
	if ev.Status != "CONFIRMED" {
		t.Errorf("status = %q", ev.Status)
	}
	if ev.Sequence != 3 {
		t.Errorf("sequence = %d, want 3", ev.Sequence)
	}
	if ev.RawSharedSigned == "" || !strings.Contains(ev.RawSharedSigned, "DTSTART") {
		t.Errorf("RawSharedSigned not captured: %q", ev.RawSharedSigned)
	}
	// Fragment DTSTART (09:00 Berlin = 07:00 UTC on 2026-06-15) overrides raw metadata.
	wantStart := time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC).Unix()
	if ev.Start.Unix() != wantStart {
		t.Errorf("Start = %d, want %d (fragment overrides metadata)", ev.Start.Unix(), wantStart)
	}
}

func TestDecryptEnrichesAttendeesConferenceRowFields(t *testing.T) {
	calKR, addrKR := testKeys(t)
	raw := fabricateRaw(t, "ev1", "uid1", 1000, 2000, "UTC", "", 0, nil, "Test Event", 0)

	// Conference ID lives in the shared SIGNED card; URL in the shared
	// ENCRYPTED card. Append both to the existing fabricated cards.
	raw.SharedEvents[0].Data = strings.Replace(raw.SharedEvents[0].Data,
		"\r\nEND:VEVENT", "\r\nX-PM-CONFERENCE-ID;X-PM-PROVIDER=2:MQYTXG4HKC\r\nEND:VEVENT", 1)
	// Re-sign the modified signed card so nothing downstream trips on it
	// (Decrypt itself does not verify, but keep the fixture honest).
	sig, err := pgp.SignDetached(raw.SharedEvents[0].Data, addrKR)
	if err != nil {
		t.Fatal(err)
	}
	raw.SharedEvents[0].Signature = sig

	// Attendees card: encrypted with the shared session key.
	attCard := strings.Join([]string{
		"BEGIN:VCALENDAR", "BEGIN:VEVENT",
		"UID:uid1", "DTSTAMP:20260601T000000Z",
		"ATTENDEE;X-PM-TOKEN=tok123;RSVP=TRUE;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION;CN=adacrowd:mailto:adacrowd@amazon.com",
		"END:VEVENT", "END:VCALENDAR",
	}, "\r\n")
	// Re-encrypt the conference URL into the shared encrypted card by
	// rebuilding it alongside the existing summary content.
	// DESCRIPTION carries the user's text plus Proton's embedded
	// conference block (separator-bracketed), exactly as Proton stores it.
	const sep = "~-~-~-~-~-~-~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~-~-~-~-~-~-~"
	desc := "Some Test description\\n" + sep +
		"\\nJoin Proton Meet: https://meet.proton.me/join/id-MQYTXG4HKC#pwd-secret123\\n" + sep
	sharedEnc := strings.Join([]string{
		"BEGIN:VCALENDAR", "BEGIN:VEVENT",
		"UID:uid1", "DTSTAMP:20260601T000000Z",
		"SUMMARY:Test Event",
		"DESCRIPTION:" + desc,
		"X-PM-CONFERENCE-URL;X-PM-HOST=adam@adamcrowder.net:https://meet.proton.me/join/id-MQYTXG4HKC#pwd-secret123",
		"END:VEVENT", "END:VCALENDAR",
	}, "\r\n")
	sharedKP, sharedData, sharedSig, err := pgp.EncryptAndSign(sharedEnc, calKR, addrKR)
	if err != nil {
		t.Fatal(err)
	}
	raw.SharedKeyPacket = sharedKP
	raw.SharedEvents[1] = caltypes.EventPart{Type: caltypes.CardEncryptedAndSigned, Data: sharedData, Signature: sharedSig}

	// Attendees card is encrypted with the SAME shared session key (the
	// server keeps one key packet for both cards), so derive it and reuse.
	sk, err := pgp.DecryptSessionKey(sharedKP, calKR)
	if err != nil {
		t.Fatal(err)
	}
	attData, attSig, err := pgp.EncryptWithSessionKeyAndSign(attCard, sk, addrKR)
	if err != nil {
		t.Fatal(err)
	}
	raw.AttendeesEvents = []caltypes.EventPart{
		{Type: caltypes.CardEncryptedAndSigned, Data: attData, Signature: attSig},
	}

	// Plaintext row fields.
	raw.Color = "#EC3E7C"
	raw.IsOrganizer = 1
	raw.Notifications = []caltypes.Notification{{Type: 1, Trigger: "-PT1H"}, {Type: 1, Trigger: "-PT15M"}}
	raw.Attendees = []caltypes.AttendeeToken{{ID: "a1", Token: "tok123", Status: 3}}
	raw.AttendeesInfo = caltypes.AttendeesInfo{MoreAttendees: 0}

	ev, err := Decrypt(raw, calKR)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if ev.DecryptFailed {
		t.Fatalf("unexpected DecryptFailed")
	}
	if ev.Color != "#EC3E7C" || !ev.IsOrganizer {
		t.Errorf("color/isOrganizer = %q/%v", ev.Color, ev.IsOrganizer)
	}
	// The embedded conference block must be stripped from the displayed
	// description (it is surfaced as a structured Conference field instead).
	if ev.Description != "Some Test description" {
		t.Errorf("description not cleaned: %q", ev.Description)
	}
	if len(ev.Notifications) != 2 || ev.Notifications[0].Trigger != "-PT1H" {
		t.Errorf("notifications = %+v", ev.Notifications)
	}
	if len(ev.Attendees) != 1 {
		t.Fatalf("attendees len = %d", len(ev.Attendees))
	}
	a := ev.Attendees[0]
	if a.Email != "adacrowd@amazon.com" || a.CN != "adacrowd" || a.Status != 3 {
		t.Errorf("attendee = %+v (want email/cn set, Status 3 from row join)", a)
	}
	if ev.Conference == nil {
		t.Fatal("conference = nil")
	}
	if ev.Conference.ID != "MQYTXG4HKC" || ev.Conference.Provider != "2" {
		t.Errorf("conference id/provider = %q/%q", ev.Conference.ID, ev.Conference.Provider)
	}
	if ev.Conference.URL != "https://meet.proton.me/join/id-MQYTXG4HKC#pwd-secret123" {
		t.Errorf("conference url = %q", ev.Conference.URL)
	}
	if ev.Conference.Password != "secret123" || ev.Conference.Host != "adam@adamcrowder.net" {
		t.Errorf("conference pwd/host = %q/%q", ev.Conference.Password, ev.Conference.Host)
	}
}

func TestDecryptLenientOnBadCard(t *testing.T) {
	calKR, _ := testKeys(t)
	raw := fabricateRaw(t, "ev1", "uid1", 1000, 2000, "UTC", "", 0, nil, "Good", 0)
	// Corrupt the encrypted shared card.
	raw.SharedEvents[1].Data = base64.StdEncoding.EncodeToString([]byte("garbage"))

	ev, err := Decrypt(raw, calKR)
	if err != nil {
		t.Fatalf("Decrypt should be lenient, got %v", err)
	}
	if ev.Summary != "" {
		t.Errorf("summary should be missing, got %q", ev.Summary)
	}
	if ev.Status != "CONFIRMED" {
		t.Errorf("calendar-signed part should still parse, status = %q", ev.Status)
	}
	if !ev.DecryptFailed {
		t.Error("DecryptFailed should be set when a card fails to decrypt")
	}
}

func TestUpdateRefusesDegradedMaster(t *testing.T) {
	raw := fabricateRaw(t, "ev1", "uid1", 1000, 2000, "UTC", "", 0, nil, "Good", 0)
	raw.SharedEvents[1].Data = base64.StdEncoding.EncodeToString([]byte("garbage"))

	rec := newSyncRecorder()
	serveExisting(rec, raw)
	client := newTestClient(t, rec)
	access := testAccess(t)

	summary := "New title"
	_, err := update(context.Background(), client, access, "ev1", UpdateOptions{Summary: &summary})
	if !errors.Is(err, ErrDecryptDegraded) {
		t.Fatalf("update of half-decrypted event must refuse with ErrDecryptDegraded, got %v", err)
	}
	if n := len(rec.bodies()); n != 0 {
		t.Fatalf("no sync write may happen on a degraded master, got %d", n)
	}
}

func TestMarshalNotificationsTriState(t *testing.T) {
	// Inherit (!set) -> null, so the server keeps applying the calendar
	// default and an update does not freeze a copy onto the event.
	if got, _ := marshalNotifications(nil, false); string(got) != "null" {
		t.Errorf("inherit -> %s, want null", got)
	}
	// Explicit none (set, empty) -> [] so the calendar default does NOT get
	// silently re-enabled on an event whose reminders were removed.
	if got, _ := marshalNotifications(nil, true); string(got) != "[]" {
		t.Errorf("explicit-none -> %s, want []", got)
	}
	if got, _ := marshalNotifications([]caltypes.Notification{}, true); string(got) != "[]" {
		t.Errorf("explicit-none (empty slice) -> %s, want []", got)
	}
	// Custom -> the array.
	got, _ := marshalNotifications([]caltypes.Notification{{Type: 1, Trigger: "-PT15M"}}, true)
	if string(got) != `[{"Type":1,"Trigger":"-PT15M"}]` {
		t.Errorf("custom -> %s", got)
	}
}

// ---------- Query ----------

// fakeEventsServer serves the windowed /events endpoint: it partitions the
// given rows across the four Type buckets and the More-paginated pages, and
// only returns a row when the requested [Start,End] window overlaps the row's
// [StartTime,EndTime]. It records the (Type,Page) keys it served. A nil
// bucketOf assigns every row to queryTypePartDayInside.
type fakeEventsServer struct {
	rows     []*caltypes.RawEvent
	bucketOf func(*caltypes.RawEvent) int

	mu     sync.Mutex
	served map[string]int // "type:page" -> count
}

func newFakeEventsServer(t *testing.T, rows []*caltypes.RawEvent, bucketOf func(*caltypes.RawEvent) int) (*fakeEventsServer, papi.API) {
	t.Helper()
	f := &fakeEventsServer{rows: rows, bucketOf: bucketOf, served: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/calendar/v1/cal1/events", f.handle)
	return f, newTestClient(t, mux)
}

func (f *fakeEventsServer) handle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	typ, _ := strconv.Atoi(q.Get("Type"))
	page, _ := strconv.Atoi(q.Get("Page"))
	start, _ := strconv.ParseInt(q.Get("Start"), 10, 64)
	end, _ := strconv.ParseInt(q.Get("End"), 10, 64)

	f.mu.Lock()
	f.served[itoa(typ)+":"+itoa(page)]++
	f.mu.Unlock()

	// Collect this Type's rows that overlap the requested window.
	var matched []*caltypes.RawEvent
	for _, ev := range f.rows {
		b := queryTypePartDayInside
		if f.bucketOf != nil {
			b = f.bucketOf(ev)
		}
		if b != typ {
			continue
		}
		if ev.StartTime < end && ev.EndTime > start {
			matched = append(matched, ev)
		}
	}

	lo := page * pageSize
	hi := min(lo+pageSize, len(matched))
	if lo > len(matched) {
		lo = len(matched)
	}
	resp := eventsResponse{Events: matched[lo:hi]}
	if hi < len(matched) {
		resp.More = 1
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeEventsServer) count(typ, page int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.served[itoa(typ)+":"+itoa(page)]
}

func TestQueryWindowAndDedup(t *testing.T) {
	rows := []*caltypes.RawEvent{
		{ID: "in-window", StartTime: 150, EndTime: 250},
		{ID: "outside", StartTime: 5_000_000, EndTime: 6_000_000},
		// Recurring master starting before the window, served under the
		// FullDayBefore bucket - must survive even though it does not overlap
		// [100,1000) by its own times.
		{ID: "master", StartTime: 5, EndTime: 10, RRule: "FREQ=DAILY", FullDay: 1},
	}
	bucket := func(ev *caltypes.RawEvent) int {
		if ev.ID == "master" {
			return queryTypeFullDayBefore
		}
		return queryTypePartDayInside
	}
	_, client := newFakeEventsServer(t, rows, bucket)

	got, err := query(context.Background(), client, "cal1", 100, 1000, "UTC")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	var ids []string
	for _, ev := range got {
		ids = append(ids, ev.ID)
	}
	// "outside" never overlaps any padded window; "in-window" and "master"
	// do (master via the before-window bucket). Sorted by StartTime:
	// master(5) first.
	if len(ids) != 2 || ids[0] != "master" || ids[1] != "in-window" {
		t.Errorf("ids = %v, want [master in-window]", ids)
	}
}

func TestQueryAllTypesQueried(t *testing.T) {
	f, client := newFakeEventsServer(t, []*caltypes.RawEvent{
		{ID: "a", StartTime: 150, EndTime: 250},
	}, nil)

	if _, err := query(context.Background(), client, "cal1", 100, 1000, "UTC"); err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, typ := range queryTypes {
		if n := f.count(typ, 0); n != 1 {
			t.Errorf("Type %d page 0 queried %d times, want exactly 1", typ, n)
		}
	}
}

func TestQueryChunksWideWindow(t *testing.T) {
	f, client := newFakeEventsServer(t, nil, nil)
	// A window far wider than maxWindowSeconds must be split into chunks the
	// server accepts; with padding the span is end-start+2*pad.
	start := int64(0)
	end := int64(3 * maxWindowSeconds)
	if _, err := query(context.Background(), client, "cal1", start, end, "UTC"); err != nil {
		t.Fatalf("query: %v", err)
	}
	// padded span = end + pad - max(0,start-pad) = 3*max + pad + pad = 3*max + 2*pad
	// chunks = ceil(span / max)
	span := (end + windowPadSeconds) - 0
	wantChunks := int((span + maxWindowSeconds - 1) / maxWindowSeconds)
	// Each chunk issues one page-0 request per Type.
	gotPage0 := 0
	f.mu.Lock()
	for k, n := range f.served {
		if strings.HasSuffix(k, ":0") {
			gotPage0 += n
		}
	}
	f.mu.Unlock()
	if want := wantChunks * len(queryTypes); gotPage0 != want {
		t.Errorf("page-0 requests = %d, want %d (%d chunks x %d types)", gotPage0, want, wantChunks, len(queryTypes))
	}
}

// ---------- Create ----------

func TestQueryPaginatesViaMoreCursor(t *testing.T) {
	const pages = 4
	const total = pageSize*(pages-1) + 17

	rows := make([]*caltypes.RawEvent, total)
	for i := range rows {
		rows[i] = &caltypes.RawEvent{ID: "ev" + itoa(i), StartTime: 100, EndTime: 200}
	}
	// All rows in one Type bucket and one chunk, so this exercises the
	// More-cursor page loop fully.
	f, client := newFakeEventsServer(t, rows, nil)

	got, err := query(context.Background(), client, "cal1", 0, 1000, "UTC")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != total {
		t.Fatalf("got %d events, want %d", len(got), total)
	}
	seen := make(map[string]bool, total)
	for _, ev := range got {
		if seen[ev.ID] {
			t.Fatalf("duplicate event %s", ev.ID)
		}
		seen[ev.ID] = true
	}
	for p := range pages {
		if n := f.count(queryTypePartDayInside, p); n != 1 {
			t.Errorf("PartDayInside page %d fetched %d times, want exactly once", p, n)
		}
	}
}

func TestCreatePayloadShape(t *testing.T) {
	pinNow(t, time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	pinUID(t, "fixed-uid-1")
	calKR, addrKR := testKeys(t)

	rec := newSyncRecorder()
	client := newTestClient(t, rec)
	access := testAccess(t)

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	created, err := Create(context.Background(), client, access, CreateOptions{
		Summary:  "Standup, daily; really",
		Location: "Room 1",
		Start:    start,
		End:      start.Add(30 * time.Minute),
		TZName:   "UTC",
		RRule:    "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = created // server did not echo an Event; nil is allowed

	bodies := rec.bodies()
	if len(bodies) != 1 {
		t.Fatalf("expected 1 sync call, got %d", len(bodies))
	}
	body := bodies[0]

	if body["MemberID"] != "member1" {
		t.Errorf("MemberID = %v", body["MemberID"])
	}
	if body["IsImport"] != float64(0) {
		t.Errorf("IsImport = %v, want 0", body["IsImport"])
	}
	events := body["Events"].([]any)
	entry := events[0].(map[string]any)
	if entry["Overwrite"] != float64(0) {
		t.Errorf("Overwrite = %v, want 0", entry["Overwrite"])
	}
	evBody := entry["Event"].(map[string]any)
	if evBody["Permissions"] != float64(1) {
		t.Errorf("Permissions = %v, want 1", evBody["Permissions"])
	}
	for _, key := range []string{"SharedKeyPacket", "CalendarKeyPacket"} {
		if s, _ := evBody[key].(string); s == "" {
			t.Errorf("%s missing/empty", key)
		}
	}
	// Explicit nulls and empty arrays.
	for _, key := range []string{"Notifications", "Color"} {
		v, present := evBody[key]
		if !present || v != nil {
			t.Errorf("%s: present=%v value=%v, want explicit null", key, present, v)
		}
	}
	for _, key := range []string{"AttendeesEventContent", "Attendees"} {
		v, present := evBody[key]
		arr, ok := v.([]any)
		if !present || !ok || len(arr) != 0 {
			t.Errorf("%s: present=%v value=%#v, want empty array", key, present, v)
		}
	}

	shared := evBody["SharedEventContent"].([]any)
	signedCard := shared[0].(map[string]any)
	encCard := shared[1].(map[string]any)
	if signedCard["Type"] != float64(2) || encCard["Type"] != float64(3) {
		t.Errorf("card types = %v/%v, want 2/3", signedCard["Type"], encCard["Type"])
	}

	// Signed card: fragment + verifiable signature; carries RRULE + UID.
	frag := signedCard["Data"].(string)
	if !strings.Contains(frag, "RRULE:FREQ=WEEKLY;COUNT=5") || !strings.Contains(frag, "UID:fixed-uid-1") {
		t.Errorf("shared signed fragment missing RRULE/UID:\n%s", frag)
	}
	sig, err := crypto.NewPGPSignatureFromArmored(signedCard["Signature"].(string))
	if err != nil {
		t.Fatalf("parsing signature: %v", err)
	}
	if err := addrKR.VerifyDetached(crypto.NewPlainMessage([]byte(frag)), sig, crypto.GetUnixTime()); err != nil {
		t.Errorf("shared signed signature does not verify: %v", err)
	}

	// Encrypted card decrypts via the posted key packet and contains the
	// escaped summary.
	plain, err := pgp.DecryptPart(encCard["Data"].(string), evBody["SharedKeyPacket"].(string), calKR)
	if err != nil {
		t.Fatalf("decrypting posted shared card: %v", err)
	}
	if !strings.Contains(plain, `SUMMARY:Standup\, daily\; really`) {
		t.Errorf("decrypted shared part missing escaped summary:\n%s", plain)
	}
}

func TestCreateWithRemindersAndColor(t *testing.T) {
	pinNow(t, time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	pinUID(t, "fixed-uid-2")
	rec := newSyncRecorder()
	client := newTestClient(t, rec)
	access := testAccess(t)

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	_, err := Create(context.Background(), client, access, CreateOptions{
		Summary: "Reminders", Start: start, End: start.Add(time.Hour), TZName: "UTC",
		Reminders: []caltypes.Notification{
			{Type: 1, Trigger: "-PT15M"},
			{Type: 0, Trigger: "-PT1H"},
		},
		RemindersSet: true,
		Color:        "#EC3E7C",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	evBody := rec.bodies()[0]["Events"].([]any)[0].(map[string]any)["Event"].(map[string]any)

	notifs, ok := evBody["Notifications"].([]any)
	if !ok || len(notifs) != 2 {
		t.Fatalf("Notifications = %#v, want 2-element array", evBody["Notifications"])
	}
	n0 := notifs[0].(map[string]any)
	if n0["Type"] != float64(1) || n0["Trigger"] != "-PT15M" {
		t.Errorf("first notification = %#v", n0)
	}
	if evBody["Color"] != "#EC3E7C" {
		t.Errorf("Color = %#v, want #EC3E7C", evBody["Color"])
	}
}

func TestCreateExplicitNoReminders(t *testing.T) {
	pinNow(t, time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC))
	pinUID(t, "fixed-uid-3")
	rec := newSyncRecorder()
	client := newTestClient(t, rec)
	access := testAccess(t)

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	// RemindersSet true, empty list -> explicit [] (none).
	if _, err := Create(context.Background(), client, access, CreateOptions{
		Summary: "NoReminders", Start: start, End: start.Add(time.Hour), TZName: "UTC",
		RemindersSet: true,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	evBody := rec.bodies()[0]["Events"].([]any)[0].(map[string]any)["Event"].(map[string]any)
	arr, ok := evBody["Notifications"].([]any)
	if !ok || len(arr) != 0 {
		t.Errorf("Notifications = %#v, want empty array", evBody["Notifications"])
	}
}

// ---------- Update ----------

// serveExisting wires GET event/{id} + UID listing for a fabricated event.
func serveExisting(rec *syncRecorder, raw *caltypes.RawEvent, related ...*caltypes.RawEvent) {
	rec.handleJSON("/calendar/v1/cal1/events/"+raw.ID, map[string]any{"Event": raw})
	for _, r := range related {
		rec.handleJSON("/calendar/v1/cal1/events/"+r.ID, map[string]any{"Event": r})
	}
	rec.handleFunc("/calendar/v1/cal1/events", func(w http.ResponseWriter, r *http.Request) {
		uid := r.URL.Query().Get("UID")
		var rows []*caltypes.RawEvent
		for _, cand := range append([]*caltypes.RawEvent{raw}, related...) {
			if cand.UID == uid {
				rows = append(rows, cand)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(eventsResponse{Events: rows, Total: len(rows)})
	})
}

func TestUpdateSummaryOnlyReusesSessionKeyAndKeepsSequence(t *testing.T) {
	pinNow(t, time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	calKR, _ := testKeys(t)
	start := time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC).Unix()
	raw := fabricateRaw(t, "ev1", "uid1", start, start+1800, "Europe/Berlin", "FREQ=DAILY;COUNT=5", 0, nil, "Old title", 2)

	rec := newSyncRecorder()
	serveExisting(rec, raw)
	client := newTestClient(t, rec)
	access := testAccess(t)

	newSummary := "New title"
	if _, err := update(context.Background(), client, access, "ev1", UpdateOptions{Summary: &newSummary}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	bodies := rec.bodies()
	if len(bodies) != 1 {
		t.Fatalf("expected 1 sync call, got %d", len(bodies))
	}
	body := bodies[0]
	if _, hasImport := body["IsImport"]; hasImport {
		t.Error("update payload must not carry IsImport")
	}
	entry := body["Events"].([]any)[0].(map[string]any)
	if entry["ID"] != "ev1" {
		t.Errorf("entry ID = %v", entry["ID"])
	}
	if _, hasOverwrite := entry["Overwrite"]; hasOverwrite {
		t.Error("update entry must not carry Overwrite")
	}
	evBody := entry["Event"].(map[string]any)
	for _, key := range []string{"SharedKeyPacket", "CalendarKeyPacket"} {
		if _, present := evBody[key]; present {
			t.Errorf("update payload must omit %s (server reuses session keys)", key)
		}
	}

	shared := evBody["SharedEventContent"].([]any)
	frag := shared[0].(map[string]any)["Data"].(string)
	if !strings.Contains(frag, "SEQUENCE:2") {
		t.Errorf("field-only edit must keep SEQUENCE:2, fragment:\n%s", frag)
	}
	if !strings.Contains(frag, "RRULE:FREQ=DAILY;COUNT=5") {
		t.Errorf("recurrence must be preserved, fragment:\n%s", frag)
	}
	if !strings.Contains(frag, "DTSTART;TZID=Europe/Berlin:20260615T090000") {
		t.Errorf("times/zone must be preserved, fragment:\n%s", frag)
	}

	// The new data packet must decrypt with the ORIGINAL session key.
	newData := shared[1].(map[string]any)["Data"].(string)
	plain, err := pgp.DecryptPart(newData, raw.SharedKeyPacket, calKR)
	if err != nil {
		t.Fatalf("new data packet does not decrypt with original key packet: %v", err)
	}
	if !strings.Contains(plain, "SUMMARY:New title") {
		t.Errorf("decrypted updated part:\n%s", plain)
	}
}

func TestUpdateRemindersAndColorTriState(t *testing.T) {
	pinNow(t, time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	start := time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC).Unix()

	// notificationsOf extracts the Notifications field from a recorded body.
	notificationsOf := func(rec *syncRecorder) any {
		return rec.bodies()[0]["Events"].([]any)[0].(map[string]any)["Event"].(map[string]any)["Notifications"]
	}
	colorOf := func(rec *syncRecorder) any {
		return rec.bodies()[0]["Events"].([]any)[0].(map[string]any)["Event"].(map[string]any)["Color"]
	}

	// withCustom returns a fresh raw carrying a custom reminder + color.
	withCustom := func(t *testing.T) *caltypes.RawEvent {
		raw := fabricateRaw(t, "ev1", "uid1", start, start+1800, "UTC", "", 0, nil, "T", 1)
		raw.Notifications = []caltypes.Notification{{Type: 1, Trigger: "-PT30M"}}
		raw.NotificationsSet = true
		raw.Color = "#415DF0"
		return raw
	}

	t.Run("keep re-sends existing", func(t *testing.T) {
		rec := newSyncRecorder()
		serveExisting(rec, withCustom(t))
		client := newTestClient(t, rec)
		s := "renamed"
		if _, err := update(context.Background(), client, testAccess(t), "ev1", UpdateOptions{Summary: &s}); err != nil {
			t.Fatalf("update: %v", err)
		}
		arr, ok := notificationsOf(rec).([]any)
		if !ok || len(arr) != 1 {
			t.Errorf("keep: Notifications = %#v, want the existing 1-element array", notificationsOf(rec))
		}
		if colorOf(rec) != "#415DF0" {
			t.Errorf("keep: Color = %#v, want preserved #415DF0", colorOf(rec))
		}
	})

	t.Run("inherit -> null", func(t *testing.T) {
		rec := newSyncRecorder()
		serveExisting(rec, withCustom(t))
		client := newTestClient(t, rec)
		if _, err := update(context.Background(), client, testAccess(t), "ev1", UpdateOptions{
			Reminders: &RemindersUpdate{Inherit: true},
		}); err != nil {
			t.Fatalf("update: %v", err)
		}
		if notificationsOf(rec) != nil {
			t.Errorf("inherit: Notifications = %#v, want null", notificationsOf(rec))
		}
	})

	t.Run("none -> empty array", func(t *testing.T) {
		rec := newSyncRecorder()
		serveExisting(rec, withCustom(t))
		client := newTestClient(t, rec)
		if _, err := update(context.Background(), client, testAccess(t), "ev1", UpdateOptions{
			Reminders: &RemindersUpdate{List: nil}, // !Inherit, empty -> []
		}); err != nil {
			t.Fatalf("update: %v", err)
		}
		arr, ok := notificationsOf(rec).([]any)
		if !ok || len(arr) != 0 {
			t.Errorf("none: Notifications = %#v, want empty array", notificationsOf(rec))
		}
	})

	t.Run("custom + color override", func(t *testing.T) {
		rec := newSyncRecorder()
		serveExisting(rec, withCustom(t))
		client := newTestClient(t, rec)
		if _, err := update(context.Background(), client, testAccess(t), "ev1", UpdateOptions{
			Reminders: &RemindersUpdate{List: []caltypes.Notification{{Type: 0, Trigger: "-PT2H"}}},
			Color:     &ColorUpdate{Value: "#112233"},
		}); err != nil {
			t.Fatalf("update: %v", err)
		}
		arr, ok := notificationsOf(rec).([]any)
		if !ok || len(arr) != 1 || arr[0].(map[string]any)["Trigger"] != "-PT2H" {
			t.Errorf("custom: Notifications = %#v", notificationsOf(rec))
		}
		if colorOf(rec) != "#112233" {
			t.Errorf("color override = %#v, want #112233", colorOf(rec))
		}
	})

	t.Run("color inherit -> null", func(t *testing.T) {
		rec := newSyncRecorder()
		serveExisting(rec, withCustom(t))
		client := newTestClient(t, rec)
		if _, err := update(context.Background(), client, testAccess(t), "ev1", UpdateOptions{
			Color: &ColorUpdate{Value: ""},
		}); err != nil {
			t.Fatalf("update: %v", err)
		}
		if colorOf(rec) != nil {
			t.Errorf("color inherit: Color = %#v, want null", colorOf(rec))
		}
	})
}

func TestUpdateStartBumpsSequenceAndClearRRule(t *testing.T) {
	pinNow(t, time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	start := time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC).Unix()
	raw := fabricateRaw(t, "ev1", "uid1", start, start+1800, "UTC", "FREQ=DAILY", 0, []int64{start + 86400}, "T", 1)

	rec := newSyncRecorder()
	serveExisting(rec, raw)
	client := newTestClient(t, rec)
	access := testAccess(t)

	newStart := time.Date(2026, 6, 16, 8, 0, 0, 0, time.UTC)
	if _, err := update(context.Background(), client, access, "ev1", UpdateOptions{Start: &newStart, ClearRRule: true}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	frag := rec.bodies()[0]["Events"].([]any)[0].(map[string]any)["Event"].(map[string]any)["SharedEventContent"].([]any)[0].(map[string]any)["Data"].(string)
	if !strings.Contains(frag, "SEQUENCE:2") {
		t.Errorf("significant edit must bump SEQUENCE to 2:\n%s", frag)
	}
	if strings.Contains(frag, "RRULE") || strings.Contains(frag, "EXDATE") {
		t.Errorf("ClearRRule must strip recurrence and exdates:\n%s", frag)
	}
	if !strings.Contains(frag, "DTSTART:20260616T080000Z") {
		t.Errorf("new start missing:\n%s", frag)
	}
}

// TestUpdatePreservesUnknownProperties is the regression guard for the
// data-loss bug: updating one field must not drop conferencing, organizer,
// attendees or any third-party property the writer doesn't model. The update
// patches existing cards in place rather than rebuilding from a fixed field
// set.
func TestUpdatePreservesUnknownProperties(t *testing.T) {
	pinNow(t, time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	calKR, addrKR := testKeys(t)
	uid := "uid1"

	// Shared encrypted card with user text PLUS conferencing/organizer and a
	// third-party property the writer knows nothing about.
	sharedEnc := strings.Join([]string{
		"BEGIN:VCALENDAR", "BEGIN:VEVENT",
		"UID:" + uid, "DTSTAMP:20260601T000000Z", "CREATED:20250101T080000Z",
		"SUMMARY:Test Event",
		"DESCRIPTION:keep me",
		"LOCATION:old location",
		"ORGANIZER;CN=Me:mailto:me@example.com",
		"X-PM-CONFERENCE-URL;X-PM-HOST=me@example.com:https://meet.proton.me/join/id-ABC#pwd-xyz",
		"X-WR-SOMETHING:third-party-value",
		"END:VEVENT", "END:VCALENDAR",
	}, "\r\n")
	sharedSigned := strings.Join([]string{
		"BEGIN:VCALENDAR", "BEGIN:VEVENT",
		"UID:" + uid, "DTSTAMP:20260601T000000Z",
		"DTSTART;TZID=Europe/Berlin:20260615T090000",
		"DTEND;TZID=Europe/Berlin:20260615T093000",
		"X-PM-CONFERENCE-ID;X-PM-PROVIDER=2:ABC",
		"SEQUENCE:2",
		"END:VEVENT", "END:VCALENDAR",
	}, "\r\n")
	attCard := strings.Join([]string{
		"BEGIN:VCALENDAR", "BEGIN:VEVENT",
		"UID:" + uid, "DTSTAMP:20260601T000000Z",
		"ATTENDEE;CN=Bob;ROLE=REQ-PARTICIPANT;X-PM-TOKEN=tok1:mailto:bob@example.com",
		"END:VEVENT", "END:VCALENDAR",
	}, "\r\n")

	sharedKP, sharedData, sharedSig, err := pgp.EncryptAndSign(sharedEnc, calKR, addrKR)
	if err != nil {
		t.Fatal(err)
	}
	sharedSignedSig, err := pgp.SignDetached(sharedSigned, addrKR)
	if err != nil {
		t.Fatal(err)
	}
	sk, err := pgp.DecryptSessionKey(sharedKP, calKR)
	if err != nil {
		t.Fatal(err)
	}
	attData, attSig, err := pgp.EncryptWithSessionKeyAndSign(attCard, sk, addrKR)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC).Unix()
	raw := &caltypes.RawEvent{
		ID: "ev1", UID: uid, CalendarID: "cal1",
		StartTime: start, EndTime: start + 1800,
		StartTimezone: "Europe/Berlin", EndTimezone: "Europe/Berlin",
		// No CalendarKeyPacket: this event (like a web-app event) has only a
		// signed calendar card, exercising the empty-calendar-key path too.
		SharedKeyPacket: sharedKP,
		Color:           "#EC3E7C",
		Notifications:   []caltypes.Notification{{Type: 1, Trigger: "-PT1H"}, {Type: 0, Trigger: "-PT15M"}},
		Attendees:       []caltypes.AttendeeToken{{ID: "a1", Token: "tok1", Status: 3}},
		SharedEvents: []caltypes.EventPart{
			{Type: caltypes.CardSigned, Data: sharedSigned, Signature: sharedSignedSig},
			{Type: caltypes.CardEncryptedAndSigned, Data: sharedData, Signature: sharedSig},
		},
		AttendeesEvents: []caltypes.EventPart{
			{Type: caltypes.CardEncryptedAndSigned, Data: attData, Signature: attSig},
		},
	}

	rec := newSyncRecorder()
	serveExisting(rec, raw)
	client := newTestClient(t, rec)
	access := testAccess(t)

	newLoc := "new location"
	if _, err := update(context.Background(), client, access, "ev1", UpdateOptions{Location: &newLoc}); err != nil {
		t.Fatalf("update: %v", err)
	}

	evBody := rec.bodies()[0]["Events"].([]any)[0].(map[string]any)["Event"].(map[string]any)
	// No key packets on an update.
	for _, k := range []string{"SharedKeyPacket", "CalendarKeyPacket"} {
		if _, ok := evBody[k]; ok {
			t.Errorf("update must omit %s", k)
		}
	}
	// Row-level personal data must be re-sent (not null/[]) so the sync call
	// does not wipe reminders, color or attendee RSVP rows.
	notifs, ok := evBody["Notifications"].([]any)
	if !ok || len(notifs) != 2 {
		t.Fatalf("Notifications must carry the 2 existing reminders, got %#v", evBody["Notifications"])
	}
	if n0 := notifs[0].(map[string]any); n0["Trigger"] != "-PT1H" || n0["Type"] != float64(1) {
		t.Errorf("notification[0] = %#v", n0)
	}
	if evBody["Color"] != "#EC3E7C" {
		t.Errorf("Color must be preserved, got %v", evBody["Color"])
	}
	atts, ok := evBody["Attendees"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("Attendees clear rows must be re-sent, got %#v", evBody["Attendees"])
	}
	if a0 := atts[0].(map[string]any); a0["Token"] != "tok1" || a0["Status"] != float64(3) {
		t.Errorf("attendee clear row = %#v", a0)
	}
	// unfold reverses RFC 5545 line folding so substring checks see logical
	// lines (long properties are folded at 75 octets on output).
	unfold := func(s string) string { return strings.ReplaceAll(s, "\r\n ", "") }

	shared := evBody["SharedEventContent"].([]any)
	signedFrag := unfold(shared[0].(map[string]any)["Data"].(string))
	encRaw, err := pgp.DecryptPart(shared[1].(map[string]any)["Data"].(string), sharedKP, calKR)
	if err != nil {
		t.Fatalf("decrypting updated shared enc: %v", err)
	}
	encFrag := unfold(encRaw)

	// The edit applied.
	if !strings.Contains(encFrag, "LOCATION:new location") {
		t.Errorf("location not updated:\n%s", encFrag)
	}
	// Everything else preserved verbatim.
	for _, want := range []string{
		"DESCRIPTION:keep me",
		"SUMMARY:Test Event",
		"ORGANIZER;CN=Me:mailto:me@example.com",
		"X-PM-CONFERENCE-URL;X-PM-HOST=me@example.com:https://meet.proton.me/join/id-ABC#pwd-xyz",
		"X-WR-SOMETHING:third-party-value",
	} {
		if !strings.Contains(encFrag, want) {
			t.Errorf("shared-encrypted lost %q:\n%s", want, encFrag)
		}
	}
	for _, want := range []string{"X-PM-CONFERENCE-ID;X-PM-PROVIDER=2:ABC", "SEQUENCE:2"} {
		if !strings.Contains(signedFrag, want) {
			t.Errorf("shared-signed lost %q:\n%s", want, signedFrag)
		}
	}
	// Attendees card must survive and still decrypt.
	att := evBody["AttendeesEventContent"].([]any)
	if len(att) != 1 {
		t.Fatalf("attendees card dropped, got %d parts", len(att))
	}
	attPlain, err := pgp.DecryptPart(att[0].(map[string]any)["Data"].(string), sharedKP, calKR)
	if err != nil {
		t.Fatalf("decrypting attendees: %v", err)
	}
	if !strings.Contains(attPlain, "ATTENDEE;CN=Bob") {
		t.Errorf("attendee lost:\n%s", attPlain)
	}
}

// ---------- SmartDelete ----------

func TestSmartDeletePlainEvent(t *testing.T) {
	raw := &caltypes.RawEvent{ID: "ev1", UID: "uid1", StartTime: 1, EndTime: 2}
	rec := newSyncRecorder()
	serveExisting(rec, raw)
	client := newTestClient(t, rec)

	res, err := SmartDelete(context.Background(), client, testAccess(t), "ev1", 0)
	if err != nil {
		t.Fatalf("SmartDelete: %v", err)
	}
	if res.Kind != "event" || res.RowsDeleted != 1 {
		t.Errorf("result = %+v", res)
	}
	entry := rec.bodies()[0]["Events"].([]any)
	if len(entry) != 1 || entry[0].(map[string]any)["ID"] != "ev1" {
		t.Errorf("delete entries = %v", entry)
	}
}

func TestSmartDeleteSeriesBatchesAllRows(t *testing.T) {
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC).Unix()
	master := &caltypes.RawEvent{ID: "master", UID: "uid1", StartTime: start, EndTime: start + 1800, RRule: "FREQ=DAILY"}
	exception := &caltypes.RawEvent{ID: "exc1", UID: "uid1", StartTime: start + 86400, EndTime: start + 88200, RecurrenceID: start + 86400}
	rec := newSyncRecorder()
	serveExisting(rec, master, exception)
	client := newTestClient(t, rec)

	res, err := SmartDelete(context.Background(), client, testAccess(t), "master", 0)
	if err != nil {
		t.Fatalf("SmartDelete: %v", err)
	}
	if res.Kind != "series" || res.RowsDeleted != 2 {
		t.Errorf("result = %+v", res)
	}
	bodies := rec.bodies()
	if len(bodies) != 1 {
		t.Fatalf("series delete must be ONE batched sync call, got %d", len(bodies))
	}
	entries := bodies[0]["Events"].([]any)
	if len(entries) != 2 {
		t.Errorf("expected master+exception in one call, got %v", entries)
	}
}

func TestSmartDeleteLiveOccurrenceAddsExdate(t *testing.T) {
	pinNow(t, time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC).Unix()
	master := fabricateRaw(t, "master", "uid1", start, start+1800, "UTC", "FREQ=DAILY", 0, nil, "T", 0)
	// fabricateRaw writes DTSTART 09:00 UTC on 2026-06-15 which matches start.
	rec := newSyncRecorder()
	serveExisting(rec, master)
	client := newTestClient(t, rec)

	occ := start + 2*86400 // 2026-06-17, a live occurrence
	res, err := SmartDelete(context.Background(), client, testAccess(t), "master", occ)
	if err != nil {
		t.Fatalf("SmartDelete: %v", err)
	}
	if res.Kind != "occurrence" || res.RowsDeleted != 1 {
		t.Errorf("result = %+v", res)
	}
	bodies := rec.bodies()
	if len(bodies) != 1 {
		t.Fatalf("expected only the EXDATE update call, got %d", len(bodies))
	}
	frag := bodies[0]["Events"].([]any)[0].(map[string]any)["Event"].(map[string]any)["SharedEventContent"].([]any)[0].(map[string]any)["Data"].(string)
	if !strings.Contains(frag, "EXDATE:20260617T090000Z") {
		t.Errorf("EXDATE for occurrence missing:\n%s", frag)
	}
}

func TestSmartDeleteEditedOccurrenceAlsoDeletesRow(t *testing.T) {
	pinNow(t, time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC).Unix()
	occ := start + 86400
	master := fabricateRaw(t, "master", "uid1", start, start+1800, "UTC", "FREQ=DAILY", 0, nil, "T", 0)
	exception := &caltypes.RawEvent{ID: "exc1", UID: "uid1", StartTime: occ + 3600, EndTime: occ + 5400, RecurrenceID: occ}
	rec := newSyncRecorder()
	serveExisting(rec, master, exception)
	client := newTestClient(t, rec)

	res, err := SmartDelete(context.Background(), client, testAccess(t), "master", occ)
	if err != nil {
		t.Fatalf("SmartDelete: %v", err)
	}
	if res.Kind != "occurrence" || res.RowsDeleted != 2 {
		t.Errorf("result = %+v", res)
	}
	bodies := rec.bodies()
	if len(bodies) != 2 {
		t.Fatalf("expected EXDATE update + row delete, got %d calls", len(bodies))
	}
	delEntries := bodies[1]["Events"].([]any)
	if len(delEntries) != 1 || delEntries[0].(map[string]any)["ID"] != "exc1" {
		t.Errorf("second call should delete exc1: %v", delEntries)
	}
}

// ---------- SmartUpdate ----------

func TestSmartUpdateCreatesExceptionRow(t *testing.T) {
	pinNow(t, time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC).Unix()
	master := fabricateRaw(t, "master", "uid1", start, start+1800, "UTC", "FREQ=DAILY", 0, nil, "Series title", 4)
	rec := newSyncRecorder()
	serveExisting(rec, master)
	client := newTestClient(t, rec)

	occ := start + 86400
	newSummary := "One-off rename"
	outcome, err := SmartUpdate(context.Background(), client, testAccess(t), "master", UpdateOptions{Summary: &newSummary}, occ)
	if err != nil {
		t.Fatalf("SmartUpdate: %v", err)
	}
	if !outcome.EditedOccurrence {
		t.Error("EditedOccurrence not set")
	}
	body := rec.bodies()[0]
	if body["IsImport"] != float64(0) {
		t.Error("exception creation must be a create (IsImport present)")
	}
	evBody := body["Events"].([]any)[0].(map[string]any)["Event"].(map[string]any)
	frag := evBody["SharedEventContent"].([]any)[0].(map[string]any)["Data"].(string)
	if !strings.Contains(frag, "UID:uid1") {
		t.Errorf("exception row must reuse the master UID:\n%s", frag)
	}
	if !strings.Contains(frag, "RECURRENCE-ID:20260616T090000Z") {
		t.Errorf("RECURRENCE-ID missing:\n%s", frag)
	}
	if !strings.Contains(frag, "SEQUENCE:4") {
		t.Errorf("exception must carry the master's sequence (4):\n%s", frag)
	}
	if strings.Contains(frag, "RRULE") {
		t.Errorf("exception row must not carry RRULE:\n%s", frag)
	}
	// Seeded encrypted fields: decrypt and check the overridden summary.
	calKR, _ := testKeys(t)
	plain, err := pgp.DecryptPart(
		evBody["SharedEventContent"].([]any)[1].(map[string]any)["Data"].(string),
		evBody["SharedKeyPacket"].(string), calKR)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plain, "SUMMARY:One-off rename") || !strings.Contains(plain, "LOCATION:fixture location") {
		t.Errorf("exception seeding wrong:\n%s", plain)
	}
}

func TestSmartUpdateRoutesToExistingExceptionRow(t *testing.T) {
	pinNow(t, time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC).Unix()
	occ := start + 86400
	master := fabricateRaw(t, "master", "uid1", start, start+1800, "UTC", "FREQ=DAILY", 0, nil, "Series", 1)
	exception := fabricateRaw(t, "exc1", "uid1", occ, occ+1800, "UTC", "", occ, nil, "Edited", 1)
	rec := newSyncRecorder()
	serveExisting(rec, master, exception)
	client := newTestClient(t, rec)

	loc := "Elsewhere"
	outcome, err := SmartUpdate(context.Background(), client, testAccess(t), "master", UpdateOptions{Location: &loc}, occ)
	if err != nil {
		t.Fatalf("SmartUpdate: %v", err)
	}
	if !outcome.EditedOccurrence {
		t.Error("EditedOccurrence not set")
	}
	entry := rec.bodies()[0]["Events"].([]any)[0].(map[string]any)
	if entry["ID"] != "exc1" {
		t.Errorf("must update the exception row, got ID %v", entry["ID"])
	}
}

func TestSmartUpdateMasterSignificantCleansExceptions(t *testing.T) {
	pinNow(t, time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC).Unix()
	occ := start + 86400
	master := fabricateRaw(t, "master", "uid1", start, start+1800, "UTC", "FREQ=DAILY", 0, nil, "Series", 1)
	exception := &caltypes.RawEvent{ID: "exc1", UID: "uid1", StartTime: occ, EndTime: occ + 1800, RecurrenceID: occ}
	rec := newSyncRecorder()
	serveExisting(rec, master, exception)
	client := newTestClient(t, rec)

	newStart := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	outcome, err := SmartUpdate(context.Background(), client, testAccess(t), "master", UpdateOptions{Start: &newStart}, 0)
	if err != nil {
		t.Fatalf("SmartUpdate: %v", err)
	}
	if outcome.RemovedExceptions != 1 {
		t.Errorf("RemovedExceptions = %d, want 1", outcome.RemovedExceptions)
	}
	bodies := rec.bodies()
	if len(bodies) != 2 {
		t.Fatalf("expected update + cleanup calls, got %d", len(bodies))
	}
	delEntries := bodies[1]["Events"].([]any)
	if len(delEntries) != 1 || delEntries[0].(map[string]any)["ID"] != "exc1" {
		t.Errorf("cleanup should delete exc1: %v", delEntries)
	}
}

func TestSmartUpdateRejectsRecurrenceWithOccurrence(t *testing.T) {
	rec := newSyncRecorder()
	client := newTestClient(t, rec)
	rr := "FREQ=DAILY"
	_, err := SmartUpdate(context.Background(), client, testAccess(t), "x", UpdateOptions{RRule: &rr}, 12345)
	if err == nil || !strings.Contains(err.Error(), "occurrence") {
		t.Errorf("expected occurrence-conflict error, got %v", err)
	}
}

// ---------- sync error mapping ----------

func TestSyncErrorSurfacesCodeAndMessage(t *testing.T) {
	pinNow(t, time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	pinUID(t, "uid-err")
	rec := newSyncRecorder()
	rec.respond = func(map[string]any) any {
		return map[string]any{
			"Code": 1001,
			"Responses": []any{map[string]any{
				"Index": 0,
				"Response": map[string]any{
					"Code":  2001,
					"Error": "Single edits should have a Sequence greater or equal to main event",
				},
			}},
		}
	}
	client := newTestClient(t, rec)

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	_, err := Create(context.Background(), client, testAccess(t), CreateOptions{
		Summary: "x", Start: start, End: start.Add(time.Hour), TZName: "UTC",
	})
	if err == nil || !strings.Contains(err.Error(), "2001") || !strings.Contains(err.Error(), "Sequence greater") {
		t.Errorf("error must carry code and message, got: %v", err)
	}
}

// TestUpdatePreservesWebClientFields guards the update round-trip against
// silently resetting card properties this tool does not edit: a
// web-client-set STATUS/TRANSP, the original CREATED, and the COMMENT.
func TestUpdatePreservesWebClientFields(t *testing.T) {
	pinNow(t, time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC))
	calKR, _ := testKeys(t)
	start := time.Date(2026, 6, 15, 7, 0, 0, 0, time.UTC).Unix()
	raw := fabricateRaw(t, "ev1", "uid1", start, start+1800, "UTC", "", 0, nil, "Busy?", 0, fixtureExtras{
		status:  "TENTATIVE",
		transp:  "TRANSPARENT",
		created: "20250101T080000Z",
		comment: "keep me",
	})

	rec := newSyncRecorder()
	serveExisting(rec, raw)
	client := newTestClient(t, rec)
	access := testAccess(t)

	newSummary := "Still busy?"
	if _, err := update(context.Background(), client, access, "ev1", UpdateOptions{Summary: &newSummary}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	evBody := rec.bodies()[0]["Events"].([]any)[0].(map[string]any)["Event"].(map[string]any)

	calSigned := evBody["CalendarEventContent"].([]any)[0].(map[string]any)["Data"].(string)
	if !strings.Contains(calSigned, "STATUS:TENTATIVE") {
		t.Errorf("update reset STATUS, calendar-signed card:\n%s", calSigned)
	}
	if !strings.Contains(calSigned, "TRANSP:TRANSPARENT") {
		t.Errorf("update reset TRANSP, calendar-signed card:\n%s", calSigned)
	}

	sharedEncData := evBody["SharedEventContent"].([]any)[1].(map[string]any)["Data"].(string)
	sharedPlain, err := pgp.DecryptPart(sharedEncData, raw.SharedKeyPacket, calKR)
	if err != nil {
		t.Fatalf("decrypting updated shared card: %v", err)
	}
	if !strings.Contains(sharedPlain, "CREATED:20250101T080000Z") {
		t.Errorf("update rewrote CREATED, shared-encrypted card:\n%s", sharedPlain)
	}
	if !strings.Contains(sharedPlain, "SUMMARY:Still busy?") {
		t.Errorf("updated summary missing:\n%s", sharedPlain)
	}

	calEncData := evBody["CalendarEventContent"].([]any)[1].(map[string]any)["Data"].(string)
	calPlain, err := pgp.DecryptPart(calEncData, raw.CalendarKeyPacket, calKR)
	if err != nil {
		t.Fatalf("decrypting updated calendar card: %v", err)
	}
	if !strings.Contains(calPlain, "COMMENT:keep me") {
		t.Errorf("update dropped COMMENT, calendar-encrypted card:\n%s", calPlain)
	}
}
