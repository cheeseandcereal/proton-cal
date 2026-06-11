package calendar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cheeseandcereal/proton-cal-go/internal/config"
	"github.com/cheeseandcereal/proton-cal-go/internal/papi"
)

// newTestClient builds a papi.Client backed by an httptest server running
// handler, with a temp config dir holding a valid session.
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

// countingMux is an http.Handler that counts hits per path.
type countingMux struct {
	mux *http.ServeMux

	mu   sync.Mutex
	hits map[string]int
}

func newCountingMux() *countingMux {
	return &countingMux{mux: http.NewServeMux(), hits: make(map[string]int)}
}

func (c *countingMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.hits[r.URL.Path]++
	c.mu.Unlock()
	c.mux.ServeHTTP(w, r)
}

func (c *countingMux) hitCount(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits[path]
}

func (c *countingMux) handleJSON(path string, body any) {
	c.mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			panic(err)
		}
	})
}

func TestList(t *testing.T) {
	mux := newCountingMux()
	mux.handleJSON("/calendar/v1", map[string]any{
		"Calendars": []map[string]any{
			{
				// Modern (live-verified) shape: metadata only on members.
				"ID":         "cal-modern",
				"Type":       0,
				"CreateTime": 1700000000,
				"Members": []map[string]any{{
					"ID":          "m1",
					"AddressID":   "addr1",
					"CalendarID":  "cal-modern",
					"Email":       "me@example.com",
					"Name":        "Personal",
					"Description": "My stuff",
					"Color":       "#415DF0",
					"Display":     1,
					"Permissions": 112,
					"Flags":       1,
					"Priority":    1,
				}},
			},
			{
				// Legacy shape: metadata top-level, no members.
				"ID":          "cal-legacy",
				"Type":        0,
				"Name":        "Legacy",
				"Description": "Old shape",
				"Color":       "#FF0000",
			},
			{
				// Subscribed calendar.
				"ID":   "cal-sub",
				"Type": 1,
				"Members": []map[string]any{{
					"ID":        "m2",
					"AddressID": "addr1",
					"Email":     "me@example.com",
					"Name":      "Feed",
					"Color":     "#00FF00",
				}},
			},
			{
				// Entirely sparse entry: defaults apply.
				"ID": "cal-sparse",
			},
		},
	})
	client := newTestClient(t, mux)

	cals, err := List(context.Background(), client)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(cals) != 4 {
		t.Fatalf("got %d calendars, want 4", len(cals))
	}

	want := []Info{
		{
			ID: "cal-modern", Name: "Personal", Description: "My stuff", Color: "#415DF0",
			Type: 0, MemberID: "m1", AddressID: "addr1", Email: "me@example.com",
		},
		{
			ID: "cal-legacy", Name: "Legacy", Description: "Old shape", Color: "#FF0000",
			Type: 0,
		},
		{
			ID: "cal-sub", Name: "Feed", Description: "", Color: "#00FF00",
			Type: 1, MemberID: "m2", AddressID: "addr1", Email: "me@example.com",
		},
		{
			ID: "cal-sparse", Name: "Unnamed", Description: "", Color: "#000000",
			Type: 0,
		},
	}
	for i, w := range want {
		if cals[i] != w {
			t.Errorf("calendar %d:\n got  %+v\n want %+v", i, cals[i], w)
		}
	}
}

func TestListEmpty(t *testing.T) {
	mux := newCountingMux()
	mux.handleJSON("/calendar/v1", map[string]any{})
	client := newTestClient(t, mux)

	cals, err := List(context.Background(), client)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(cals) != 0 {
		t.Fatalf("got %d calendars, want 0", len(cals))
	}
}

func TestResolve(t *testing.T) {
	cals := []Info{
		{ID: "id1", Name: "Work"},
		{ID: "id2", Name: "personal"},
		{ID: "id3", Name: "Personal"},
	}

	tests := []struct {
		name            string
		cals            []Info
		selector        string
		defaultSelector string
		wantID          string
		wantErr         string
	}{
		{name: "by ID", cals: cals, selector: "id3", wantID: "id3"},
		{name: "by unique name case-insensitive", cals: cals, selector: "WORK", wantID: "id1"},
		{name: "ambiguous name", cals: cals, selector: "personal", wantErr: "ambiguous"},
		{name: "no match", cals: cals, selector: "nope", wantErr: "no calendar with ID or name"},
		{name: "empty selector uses default", cals: cals, defaultSelector: "work", wantID: "id1"},
		{name: "selector beats default", cals: cals, selector: "id2", defaultSelector: "work", wantID: "id2"},
		{name: "empty both picks first", cals: cals, wantID: "id1"},
		{name: "no calendars", cals: nil, wantErr: "no calendars"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.cals, tt.selector, tt.defaultSelector)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Resolve: got %+v, want error containing %q", got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Resolve error %q does not contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.ID != tt.wantID {
				t.Fatalf("Resolve picked %q, want %q", got.ID, tt.wantID)
			}
		})
	}
}

func TestResolveAmbiguousErrorListsCandidates(t *testing.T) {
	cals := []Info{
		{ID: "id2", Name: "personal"},
		{ID: "id3", Name: "Personal"},
	}
	_, err := Resolve(cals, "PERSONAL", "")
	if err == nil {
		t.Fatal("Resolve: expected error")
	}
	for _, want := range []string{"id2", "id3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ambiguous error %q does not list candidate %q", err, want)
		}
	}
}
