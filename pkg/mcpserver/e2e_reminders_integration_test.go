//go:build integration

package mcpserver

import (
	"context"
	"testing"

	"github.com/cheeseandcereal/proton-cal/pkg/caljson"
)

// TestE2EMCPRemindersAndColor creates an event with reminders+color via the MCP
// handler, then exercises reminders_mode=none and color-inherit via clear_fields.
func TestE2EMCPRemindersAndColor(t *testing.T) {
	s, cal := liveServer(t)
	ctx := context.Background()

	start, end := e2eFutureSlot()
	_, created, err := s.createEvent(ctx, nil, createEventArgs{
		Summary: e2eSummary("mcp-reminders"), Start: start, End: end, Calendar: cal, TZ: "UTC",
		Reminders: []string{"15m", "email:1h"}, Color: "#EC3E7C",
	})
	if err != nil {
		t.Fatalf("createEvent: %v", err)
	}
	evID := created.ID
	defer func() { _, _, _ = s.deleteEvent(ctx, nil, deleteEventArgs{EventID: evID, Calendar: cal}) }()

	if created.Color != "#EC3E7C" || len(created.Notifications) != 2 {
		t.Errorf("create structured reminders/color = %+v / %q", created.Notifications, created.Color)
	}
	if created.Notifications[0].Trigger != "-PT15M" || created.Notifications[1].Type != "email" {
		t.Errorf("create notifications = %+v", created.Notifications)
	}

	// get_event structured content reflects the stored custom reminders+color.
	_, structured, err := s.getEvent(ctx, nil, getEventArgs{EventID: evID, Calendar: cal, TZ: "UTC"})
	if err != nil {
		t.Fatalf("getEvent: %v", err)
	}
	detail := structured.(caljson.Event)
	if detail.Color != "#EC3E7C" || len(detail.Notifications) != 2 {
		t.Errorf("get_event reminders/color = %+v / %q", detail.Notifications, detail.Color)
	}

	// The calendar's own color (clearing color reverts to it).
	_, calStruct, err := s.getCalendar(ctx, nil, getCalendarArgs{Calendar: cal})
	if err != nil {
		t.Fatalf("getCalendar: %v", err)
	}
	calColor := calStruct.Color

	// reminders_mode=none removes them; color="default" reverts to the
	// calendar color (Proton has no color "clear", so it becomes calColor).
	if _, _, err := s.updateEvent(ctx, nil, updateEventArgs{
		EventID: evID, Calendar: cal, RemindersMode: "none", Color: "default",
	}); err != nil {
		t.Fatalf("updateEvent none+color default: %v", err)
	}
	_, structured, err = s.getEvent(ctx, nil, getEventArgs{EventID: evID, Calendar: cal, TZ: "UTC"})
	if err != nil {
		t.Fatalf("getEvent after update: %v", err)
	}
	detail = structured.(caljson.Event)
	if len(detail.Notifications) != 0 {
		t.Errorf("reminders not removed: %+v", detail.Notifications)
	}
	// Note: caljson uses EffectiveColor, so a color equal to the calendar's is
	// reported the same whether stored or inherited; either way it must match.
	if detail.Color != calColor {
		t.Errorf("color after revert = %q, want calendar color %q", detail.Color, calColor)
	}

	// clear_fields:["color"] is the backward-compatible equivalent of
	// color="default": set a palette color, then clear it.
	if _, _, err := s.updateEvent(ctx, nil, updateEventArgs{EventID: evID, Calendar: cal, Color: "pacific"}); err != nil {
		t.Fatalf("updateEvent set pacific: %v", err)
	}
	if _, _, err := s.updateEvent(ctx, nil, updateEventArgs{EventID: evID, Calendar: cal, ClearFields: []string{"color"}}); err != nil {
		t.Fatalf("updateEvent clear_fields color: %v", err)
	}
	_, structured, err = s.getEvent(ctx, nil, getEventArgs{EventID: evID, Calendar: cal, TZ: "UTC"})
	if err != nil {
		t.Fatalf("getEvent after clear_fields: %v", err)
	}
	if detail = structured.(caljson.Event); detail.Color != calColor {
		t.Errorf("color after clear_fields = %q, want calendar color %q", detail.Color, calColor)
	}
}
