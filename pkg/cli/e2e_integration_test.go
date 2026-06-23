//go:build integration

// Live e2e tests driving the cobra commands in-process (via the runCLI seam)
// against a real authenticated service. Same setup as pkg/integration: a stored
// session and a pkg/integration/config.toml naming a dedicated test calendar.
package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cheeseandcereal/proton-cal/pkg/calsvc"
	"github.com/cheeseandcereal/proton-cal/pkg/internal/e2eharness"
)

// e2eSvc is the shared live service, set by liveFactory so tests can drive
// setup/verification directly (not just through the cobra factory).
var e2eSvc *calsvc.Service

// liveFactory returns a serviceFactory backed by the shared live service plus
// the configured test calendar selector, skipping when unconfigured.
func liveFactory(t *testing.T) (func() (*calsvc.Service, error), string) {
	svc, cal := e2eharness.LiveService(t)
	e2eSvc = svc
	return func() (*calsvc.Service, error) { return svc, nil }, cal
}

func e2eSummary(label string) string     { return e2eharness.Summary(label) }
func e2eFutureSlot() (start, end string) { return e2eharness.FutureSlot() }

// createForTest creates a tagged event through the service and returns its ID
// plus a cleanup-registering deleter.
func createForTest(t *testing.T, svc *calsvc.Service, cal string, in calsvc.CreateEventInput) string {
	t.Helper()
	in.Summary = e2eSummary("cli")
	in.Calendar = cal
	in.TZ = "UTC"
	created, err := svc.CreateEvent(context.Background(), in)
	if err != nil {
		t.Fatalf("seed CreateEvent: %v", err)
	}
	t.Cleanup(func() {
		if _, err := svc.DeleteEvent(context.Background(), calsvc.DeleteEventInput{EventID: created.ID, Calendar: cal}); err != nil {
			t.Logf("cleanup delete %s: %v", created.ID, err)
		}
	})
	return created.ID
}

// TestE2ECLIGetEventJSON creates an event then reads it back via the CLI
// `get event -o json`, asserting the JSON document on stdout.
func TestE2ECLIGetEventJSON(t *testing.T) {
	factory, cal := liveFactory(t)
	start, end := e2eFutureSlot()
	evID := createForTest(t, e2eSvc, cal, calsvc.CreateEventInput{Start: start, End: end, Location: "CLI Lab"})

	stdout, _, err := runCLI(t, factory, "get", "event", "--calendar", cal, "--tz", "UTC", "-o", "json", "--", evID)
	if err != nil {
		t.Fatalf("get event -o json: %v", err)
	}
	var doc struct {
		ID       string `json:"id"`
		Location string `json:"location"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("parsing JSON output: %v\n%s", err, stdout)
	}
	if doc.ID != evID {
		t.Errorf("id = %q, want %q", doc.ID, evID)
	}
	if doc.Location != "CLI Lab" {
		t.Errorf("location = %q, want CLI Lab", doc.Location)
	}
}

// TestE2ECLICalendarsJSON lists calendars via the CLI and checks the JSON.
func TestE2ECLICalendarsJSON(t *testing.T) {
	factory, _ := liveFactory(t)
	stdout, _, err := runCLI(t, factory, "calendars", "-o", "json")
	if err != nil {
		t.Fatalf("calendars -o json: %v", err)
	}
	var rows []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("parsing calendars JSON: %v\n%s", err, stdout)
	}
	if len(rows) == 0 || rows[0].ID == "" {
		t.Errorf("expected at least one calendar with an ID, got %+v", rows)
	}
}

// TestE2ECLIFieldClearTriState validates flag-presence clearing live:
// `update --location ""` clears the field, omitting --location preserves it.
func TestE2ECLIFieldClearTriState(t *testing.T) {
	factory, cal := liveFactory(t)
	start, end := e2eFutureSlot()
	evID := createForTest(t, e2eSvc, cal, calsvc.CreateEventInput{
		Start: start, End: end, Location: "Original", Description: "keep me",
	})

	// Update an unrelated field (start), omitting --location: location kept.
	if _, _, err := runCLI(t, factory, "update", "event", "--calendar", cal, "--tz", "UTC", "--description", "changed", "--", evID); err != nil {
		t.Fatalf("update (omit location): %v", err)
	}
	got, err := e2eSvc.GetEvent(context.Background(), calsvc.GetEventInput{EventID: evID, Calendar: cal})
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.Event.Location != "Original" {
		t.Errorf("location changed by an update that omitted --location: %q", got.Event.Location)
	}
	if got.Event.Description != "changed" {
		t.Errorf("description = %q, want changed", got.Event.Description)
	}

	// Now clear it explicitly with --location "".
	if _, _, err := runCLI(t, factory, "update", "event", "--calendar", cal, "--location", "", "--", evID); err != nil {
		t.Fatalf("update (clear location): %v", err)
	}
	got, err = e2eSvc.GetEvent(context.Background(), calsvc.GetEventInput{EventID: evID, Calendar: cal})
	if err != nil {
		t.Fatalf("GetEvent after clear: %v", err)
	}
	if got.Event.Location != "" {
		t.Errorf("location not cleared by --location \"\": %q", got.Event.Location)
	}
}

// TestE2ECLICreateDelete exercises the create and delete commands themselves
// (not just the seeding helper), parsing the create JSON for the new ID.
func TestE2ECLICreateDelete(t *testing.T) {
	factory, cal := liveFactory(t)
	start, end := e2eFutureSlot()
	summary := e2eSummary("cli-create")

	stdout, _, err := runCLI(t, factory, "create", "event", summary, "--start", start, "--end", end, "--calendar", cal, "--tz", "UTC", "-o", "json")
	if err != nil {
		t.Fatalf("create -o json: %v", err)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &created); err != nil {
		t.Fatalf("parsing create JSON: %v\n%s", err, stdout)
	}
	if created.ID == "" {
		t.Fatalf("create returned no id:\n%s", stdout)
	}

	if _, _, err := runCLI(t, factory, "delete", "event", "--calendar", cal, "--", created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := e2eSvc.GetEvent(context.Background(), calsvc.GetEventInput{EventID: created.ID, Calendar: cal}); err == nil {
		t.Error("event still retrievable after CLI delete")
	}
}
