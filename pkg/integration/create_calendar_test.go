//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/cheeseandcereal/proton-cal/pkg/auth"
	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/event"
)

// TestCreateCalendarLifecycle exercises the full owned-calendar create flow
// live: two-step create (metadata + key setup), unlock via the normal bootstrap
// path, a real event round-trip into the new calendar, then cleanup by deleting
// the calendar (which needs the elevated scope, hence the configured password).
//
// It SKIPS unless config.toml carries `password` (owned-calendar delete cannot
// otherwise clean up after itself). The calendar name is tagged so a human can
// spot any leak if cleanup ever fails.
func TestCreateCalendarLifecycle(t *testing.T) {
	ctx := context.Background()
	s := setup(t)
	if s.tc.Password == "" {
		t.Skip("config.toml has no password; skipping calendar create/delete lifecycle (owned-calendar delete needs the login password)")
	}

	addressID, addrKR, err := s.keychain.SigningAddress()
	if err != nil {
		t.Fatalf("SigningAddress: %v", err)
	}

	name := uniqueSummary("create-calendar")
	description := "Created by the proton-cal integration suite."
	info, err := calendar.Create(ctx, s.client, calendar.CreateInput{
		Name:        name,
		Description: description,
		ColorHex:    "#F78400",
		AddressID:   addressID,
		AddrKR:      addrKR,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.ID == "" {
		t.Fatal("Create returned no calendar ID")
	}

	// Always attempt cleanup, even if later assertions fail. Deleting an owned
	// calendar needs the elevated "locked" scope (re-prove the password).
	var deleted bool
	t.Cleanup(func() {
		if deleted {
			return
		}
		username := s.cfg.Username
		if username == "" {
			username = s.unlocked.User.Name
		}
		err := auth.WithLockedScope(ctx, s.client.Manager(), s.client, username, s.tc.Password, func() error {
			return calendar.DeleteCalendar(ctx, s.client, info.ID, false)
		})
		if err != nil {
			t.Errorf("cleanup: could not delete test calendar %q (%s): %v", name, info.ID, err)
		}
	})

	if info.Name != name {
		t.Errorf("created calendar name = %q, want %q", info.Name, name)
	}
	if info.Description != description {
		t.Errorf("created calendar description = %q, want %q", info.Description, description)
	}
	if info.Color != "#F78400" {
		t.Errorf("created calendar color = %q, want %q", info.Color, "#F78400")
	}
	if info.Type != 0 {
		t.Errorf("created calendar Type = %d, want 0 (owned)", info.Type)
	}

	// Unlock the freshly created calendar through the normal bootstrap path:
	// the create crypto must be readable by the standard unlock chain.
	access, err := s.keychain.Unlock(ctx, info)
	if err != nil {
		t.Fatalf("Unlock newly created calendar: %v", err)
	}
	if access.KR.CountEntities() == 0 {
		t.Fatal("unlocked calendar keyring has no keys")
	}

	// A real event must round-trip into the new calendar.
	start, end := futureWindow()
	summary := uniqueSummary("in-new-calendar")
	created, err := event.Create(ctx, s.client, access, event.CreateOptions{
		Summary: summary,
		Start:   start,
		End:     end,
		TZName:  s.tzName,
	})
	if err != nil {
		t.Fatalf("creating event in new calendar: %v", err)
	}
	if created == nil || created.ID == "" {
		t.Fatalf("event create did not echo a row: %+v", created)
	}
	ev := getDecrypted(t, s.client, access, created.ID)
	if ev.Summary != summary {
		t.Errorf("event summary round-trip: got %q, want %q", ev.Summary, summary)
	}
	if !ev.Start.Equal(start) {
		t.Errorf("event Start = %v, want %v", ev.Start, start)
	}

	// Delete the event before the calendar (calendar delete would orphan it,
	// but this keeps the flow explicit and verifies event delete too).
	if _, err := event.SmartDelete(ctx, s.client, access, created.ID, 0); err != nil {
		t.Errorf("deleting event in new calendar: %v", err)
	}

	// Delete the calendar now (inside the test) so we can assert success; the
	// Cleanup above becomes a no-op.
	username := s.cfg.Username
	if username == "" {
		username = s.unlocked.User.Name
	}
	if err := auth.WithLockedScope(ctx, s.client.Manager(), s.client, username, s.tc.Password, func() error {
		return calendar.DeleteCalendar(ctx, s.client, info.ID, false)
	}); err != nil {
		t.Fatalf("deleting test calendar: %v", err)
	}
	deleted = true

	// It must no longer be listed.
	cals, err := calendar.List(ctx, s.client)
	if err != nil {
		t.Fatalf("listing calendars after delete: %v", err)
	}
	for _, c := range cals {
		if c.ID == info.ID {
			t.Errorf("calendar %s still present after delete", info.ID)
		}
	}
}
