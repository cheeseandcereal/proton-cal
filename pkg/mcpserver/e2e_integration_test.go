//go:build integration

// Live e2e tests for the MCP tool handlers. They drive the unexported
// handler methods directly (the same code the stdio server dispatches to)
// against a real authenticated service, so they live in this package. They
// require the same setup as pkg/integration: a stored session and an
// pkg/integration/config.toml naming a dedicated test calendar.
package mcpserver

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cheeseandcereal/proton-cal/pkg/caljson"
	"github.com/cheeseandcereal/proton-cal/pkg/calsvc"
	"github.com/cheeseandcereal/proton-cal/pkg/config"
)

const e2eSummaryPrefix = "proton-cal-test"

type e2eConfig struct {
	Calendars []string `toml:"calendars"`
}

var (
	e2eOnce sync.Once
	e2eSrv  *server
	e2eCal  string
	e2eSkip string
)

// liveServer returns an MCP server bootstrapped with a real service plus the
// configured test calendar selector, skipping when unconfigured.
func liveServer(t *testing.T) (*server, string) {
	t.Helper()
	e2eOnce.Do(func() {
		data, err := os.ReadFile("../integration/config.toml")
		if errors.Is(err, os.ErrNotExist) {
			e2eSkip = "e2e not configured: see pkg/integration/README.md"
			return
		} else if err != nil {
			t.Fatalf("reading config.toml: %v", err)
		}
		var tc e2eConfig
		if err := toml.Unmarshal(data, &tc); err != nil {
			t.Fatalf("parsing config.toml: %v", err)
		}
		if len(tc.Calendars) == 0 {
			e2eSkip = "pkg/integration/config.toml lists no calendars"
			return
		}
		svc, err := calsvc.New(true)
		if errors.Is(err, config.ErrNoSession) {
			e2eSkip = "no saved session; run `proton-cal login`"
			return
		} else if err != nil {
			t.Fatalf("calsvc.New: %v", err)
		}
		e2eSrv = &server{bootstrap: func() (*calsvc.Service, error) { return svc, nil }}
		e2eCal = tc.Calendars[0]
	})
	if e2eSkip != "" {
		t.Skip(e2eSkip)
	}
	if e2eSrv == nil {
		t.Fatal("e2e server not initialised")
	}
	return e2eSrv, e2eCal
}

// textOf extracts the first text block from a tool result.
func textOf(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

func e2eSummary(label string) string {
	b := make([]byte, 4)
	if _, err := crand.Read(b); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%s %s %s", e2eSummaryPrefix, hex.EncodeToString(b), label)
}

func e2eFutureSlot() (start, end string) {
	d := time.Now().UTC().AddDate(0, 0, 30).Format("2006-01-02")
	return d + " 09:00", d + " 09:30"
}

// TestE2EMCPLifecycle drives create -> get_event (text + structured) ->
// update -> list_events (structured) -> delete through the MCP handlers.
func TestE2EMCPLifecycle(t *testing.T) {
	s, cal := liveServer(t)
	ctx := context.Background()

	start, end := e2eFutureSlot()
	summary := e2eSummary("mcp-lifecycle")
	res, structured, err := s.createEvent(ctx, nil, createEventArgs{
		Summary: summary, Start: start, End: end, Location: "MCP Lab", Calendar: cal, TZ: "UTC",
	})
	if err != nil {
		t.Fatalf("createEvent: %v", err)
	}
	created, ok := structured.(caljson.Created)
	if !ok || created.ID == "" {
		t.Fatalf("create structured content = %#v", structured)
	}
	if !strings.Contains(textOf(res), "Event created") {
		t.Errorf("create text = %q", textOf(res))
	}
	evID := created.ID
	defer func() {
		_, _, _ = s.deleteEvent(ctx, nil, deleteEventArgs{EventID: evID, Calendar: cal})
	}()

	// get_event: text + structured.
	res, structured, err = s.getEvent(ctx, nil, getEventArgs{EventID: evID, Calendar: cal, TZ: "UTC"})
	if err != nil {
		t.Fatalf("getEvent: %v", err)
	}
	detail, ok := structured.(caljson.Event)
	if !ok {
		t.Fatalf("get_event structured content type = %T", structured)
	}
	if detail.Location != "MCP Lab" {
		t.Errorf("structured location = %q, want MCP Lab", detail.Location)
	}
	if !strings.Contains(textOf(res), summary) {
		t.Errorf("get_event text missing summary:\n%s", textOf(res))
	}

	// update_event.
	if _, _, err := s.updateEvent(ctx, nil, updateEventArgs{EventID: evID, Calendar: cal, Summary: e2eSummary("mcp-renamed")}); err != nil {
		t.Fatalf("updateEvent: %v", err)
	}

	// list_events: structured rows include our event.
	_, structured, err = s.listEvents(ctx, nil, listEventsArgs{Calendar: cal, TZ: "UTC", From: time.Now().UTC().AddDate(0, 0, 30).Format("2006-01-02") + " 00:00", Days: 2})
	if err != nil {
		t.Fatalf("listEvents: %v", err)
	}
	rows, ok := structured.([]caljson.Event)
	if !ok {
		t.Fatalf("list_events structured type = %T", structured)
	}
	var found bool
	for _, r := range rows {
		if r.ID == evID {
			found = true
		}
	}
	if !found {
		t.Errorf("event %s not in %d structured list rows", evID, len(rows))
	}

	// delete_event.
	if _, _, err := s.deleteEvent(ctx, nil, deleteEventArgs{EventID: evID, Calendar: cal}); err != nil {
		t.Fatalf("deleteEvent: %v", err)
	}
	if _, _, err := s.getEvent(ctx, nil, getEventArgs{EventID: evID, Calendar: cal}); err == nil {
		t.Error("event still retrievable after delete")
	}
}

// TestE2EMCPClearFields proves the clear_fields round trip live: create with
// a location + description, clear them, and confirm they come back empty.
func TestE2EMCPClearFields(t *testing.T) {
	s, cal := liveServer(t)
	ctx := context.Background()

	start, end := e2eFutureSlot()
	_, structured, err := s.createEvent(ctx, nil, createEventArgs{
		Summary: e2eSummary("mcp-clear"), Start: start, End: end,
		Location: "To Be Cleared", Description: "also cleared", Calendar: cal, TZ: "UTC",
	})
	if err != nil {
		t.Fatalf("createEvent: %v", err)
	}
	evID := structured.(caljson.Created).ID
	defer func() { _, _, _ = s.deleteEvent(ctx, nil, deleteEventArgs{EventID: evID, Calendar: cal}) }()

	if _, _, err := s.updateEvent(ctx, nil, updateEventArgs{
		EventID: evID, Calendar: cal, ClearFields: []string{"location", "description"},
	}); err != nil {
		t.Fatalf("updateEvent clear_fields: %v", err)
	}

	_, structured, err = s.getEvent(ctx, nil, getEventArgs{EventID: evID, Calendar: cal, TZ: "UTC"})
	if err != nil {
		t.Fatalf("getEvent: %v", err)
	}
	detail := structured.(caljson.Event)
	if detail.Location != "" {
		t.Errorf("location not cleared: %q", detail.Location)
	}
	if detail.Description != "" {
		t.Errorf("description not cleared: %q", detail.Description)
	}
}

// TestE2EMCPGetCalendar exercises get_calendar live (text + structured).
func TestE2EMCPGetCalendar(t *testing.T) {
	s, cal := liveServer(t)
	res, structured, err := s.getCalendar(context.Background(), nil, getCalendarArgs{Calendar: cal})
	if err != nil {
		t.Fatalf("getCalendar: %v", err)
	}
	c, ok := structured.(caljson.Calendar)
	if !ok || c.ID == "" {
		t.Fatalf("get_calendar structured = %#v", structured)
	}
	if !strings.Contains(textOf(res), c.Name) {
		t.Errorf("get_calendar text missing name %q:\n%s", c.Name, textOf(res))
	}
}
