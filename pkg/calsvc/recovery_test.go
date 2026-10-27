package calsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/pkg/auth"
	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/config"
	"github.com/cheeseandcereal/proton-cal/pkg/papi"
)

// recoveryService supplies real key unlocking and HTTP event calls over fake
// responses. All account, address and calendar keys use one generated test key.
func recoveryService(t *testing.T) (*Service, *fakeRawAPI) {
	t.Helper()
	key, err := crypto.GenerateKey("test", "test@example.com", "x25519", 0)
	if err != nil {
		t.Fatal(err)
	}
	kr, err := crypto.NewKeyRing(key)
	if err != nil {
		t.Fatal(err)
	}
	pass := []byte("test-passphrase")
	locked, err := key.Lock(pass)
	if err != nil {
		t.Fatal(err)
	}
	armor, err := locked.Armor()
	if err != nil {
		t.Fatal(err)
	}
	msg, err := kr.Encrypt(crypto.NewPlainMessage(pass), nil)
	if err != nil {
		t.Fatal(err)
	}
	encPass, err := msg.GetArmored()
	if err != nil {
		t.Fatal(err)
	}
	marshal := func(v any) string {
		body, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	keys := []map[string]any{{"ID": "key", "PrivateKey": armor, "Active": 1, "Primary": 1, "Flags": 3}}
	fake := &fakeRawAPI{
		bodies: map[string]string{
			auth.UsersPath: marshal(map[string]any{"User": map[string]any{"ID": "user", "Keys": keys}}),
			auth.AddressesPath: marshal(map[string]any{"Addresses": []map[string]any{
				{"ID": "addr", "Email": "test@example.com", "Keys": keys},
			}}),
			calendar.APIPath:          calListBody(t, [2]string{"first", "First"}, [2]string{"chosen", "Chosen"}),
			calendar.UserSettingsPath: userSettingsBody("chosen"),
		},
		errs: make(map[string]error),
	}
	for _, id := range []string{"gone", "first", "chosen"} {
		fake.bodies[calendar.BootstrapPath(id)] = marshal(map[string]any{
			"Keys": keys,
			"Passphrase": map[string]any{"MemberPassphrases": []map[string]any{
				{"MemberID": "m-" + id, "Passphrase": encPass},
			}},
			"CalendarSettings": map[string]any{"DefaultEventDuration": 30},
		})
	}
	s := healService(t, fake)
	s.cfg.Timezone = "UTC"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/events/sync") {
			var body struct{ MemberID string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decoding sync request: %v", err)
			}
			if r.Method != http.MethodPut || body.MemberID != "m-"+strings.Split(r.URL.Path, "/")[3] {
				t.Errorf("unexpected sync request: %s %s, member %s", r.Method, r.URL.Path, body.MemberID)
			}
		} else if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s %s", r.Method, r.URL.Path)
		}
		var raw json.RawMessage
		if err := fake.Get(r.Context(), r.URL.Path, r.URL.Query(), &raw); err != nil {
			var pe *papi.Error
			if !errors.As(err, &pe) {
				pe = &papi.Error{Status: http.StatusInternalServerError, Message: err.Error()}
			}
			w.WriteHeader(pe.Status)
			_ = json.NewEncoder(w).Encode(map[string]any{"Code": pe.Code, "Error": pe.Message})
			return
		}
		_, _ = w.Write(raw)
	}))
	t.Cleanup(server.Close)
	store, err := config.NewSessionStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(config.Session{UID: "u", AccessToken: "a", RefreshToken: "r", SaltedKeyPass: pass}); err != nil {
		t.Fatal(err)
	}
	s.client, err = papi.FromSession(store, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, fake
}

func seedRecoveryList(t *testing.T, s *Service, fake *fakeRawAPI, source string, cals ...[2]string) {
	t.Helper()
	body := calListBody(t, cals...)
	if source == "disk" {
		s.cacheAPI.cache.Put(calendar.APIPath, json.RawMessage(body))
		return
	}
	if source == "no-cache" {
		s.api, s.freshAPI, s.cacheAPI = fake, fake, nil
	}
	var err error
	s.cals, err = calendar.List(context.Background(), &fakeRawAPI{bodies: map[string]string{calendar.APIPath: body}})
	if err != nil {
		t.Fatal(err)
	}
}

func missingResource() error {
	return &papi.Error{Status: http.StatusUnprocessableEntity, Code: papi.CodeNotFound, Message: "This resource was not found"}
}

func TestWithAccessRecoversCalendar(t *testing.T) {
	for _, source := range []string{"memory", "disk", "no-cache"} {
		for _, stage := range []string{"bootstrap", "operation"} {
			t.Run(source+"/"+stage, func(t *testing.T) {
				s, fake := recoveryService(t)
				seedRecoveryList(t, s, fake, source, [2]string{"gone", "Chosen"})
				if stage == "bootstrap" {
					fake.errs[calendar.BootstrapPath("gone")] = missingResource()
				}
				var ids []string
				err := s.withAccess(context.Background(), "Chosen", func(info calendar.Info, access *calendar.Access) error {
					ids = append(ids, info.ID)
					if access.CalendarID != info.ID {
						t.Fatalf("access = %s, resolved = %s", access.CalendarID, info.ID)
					}
					if info.ID == "gone" {
						return fmt.Errorf("operation: %w", missingResource())
					}
					return nil
				})
				wantIDs := []string{"chosen"}
				if stage == "operation" {
					wantIDs = []string{"gone", "chosen"}
				}
				if err != nil || !slices.Equal(ids, wantIDs) {
					t.Fatalf("operations = %v, %v; want %v", ids, err, wantIDs)
				}
				if gets := fake.getCounts(); gets[calendar.APIPath] != 1 {
					t.Errorf("list GETs = %d, want 1", gets[calendar.APIPath])
				}
				if s.cacheAPI != nil {
					s.cals = nil
					info, err := s.resolveCalendar(context.Background(), "Chosen")
					if gets := fake.getCounts(); err != nil || info.ID != "chosen" || gets[calendar.APIPath] != 1 {
						t.Fatalf("disk-cached resolution = %q, %v; GETs = %v", info.ID, err, gets)
					}
				}
			})
		}
	}
}

func TestWithAccessRetryBounds(t *testing.T) {
	for _, tc := range []struct {
		name       string
		selector   string
		failure    error
		retryErr   error
		serverID   string
		refreshErr error
		wantCalls  int
		wantReads  int
		wantError  string
	}{
		{name: "same calendar", selector: "Chosen", failure: missingResource(), serverID: "gone", wantCalls: 1, wantReads: 1, wantError: "resource was not found"},
		{name: "deleted name", selector: "Deleted", failure: missingResource(), serverID: "chosen", wantCalls: 1, wantReads: 1, wantError: `no calendar with ID or name "Deleted"`},
		{name: "repeated missing resource", selector: "Chosen", failure: missingResource(), serverID: "chosen", wantCalls: 2, wantReads: 1, wantError: "resource was not found"},
		{name: "second failure", selector: "Chosen", failure: fmt.Errorf("event updated, but cleanup failed: %w", missingResource()), retryErr: errors.New("replacement event unavailable"), serverID: "chosen", wantCalls: 2, wantReads: 1, wantError: "replacement event unavailable"},
		{name: "other API error", selector: "Chosen", failure: &papi.Error{Status: 422, Code: 2011, Message: "invalid input"}, wantCalls: 1, wantError: "invalid input"},
		{name: "network error", selector: "Chosen", failure: errors.New("offline"), wantCalls: 1, wantError: "offline"},
		{name: "refresh failure", selector: "Chosen", failure: fmt.Errorf("event updated, but cleanup failed: %w", missingResource()), refreshErr: errors.New("list unavailable"), wantCalls: 1, wantReads: 1, wantError: "list unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, fake := recoveryService(t)
			seedRecoveryList(t, s, fake, "memory", [2]string{"gone", tc.selector})
			fake.bodies[calendar.APIPath] = calListBody(t, [2]string{tc.serverID, "Chosen"})
			fake.errs[calendar.APIPath] = tc.refreshErr
			calls := 0
			err := s.withAccess(context.Background(), tc.selector, func(calendar.Info, *calendar.Access) error {
				calls++
				if calls > 1 && tc.retryErr != nil {
					return tc.retryErr
				}
				return tc.failure
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			if (tc.refreshErr != nil || tc.retryErr != nil) && !errors.Is(err, tc.failure) {
				t.Errorf("recovery lost the original operation error: %v", err)
			}
			if gets := fake.getCounts(); calls != tc.wantCalls || gets[calendar.APIPath] != tc.wantReads {
				t.Errorf("operations = %d, list GETs = %d; want %d, %d", calls, gets[calendar.APIPath], tc.wantCalls, tc.wantReads)
			}
		})
	}
}

func TestGetCalendarRecoversDefaultWithoutHealingKeys(t *testing.T) {
	s, fake := recoveryService(t)
	seedRecoveryList(t, s, fake, "memory", [2]string{"gone", "Deleted"}, [2]string{"first", "First"}, [2]string{"chosen", "Chosen"})
	s.cacheAPI.cache.Put(calendar.UserSettingsPath, json.RawMessage(userSettingsBody("gone")))
	for _, path := range accountKeyCacheKeys() {
		s.cacheAPI.cache.Put(path, json.RawMessage(fake.bodies[path]))
	}
	fake.errs[calendar.BootstrapPath("gone")] = missingResource()
	var notices []string
	s.Notify = func(msg string) { notices = append(notices, msg) }
	got, err := s.GetCalendar(context.Background(), "")
	if err != nil || got.Info.ID != "chosen" || !got.IsDefault {
		t.Fatalf("GetCalendar = %+v, %v; want chosen marked default", got, err)
	}
	if !slices.Equal(notices, []string{"Using calendar: Chosen"}) {
		t.Errorf("notices = %v", notices)
	}
	gets := fake.getCounts()
	for _, path := range accountKeyCacheKeys() {
		if gets[path] != 0 {
			t.Errorf("account keys refetched on a missing calendar: %s", path)
		}
	}
	if gets[calendar.APIPath] != 1 || gets[calendar.UserSettingsPath] != 1 {
		t.Errorf("metadata GETs = %v, want list 1 and default 1", gets)
	}
}

func TestGetEventDegradedKeys(t *testing.T) {
	for _, failRefresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("refresh fails=%t", failRefresh), func(t *testing.T) {
			s, fake := recoveryService(t)
			seedRecoveryList(t, s, fake, "memory", [2]string{"chosen", "Chosen"})
			for _, path := range accountKeyCacheKeys() {
				s.cacheAPI.cache.Put(path, json.RawMessage(fake.bodies[path]))
			}
			if failRefresh {
				fake.errs[auth.UsersPath] = errors.New("offline")
			}
			eventPath := calendar.APIPath + "/chosen/events/event"
			fake.bodies[eventPath] = `{"Event":{"ID":"event","SharedEvents":[{"Type":0,"Data":"BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nSUMMARY:Readable\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"},{"Type":2,"Data":"invalid ciphertext"}]}}`
			var notices []string
			s.Notify = func(msg string) { notices = append(notices, msg) }
			got, err := s.GetEvent(context.Background(), GetEventInput{EventID: "event"})
			if err != nil || got == nil || got.Event.Summary != "Readable" || !got.Event.DecryptFailed {
				t.Fatalf("result = %v, %v; want best-effort content", got, err)
			}
			if !slices.Equal(notices, []string{"Using calendar: Chosen"}) {
				t.Errorf("notices = %v", notices)
			}
			wantCalls := 2
			if failRefresh {
				wantCalls = 1
			}
			if gets := fake.getCounts(); gets[eventPath] != wantCalls || gets[calendar.APIPath] != 0 || gets[auth.UsersPath] != 1 {
				t.Errorf("GETs = %v, want event %d, list 0 and users 1", gets, wantCalls)
			}
		})
	}
}

func TestListEventsRecoversDeletedCalendar(t *testing.T) {
	for _, source := range []string{"memory", "disk", "no-cache"} {
		for _, stage := range []string{"bootstrap", "events"} {
			t.Run(source+"/"+stage, func(t *testing.T) {
				s, fake := recoveryService(t)
				seedRecoveryList(t, s, fake, source, [2]string{"gone", "Deleted"}, [2]string{"chosen", "Chosen"})
				fake.bodies[calendar.APIPath] = calListBody(t, [2]string{"chosen", "Chosen"})
				path := calendar.BootstrapPath("gone")
				if stage == "events" {
					path = calendar.APIPath + "/gone/events"
				}
				fake.errs[path] = missingResource()
				got, err := s.ListEvents(context.Background(), ListEventsInput{AllCalendars: true, From: "2026-06-01", Days: 1})
				if err != nil {
					t.Fatal(err)
				}
				if len(got.Calendars) != 1 || got.Calendars[0].ID != "chosen" {
					t.Errorf("listed calendars = %+v, want chosen", got.Calendars)
				}
				if gets := fake.getCounts(); gets[calendar.APIPath] != 1 || gets[calendar.UserSettingsPath] != 0 {
					t.Errorf("metadata GETs = %v, want list 1 and default 0", gets)
				}
			})
		}
	}
}

func TestListEventsDefaultRecovery(t *testing.T) {
	s, fake := recoveryService(t)
	seedRecoveryList(t, s, fake, "memory", [2]string{"gone", "Deleted"}, [2]string{"first", "First"}, [2]string{"chosen", "Chosen"})
	s.cacheAPI.cache.Put(calendar.UserSettingsPath, json.RawMessage(userSettingsBody("gone")))
	fake.errs[calendar.APIPath+"/gone/events"] = missingResource()
	var notices []string
	s.Notify = func(msg string) { notices = append(notices, msg) }
	got, err := s.ListEvents(context.Background(), ListEventsInput{From: "2026-06-01", Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Calendars) != 1 || got.Calendars[0].ID != "chosen" {
		t.Errorf("listed calendars = %+v, want chosen", got.Calendars)
	}
	if !slices.Equal(notices, []string{"Using calendar: Chosen"}) {
		t.Errorf("notices = %v", notices)
	}
	if gets := fake.getCounts(); gets[calendar.APIPath] != 1 || gets[calendar.UserSettingsPath] != 1 {
		t.Errorf("metadata GETs = %v, want list 1 and default 1", gets)
	}
}

func TestListEventsRetryBounds(t *testing.T) {
	for _, tc := range []struct {
		name      string
		selectors []string
		failure   error
		wantReads int
		wantError string
	}{
		{name: "deleted name", selectors: []string{"Deleted"}, failure: missingResource(), wantReads: 1, wantError: `no calendar with ID or name "Deleted"`},
		{name: "second failure", failure: missingResource(), wantReads: 1, wantError: "resource was not found"},
		{name: "other error", failure: &papi.Error{Status: 422, Code: 2011, Message: "invalid input"}, wantError: "invalid input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, fake := recoveryService(t)
			seedRecoveryList(t, s, fake, "memory", [2]string{"gone", "Deleted"})
			fake.bodies[calendar.APIPath] = calListBody(t, [2]string{"chosen", "Chosen"})
			fake.errs[calendar.BootstrapPath("gone")] = tc.failure
			fake.errs[calendar.BootstrapPath("chosen")] = tc.failure
			_, err := s.ListEvents(context.Background(), ListEventsInput{
				Calendars: tc.selectors, AllCalendars: len(tc.selectors) == 0, From: "2026-06-01", Days: 1,
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			if gets := fake.getCounts(); gets[calendar.APIPath] != tc.wantReads {
				t.Errorf("list GETs = %d, want %d", gets[calendar.APIPath], tc.wantReads)
			}
		})
	}
}

func TestCreateEventRecoversFromSyncError(t *testing.T) {
	for _, tc := range []struct{ name, reply string }{
		{"top level", `{"Code":2501}`},
		{"per event", `{"Code":1001,"Responses":[{"Response":{"Code":2501,"Error":"This resource was not found"}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, fake := recoveryService(t)
			seedRecoveryList(t, s, fake, "memory", [2]string{"gone", "Chosen"})
			gonePath := calendar.APIPath + "/gone/events/sync"
			chosenPath := calendar.APIPath + "/chosen/events/sync"
			fake.bodies[gonePath] = tc.reply
			fake.bodies[chosenPath] = `{"Code":1001,"Responses":[{"Response":{"Code":1000,"Event":{"ID":"created"}}}]}`
			got, err := s.CreateEvent(context.Background(), CreateEventInput{Calendar: "Chosen", Summary: "Test", Start: "2026-06-01 12:00"})
			if err != nil || got.ID != "created" {
				t.Fatalf("CreateEvent = %+v, %v", got, err)
			}
			if gets := fake.getCounts(); gets[gonePath] != 1 || gets[chosenPath] != 1 || gets[calendar.APIPath] != 1 {
				t.Errorf("API calls = %v, want one sync per calendar and one list refresh", gets)
			}
		})
	}
}

func TestCreateEventWithPersistedDeletedDefault(t *testing.T) {
	s, fake := recoveryService(t)
	s.cacheAPI.cache.Put(calendar.APIPath, json.RawMessage(fake.bodies[calendar.APIPath]))
	s.cacheAPI.cache.Put(calendar.UserSettingsPath, json.RawMessage(userSettingsBody("gone")))
	cache, err := config.OpenCache("test-scope")
	if err != nil {
		t.Fatal(err)
	}
	s.cacheAPI = newCachedAPI(fake, cache, false)
	s.api, s.freshAPI = s.cacheAPI, newCachedAPI(fake, cache, true)
	chosenPath := calendar.APIPath + "/chosen/events/sync"
	fake.bodies[chosenPath] = `{"Code":1001,"Responses":[{"Response":{"Code":1000,"Event":{"ID":"created"}}}]}`
	got, err := s.CreateEvent(context.Background(), CreateEventInput{Summary: "Test", Start: "2026-06-01 12:00"})
	if err != nil || got.ID != "created" {
		t.Fatalf("CreateEvent = %+v, %v", got, err)
	}
	gets := fake.getCounts()
	if gets[chosenPath] != 1 || gets[calendar.APIPath+"/first/events/sync"] != 0 || gets[calendar.APIPath+"/gone/events/sync"] != 0 {
		t.Errorf("creation used the wrong calendar: %v", gets)
	}
}

func TestListEventsNamedReplacement(t *testing.T) {
	s, fake := recoveryService(t)
	seedRecoveryList(t, s, fake, "memory", [2]string{"gone", "Chosen"})
	fake.errs[calendar.APIPath+"/gone/events"] = missingResource()
	start := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC).Unix()
	raw, err := json.Marshal(caltypes.RawEvent{ID: "event", CalendarID: "chosen", StartTime: start, EndTime: start + 3600})
	if err != nil {
		t.Fatal(err)
	}
	fake.bodies[calendar.APIPath+"/chosen/events"] = fmt.Sprintf(`{"Events":[%s]}`, raw)
	got, err := s.ListEvents(context.Background(), ListEventsInput{Calendars: []string{"Chosen"}, From: "2026-06-01", Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].Event.EventID != "event" || got.Items[0].Calendar.ID != "chosen" {
		t.Fatalf("items = %+v, want event in chosen", got.Items)
	}
}

func TestListEventsCanceledSiblingKeepsKeys(t *testing.T) {
	s, fake := recoveryService(t)
	seedRecoveryList(t, s, fake, "memory", [2]string{"gone", "Deleted"}, [2]string{"chosen", "Chosen"})
	fake.bodies[calendar.APIPath] = calListBody(t, [2]string{"chosen", "Chosen"})
	for _, path := range accountKeyCacheKeys() {
		s.cacheAPI.cache.Put(path, json.RawMessage(fake.bodies[path]))
		fake.errs[path] = errors.New("account keys unavailable")
	}
	fake.errs[calendar.BootstrapPath("gone")] = missingResource()
	started := make(chan struct{})
	var blocked atomic.Bool
	fake.beforeGet = func(ctx context.Context, path string) error {
		if path == calendar.BootstrapPath("chosen") && blocked.CompareAndSwap(false, true) {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}
		if path == calendar.BootstrapPath("gone") {
			select {
			case <-started:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := s.ListEvents(ctx, ListEventsInput{AllCalendars: true, From: "2026-06-01", Days: 1})
	if err != nil || len(got.Calendars) != 1 || got.Calendars[0].ID != "chosen" {
		t.Fatalf("ListEvents = %+v, %v", got, err)
	}
	gets := fake.getCounts()
	for _, path := range accountKeyCacheKeys() {
		if gets[path] != 0 {
			t.Errorf("cancellation refetched account keys: %s", path)
		}
	}
}

func TestListEventsConcurrentCalendarRefresh(t *testing.T) {
	s, fake := recoveryService(t)
	cals := [][2]string{{"first", "First"}, {"chosen", "Chosen"}, {"third", "Third"}, {"fourth", "Fourth"}}
	fake.bodies[calendar.APIPath] = calListBody(t, cals...)
	seedRecoveryList(t, s, fake, "memory", append(cals, [2]string{"gone", "Deleted"})...)
	for _, id := range []string{"third", "fourth"} {
		fake.bodies[calendar.BootstrapPath(id)] = fake.bodies[calendar.BootstrapPath("chosen")]
	}
	fake.errs[calendar.BootstrapPath("gone")] = missingResource()
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	fake.beforeGet = func(ctx context.Context, path string) error {
		if strings.HasSuffix(path, "/bootstrap") && path != calendar.BootstrapPath("gone") {
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type result struct {
		list *EventList
		err  error
	}
	done := make(chan result, 1)
	go func() {
		list, err := s.ListEvents(ctx, ListEventsInput{AllCalendars: true, From: "2026-06-01", Days: 1})
		done <- result{list, err}
	}()
	for range 4 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("listing workers did not start")
		}
	}
	if _, err := s.Calendars(ctx); err != nil {
		t.Fatal(err)
	}
	close(release)
	out := <-done
	if out.err != nil || len(out.list.Calendars) != 4 {
		t.Fatalf("ListEvents = %+v, %v; want four surviving calendars", out.list, out.err)
	}
}
