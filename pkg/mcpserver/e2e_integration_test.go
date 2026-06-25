//go:build integration

// Live e2e tests for the MCP tool handlers, driving the unexported handler
// methods directly (the same code the stdio server dispatches to) against a
// real authenticated service, so they live in this package. Same setup as
// pkg/integration: a stored session and a pkg/integration/config.toml naming a
// dedicated test calendar.
package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cheeseandcereal/proton-cal/pkg/caljson"
	"github.com/cheeseandcereal/proton-cal/pkg/calsvc"
	"github.com/cheeseandcereal/proton-cal/pkg/internal/e2eharness"
)

// liveServer returns an MCP server bootstrapped with the shared live service
// plus the configured test calendar selector, skipping when unconfigured.
func liveServer(t *testing.T) (*server, string) {
	svc, cal := e2eharness.LiveService(t)
	return &server{bootstrap: func() (*calsvc.Service, error) { return svc, nil }}, cal
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

func e2eSummary(label string) string     { return e2eharness.Summary(label) }
func e2eFutureSlot() (start, end string) { return e2eharness.FutureSlot() }

// TestE2EMCPLifecycle drives create -> get_event (text + structured) ->
// update -> list_events (structured) -> delete through the MCP handlers.
func TestE2EMCPLifecycle(t *testing.T) {
	s, cal := liveServer(t)
	ctx := context.Background()

	start, end := e2eFutureSlot()
	summary := e2eSummary("mcp-lifecycle")
	res, created, err := s.createEvent(ctx, nil, createEventArgs{
		Summary: summary, Start: start, End: end, Location: "MCP Lab", Calendar: cal, TZ: "UTC",
	})
	if err != nil {
		t.Fatalf("createEvent: %v", err)
	}
	if created.ID == "" {
		t.Fatalf("create structured content = %#v", created)
	}
	if !strings.Contains(textOf(res), "Event created") {
		t.Errorf("create text = %q", textOf(res))
	}
	evID := created.ID
	defer func() {
		_, _, _ = s.deleteEvent(ctx, nil, deleteEventArgs{EventID: evID, Calendar: cal})
	}()

	// get_event: text + structured. get_event keeps Out=any (its structured
	// output is the detail object, or absent for the ICS format).
	res, structured, err := s.getEvent(ctx, nil, getEventArgs{EventID: evID, Calendar: cal, TZ: "UTC"})
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

	// list_events: structured rows (wrapped in an object) include our event.
	_, listed, err := s.listEvents(ctx, nil, listEventsArgs{Calendars: []string{cal}, TZ: "UTC", From: time.Now().UTC().AddDate(0, 0, 30).Format("2006-01-02") + " 00:00", Days: 2})
	if err != nil {
		t.Fatalf("listEvents: %v", err)
	}
	rows := listed.Events
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
	_, created, err := s.createEvent(ctx, nil, createEventArgs{
		Summary: e2eSummary("mcp-clear"), Start: start, End: end,
		Location: "To Be Cleared", Description: "also cleared", Calendar: cal, TZ: "UTC",
	})
	if err != nil {
		t.Fatalf("createEvent: %v", err)
	}
	evID := created.ID
	defer func() { _, _, _ = s.deleteEvent(ctx, nil, deleteEventArgs{EventID: evID, Calendar: cal}) }()

	if _, _, err := s.updateEvent(ctx, nil, updateEventArgs{
		EventID: evID, Calendar: cal, ClearFields: []clearField{"location", "description"},
	}); err != nil {
		t.Fatalf("updateEvent clear_fields: %v", err)
	}

	_, structured, err := s.getEvent(ctx, nil, getEventArgs{EventID: evID, Calendar: cal, TZ: "UTC"})
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
	res, c, err := s.getCalendar(context.Background(), nil, getCalendarArgs{Calendar: cal})
	if err != nil {
		t.Fatalf("getCalendar: %v", err)
	}
	if c.ID == "" {
		t.Fatalf("get_calendar structured = %#v", c)
	}
	if !strings.Contains(textOf(res), c.Name) {
		t.Errorf("get_calendar text missing name %q:\n%s", c.Name, textOf(res))
	}
}
