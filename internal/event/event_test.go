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
			http.Error(w, err.Error(), 400)
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

// ---------- Query ----------

func TestQueryPaginationAndFilter(t *testing.T) {
	page0 := make([]*caltypes.RawEvent, pageSize)
	for i := range page0 {
		page0[i] = &caltypes.RawEvent{ID: "fill" + itoa(i), StartTime: 50, EndTime: 60}
	}
	page1 := []*caltypes.RawEvent{
		{ID: "in-window", StartTime: 150, EndTime: 250},
		{ID: "outside", StartTime: 5000, EndTime: 6000},
		{ID: "master", StartTime: 5, EndTime: 10, RRule: "FREQ=DAILY"},
	}
	total := len(page0) + len(page1)

	mux := http.NewServeMux()
	mux.HandleFunc("/calendar/v1/cal1/events", func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("Page")
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "0":
			_ = json.NewEncoder(w).Encode(eventsResponse{Events: page0, Total: total})
		case "1":
			_ = json.NewEncoder(w).Encode(eventsResponse{Events: page1, Total: total})
		default:
			t.Errorf("unexpected page %s", page)
			_ = json.NewEncoder(w).Encode(eventsResponse{Total: total})
		}
	})
	client := newTestClient(t, mux)

	got, err := query(context.Background(), client, "cal1", 100, 1000, "UTC")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	var ids []string
	for _, ev := range got {
		ids = append(ids, ev.ID)
	}
	// Window [100,1000): fillers (50-60) dropped, outside dropped, in-window
	// kept, master always kept. Sorted by StartTime: master(5) first.
	if len(ids) != 2 || ids[0] != "master" || ids[1] != "in-window" {
		t.Errorf("ids = %v", ids)
	}
}

// ---------- Create ----------

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
