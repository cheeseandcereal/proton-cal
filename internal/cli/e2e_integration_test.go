//go:build integration

// Live e2e tests that drive the cobra commands in-process (via the runCLI
// seam) against a real authenticated service. They require the same setup as
// internal/integration: a stored session and an internal/integration/
// config.toml naming a dedicated test calendar.
package cli

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
	"github.com/cheeseandcereal/proton-cal/internal/config"
)

const e2eSummaryPrefix = "proton-cal-test"

type e2eConfig struct {
	Calendars []string `toml:"calendars"`
}

var (
	e2eOnce sync.Once
	e2eSvc  *calsvc.Service
	e2eCal  string
	e2eSkip string
)

// liveFactory returns a serviceFactory backed by a real service plus the
// configured test calendar selector, skipping when unconfigured.
func liveFactory(t *testing.T) (func() (*calsvc.Service, error), string) {
	t.Helper()
	e2eOnce.Do(func() {
		data, err := os.ReadFile("../integration/config.toml")
		if errors.Is(err, os.ErrNotExist) {
			e2eSkip = "e2e not configured: see internal/integration/README.md"
			return
		} else if err != nil {
			t.Fatalf("reading config.toml: %v", err)
		}
		var tc e2eConfig
		if err := toml.Unmarshal(data, &tc); err != nil {
			t.Fatalf("parsing config.toml: %v", err)
		}
		if len(tc.Calendars) == 0 {
			e2eSkip = "internal/integration/config.toml lists no calendars"
			return
		}
		svc, err := calsvc.New(true)
		if errors.Is(err, config.ErrNoSession) {
			e2eSkip = "no saved session; run `proton-cal login`"
			return
		} else if err != nil {
			t.Fatalf("calsvc.New: %v", err)
		}
		e2eSvc = svc
		e2eCal = tc.Calendars[0]
	})
	if e2eSkip != "" {
		t.Skip(e2eSkip)
	}
	if e2eSvc == nil {
		t.Fatal("e2e service not initialised")
	}
	// The detached service is shared, but Notify must not panic when nil.
	return func() (*calsvc.Service, error) { return e2eSvc, nil }, e2eCal
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

	stdout, _, err := runCLI(t, factory, "get", "event", evID, "--calendar", cal, "--tz", "UTC", "-o", "json")
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

// TestE2ECLIFieldClearTriState validates the CLI's flag-presence clearing
// semantics live: `update --location ""` clears the field, while an update
// that omits --location preserves it.
func TestE2ECLIFieldClearTriState(t *testing.T) {
	factory, cal := liveFactory(t)
	start, end := e2eFutureSlot()
	evID := createForTest(t, e2eSvc, cal, calsvc.CreateEventInput{
		Start: start, End: end, Location: "Original", Description: "keep me",
	})

	// Update an unrelated field (start), omitting --location: location kept.
	if _, _, err := runCLI(t, factory, "update", evID, "--calendar", cal, "--tz", "UTC", "--description", "changed"); err != nil {
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
	if _, _, err := runCLI(t, factory, "update", evID, "--calendar", cal, "--location", ""); err != nil {
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

	stdout, _, err := runCLI(t, factory, "create", summary, "--start", start, "--end", end, "--calendar", cal, "--tz", "UTC", "-o", "json")
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

	if _, _, err := runCLI(t, factory, "delete", created.ID, "--calendar", cal); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := e2eSvc.GetEvent(context.Background(), calsvc.GetEventInput{EventID: created.ID, Calendar: cal}); err == nil {
		t.Error("event still retrievable after CLI delete")
	}
}
