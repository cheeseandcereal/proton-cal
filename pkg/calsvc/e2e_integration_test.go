//go:build integration

// Package-level live e2e tests for the calsvc service layer. They run only
// under the `integration` build tag and require the same setup as the
// pkg/integration suite: a stored session (`proton-cal login`) and an
// pkg/integration/config.toml naming a dedicated test calendar.
//
// Every event these tests create carries the same "proton-cal-test" summary
// tag the integration suite's sweep (TestZSweep) recognises, so a crashed
// run is still cleaned up by `make integration`.
package calsvc

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

	"github.com/cheeseandcereal/proton-cal/pkg/config"
)

const (
	e2eSummaryPrefix = "proton-cal-test"
	e2eOffsetDays    = 30
)

type e2eConfig struct {
	Calendars []string `toml:"calendars"`
}

var (
	e2eOnce sync.Once
	e2eSvc  *Service
	e2eCal  string // first configured calendar selector
	e2eSkip string
)

// liveService returns a real, authenticated Service and the configured test
// calendar selector, skipping the test when the suite is not configured.
func liveService(t *testing.T) (*Service, string) {
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
		svc, err := New(true) // no cache: always hit the live API
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
	return e2eSvc, e2eCal
}

func e2eSummary(label string) string {
	b := make([]byte, 4)
	if _, err := crand.Read(b); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%s %s %s", e2eSummaryPrefix, hex.EncodeToString(b), label)
}

// e2eFutureDate returns a YYYY-MM-DD date string ~30 days out.
func e2eFutureDate() string {
	return time.Now().UTC().AddDate(0, 0, e2eOffsetDays).Format("2006-01-02")
}

// e2eFutureSlot returns start/end "YYYY-MM-DD HH:MM" ~30 days out at a fixed
// wall time. Whole minutes keep the round trip exact.
func e2eFutureSlot() (start, end string) {
	d := e2eFutureDate()
	return d + " 09:00", d + " 09:30"
}

// trackDelete schedules a best-effort cleanup of an event by ID.
func trackDelete(t *testing.T, svc *Service, calSel, eventID string) (markDeleted func()) {
	t.Helper()
	var done bool
	t.Cleanup(func() {
		if done || eventID == "" {
			return
		}
		if _, err := svc.DeleteEvent(context.Background(), DeleteEventInput{EventID: eventID, Calendar: calSel}); err != nil {
			t.Logf("cleanup: delete %s: %v (TestZSweep will catch it)", eventID, err)
		}
	})
	return func() { done = true }
}

// TestE2EServiceLifecycle drives the full timed-event lifecycle through the
// service layer: create -> get -> field update -> time update -> list -> delete.
func TestE2EServiceLifecycle(t *testing.T) {
	svc, cal := liveService(t)
	ctx := context.Background()

	start, end := e2eFutureSlot()
	summary := e2eSummary("svc-lifecycle") + " with, comma; semi\nnewline"
	created, err := svc.CreateEvent(ctx, CreateEventInput{
		Summary:     summary,
		Description: "svc e2e",
		Location:    "Lab",
		Start:       start,
		End:         end,
		Calendar:    cal,
		TZ:          "UTC",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if created.ID == "" {
		t.Fatal("create did not echo an ID")
	}
	markDeleted := trackDelete(t, svc, cal, created.ID)

	// Get: fields round-trip (incl. RFC 5545 escaping through the API).
	got, err := svc.GetEvent(ctx, GetEventInput{EventID: created.ID, Calendar: cal, TZ: "UTC"})
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.Event.Summary != summary {
		t.Errorf("summary round trip:\n got %q\nwant %q", got.Event.Summary, summary)
	}
	if got.Event.Location != "Lab" || got.Event.Description != "svc e2e" {
		t.Errorf("location/description round trip: %q / %q", got.Event.Location, got.Event.Description)
	}
	if got.Event.AllDay {
		t.Error("timed event came back all-day")
	}

	// Field-only update.
	renamed := e2eSummary("svc-renamed")
	if _, err := svc.UpdateEvent(ctx, UpdateEventInput{EventID: created.ID, Calendar: cal, Summary: &renamed}); err != nil {
		t.Fatalf("UpdateEvent (summary): %v", err)
	}
	got, err = svc.GetEvent(ctx, GetEventInput{EventID: created.ID, Calendar: cal, TZ: "UTC"})
	if err != nil {
		t.Fatalf("GetEvent after rename: %v", err)
	}
	if got.Event.Summary != renamed {
		t.Errorf("summary after update = %q, want %q", got.Event.Summary, renamed)
	}
	if got.Event.Location != "Lab" {
		t.Errorf("location lost across field-only update: %q", got.Event.Location)
	}

	// Time update.
	ns, ne := got.Event.Start.Add(time.Hour).Format("2006-01-02 15:04"), got.Event.End.Add(time.Hour).Format("2006-01-02 15:04")
	if _, err := svc.UpdateEvent(ctx, UpdateEventInput{EventID: created.ID, Calendar: cal, Start: ns, End: ne, TZ: "UTC"}); err != nil {
		t.Fatalf("UpdateEvent (time): %v", err)
	}

	// List: the (renamed) event appears in the window.
	list, err := svc.ListEvents(ctx, ListEventsInput{Calendars: []string{cal}, TZ: "UTC", From: e2eFutureDate() + " 00:00", Days: 2})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var found bool
	for _, l := range list.Items {
		if l.Event != nil && l.Event.EventID == created.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("created event %s not found in listing of %d items", created.ID, len(list.Items))
	}

	// Delete -> gone.
	res, err := svc.DeleteEvent(ctx, DeleteEventInput{EventID: created.ID, Calendar: cal})
	if err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	if res.RowsDeleted < 1 {
		t.Errorf("delete removed %d rows", res.RowsDeleted)
	}
	markDeleted()
	if _, err := svc.GetEvent(ctx, GetEventInput{EventID: created.ID, Calendar: cal}); err == nil {
		t.Error("event still retrievable after delete")
	}
}

// TestE2ECreateEventDefaultEnd creates a timed event with no explicit end and
// asserts the end defaults to the calendar's DefaultEventDuration.
func TestE2ECreateEventDefaultEnd(t *testing.T) {
	svc, cal := liveService(t)
	ctx := context.Background()

	gotCal, err := svc.GetCalendar(ctx, cal)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	dur, ok := gotCal.Settings.DefaultDuration()
	if !ok {
		t.Skipf("calendar %q defines no default duration; cannot validate default end", cal)
	}

	start := e2eFutureDate() + " 09:00"
	created, err := svc.CreateEvent(ctx, CreateEventInput{
		Summary:  e2eSummary("svc-default-end"),
		Start:    start,
		Calendar: cal,
		TZ:       "UTC",
		// End deliberately omitted.
	})
	if err != nil {
		t.Fatalf("CreateEvent without end: %v", err)
	}
	if created.ID == "" {
		t.Fatal("create did not echo an ID")
	}
	trackDelete(t, svc, cal, created.ID)

	got, err := svc.GetEvent(ctx, GetEventInput{EventID: created.ID, Calendar: cal, TZ: "UTC"})
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if d := got.Event.End.Sub(got.Event.Start); d != dur {
		t.Errorf("default end duration = %v, want %v (calendar default)", d, dur)
	}
}

// TestE2EGetCalendar exercises GetCalendar against the live API.
func TestE2EGetCalendar(t *testing.T) {
	svc, cal := liveService(t)
	got, err := svc.GetCalendar(context.Background(), cal)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	if got.Info.ID == "" || got.Info.Name == "" {
		t.Errorf("calendar missing id/name: %+v", got.Info)
	}
}

// TestE2EErrorPaths checks that the friendly errors surface on the live API.
func TestE2EErrorPaths(t *testing.T) {
	svc, cal := liveService(t)
	ctx := context.Background()

	t.Run("get nonexistent", func(t *testing.T) {
		if _, err := svc.GetEvent(ctx, GetEventInput{EventID: "does-not-exist", Calendar: cal}); err == nil {
			t.Error("want error getting a nonexistent event")
		}
	})
	t.Run("delete nonexistent", func(t *testing.T) {
		if _, err := svc.DeleteEvent(ctx, DeleteEventInput{EventID: "does-not-exist", Calendar: cal}); err == nil {
			t.Error("want error deleting a nonexistent event")
		}
	})
	t.Run("unresolvable calendar", func(t *testing.T) {
		_, err := svc.ListEvents(ctx, ListEventsInput{Calendars: []string{"no-such-calendar-xyz"}})
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "calendar") {
			t.Errorf("want calendar-resolution error, got %v", err)
		}
	})
}
