//go:build integration

package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
)

// TestE2ECLIRemindersAndColor creates an event with --reminder/--color via
// the CLI, reads it back as JSON, then clears reminders and color via update.
func TestE2ECLIRemindersAndColor(t *testing.T) {
	factory, cal := liveFactory(t)
	start, end := e2eFutureSlot()
	summary := e2eSummary("cli-reminders")

	stdout, _, err := runCLI(t, factory, "create", summary,
		"--start", start, "--end", end, "--calendar", cal, "--tz", "UTC",
		"--reminder", "15m", "--reminder", "email:1h", "--color", "#EC3E7C",
		"-o", "json")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var created struct {
		ID            string `json:"id"`
		Color         string `json:"color"`
		Notifications []struct {
			Type    int    `json:"type"`
			Trigger string `json:"trigger"`
		} `json:"notifications"`
	}
	if err := json.Unmarshal([]byte(stdout), &created); err != nil {
		t.Fatalf("parse create JSON: %v\n%s", err, stdout)
	}
	if created.ID == "" {
		t.Fatalf("no id:\n%s", stdout)
	}
	t.Cleanup(func() {
		_, _ = e2eSvc.DeleteEvent(context.Background(), calsvc.DeleteEventInput{EventID: created.ID, Calendar: cal})
	})
	if created.Color != "#EC3E7C" || len(created.Notifications) != 2 {
		t.Errorf("create JSON reminders/color = %+v / %q", created.Notifications, created.Color)
	}

	// Read back via get event -o json.
	stdout, _, err = runCLI(t, factory, "get", "event", created.ID, "--calendar", cal, "--tz", "UTC", "-o", "json")
	if err != nil {
		t.Fatalf("get event: %v", err)
	}
	var got struct {
		Color         string `json:"color"`
		Notifications []any  `json:"notifications"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse get JSON: %v\n%s", err, stdout)
	}
	if got.Color != "#EC3E7C" || len(got.Notifications) != 2 {
		t.Errorf("get JSON reminders/color = %+v / %q", got.Notifications, got.Color)
	}

	// Clear reminders (--no-reminders) and revert color (--no-color, which
	// sets the event to the calendar's own color since Proton has no clear).
	calInfo, err := e2eSvc.GetCalendar(context.Background(), cal)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if _, _, err := runCLI(t, factory, "update", created.ID, "--calendar", cal, "--no-reminders", "--no-color"); err != nil {
		t.Fatalf("update clear: %v", err)
	}
	ev, err := e2eSvc.GetEvent(context.Background(), calsvc.GetEventInput{EventID: created.ID, Calendar: cal})
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if !ev.Event.NotificationsSet || len(ev.Event.Notifications) != 0 {
		t.Errorf("reminders not explicitly cleared: set=%v %+v", ev.Event.NotificationsSet, ev.Event.Notifications)
	}
	if ev.Event.Color != calInfo.Info.Color {
		t.Errorf("color after revert = %q, want calendar color %q", ev.Event.Color, calInfo.Info.Color)
	}
}
