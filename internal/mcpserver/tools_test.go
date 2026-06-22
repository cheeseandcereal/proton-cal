package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
	"github.com/cheeseandcereal/proton-cal/internal/config"
)

// stubServer returns a server whose bootstrap yields a detached service (no
// session/client). Only for paths that fail before touching the network.
func stubServer(cfg config.Config) *server {
	return &server{bootstrap: func() (*calsvc.Service, error) {
		return calsvc.NewDetached(cfg), nil
	}}
}

// failingServer returns a server whose bootstrap always fails.
func failingServer(err error) *server {
	return &server{bootstrap: func() (*calsvc.Service, error) {
		return nil, err
	}}
}

// apiStubServer returns a server whose service reads canned GET bodies so
// resolution-dependent paths run offline. Writes and key-unlock still panic.
func apiStubServer(cfg config.Config, bodies map[string]string) *server {
	return &server{bootstrap: func() (*calsvc.Service, error) {
		return calsvc.NewWithAPI(cfg, mcpFakeAPI{bodies: bodies}), nil
	}}
}

type mcpFakeAPI struct{ bodies map[string]string }

func (f mcpFakeAPI) Get(_ context.Context, path string, _ url.Values, out any) error {
	body := f.bodies[path]
	if body == "" {
		body = `{}`
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(body), out)
}

func (mcpFakeAPI) Put(context.Context, string, any, any) error  { return nil }
func (mcpFakeAPI) Post(context.Context, string, any, any) error { return nil }
func (mcpFakeAPI) Delete(context.Context, string, any) error    { return nil }

// A timed event with no end no longer errors pre-network (end defaults to
// calendar duration); a bad start still fails in the detached path tested here.
func TestCreateEventBadStart(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})
	_, _, err := s.createEvent(context.Background(), nil, createEventArgs{
		Summary: "Standup",
		Start:   "nope",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid datetime") {
		t.Fatalf("want 'invalid datetime' error, got %v", err)
	}
}

func TestCreateEventRecurrenceConflict(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})
	_, _, err := s.createEvent(context.Background(), nil, createEventArgs{
		Summary: "Standup",
		Start:   "2026-06-15 09:00",
		End:     "2026-06-15 09:15",
		Repeat:  "daily",
		RRule:   "FREQ=DAILY",
	})
	if err == nil || !strings.Contains(err.Error(), "rrule") {
		t.Fatalf("want rrule-conflict error, got %v", err)
	}
}

func TestCreateEventEveryWithoutRepeat(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})
	_, _, err := s.createEvent(context.Background(), nil, createEventArgs{
		Summary: "Standup",
		Start:   "2026-06-15 09:00",
		End:     "2026-06-15 09:15",
		Every:   2,
	})
	if err == nil || !strings.Contains(err.Error(), "repeat") {
		t.Fatalf("want every-requires-repeat error, got %v", err)
	}
}

func TestUpdateEventConflicts(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})

	_, _, err := s.updateEvent(context.Background(), nil, updateEventArgs{
		EventID:  "abc",
		NoRepeat: true,
		Repeat:   "daily",
	})
	if err == nil || !strings.Contains(err.Error(), "no-repeat cannot be combined") {
		t.Fatalf("want no-repeat conflict, got %v", err)
	}

	_, _, err = s.updateEvent(context.Background(), nil, updateEventArgs{
		EventID:    "abc",
		Occurrence: "2026-06-15 09:00",
		RRule:      "FREQ=DAILY",
	})
	if err == nil || !strings.Contains(err.Error(), "occurrence") {
		t.Fatalf("want occurrence conflict, got %v", err)
	}
}

func TestApplyClearFields(t *testing.T) {
	var in calsvc.UpdateEventInput
	if err := applyClearFields(&in, []string{"summary", "location"}); err != nil {
		t.Fatalf("applyClearFields: %v", err)
	}
	if in.Summary == nil || *in.Summary != "" {
		t.Errorf("summary not cleared: %v", in.Summary)
	}
	if in.Location == nil || *in.Location != "" {
		t.Errorf("location not cleared: %v", in.Location)
	}
	if in.Description != nil {
		t.Errorf("description should be untouched: %v", in.Description)
	}
	// "color" reverts to the calendar color (Inherit intent).
	if err := applyClearFields(&in, []string{"color"}); err != nil {
		t.Fatalf("clear color: %v", err)
	}
	if in.Color == nil || !in.Color.Inherit {
		t.Errorf("color not set to inherit: %+v", in.Color)
	}
	if err := applyClearFields(&in, []string{"bogus"}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("want unknown-field error, got %v", err)
	}
}

func TestUpdateEventUnknownClearField(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})
	_, _, err := s.updateEvent(context.Background(), nil, updateEventArgs{
		EventID:     "abc",
		ClearFields: []string{"priority"},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("want unknown clear field error, got %v", err)
	}
}

func TestResolveUpdateReminders(t *testing.T) {
	t.Run("keep", func(t *testing.T) {
		u, err := resolveUpdateReminders("", nil)
		if err != nil || u != nil {
			t.Errorf("got (%v, %v), want keep (nil,nil)", u, err)
		}
	})
	t.Run("empty mode + list -> custom", func(t *testing.T) {
		u, err := resolveUpdateReminders("", []string{"15m"})
		if err != nil || u == nil || u.Inherit || len(u.List) != 1 {
			t.Errorf("got (%+v, %v)", u, err)
		}
	})
	t.Run("inherit", func(t *testing.T) {
		u, err := resolveUpdateReminders("inherit", nil)
		if err != nil || u == nil || !u.Inherit {
			t.Errorf("got (%+v, %v)", u, err)
		}
	})
	t.Run("none", func(t *testing.T) {
		u, err := resolveUpdateReminders("none", nil)
		if err != nil || u == nil || u.Inherit || len(u.List) != 0 {
			t.Errorf("got (%+v, %v)", u, err)
		}
	})
	t.Run("custom requires list", func(t *testing.T) {
		if _, err := resolveUpdateReminders("custom", nil); err == nil || !strings.Contains(err.Error(), "requires at least one") {
			t.Errorf("want custom-empty error, got %v", err)
		}
	})
	t.Run("bad mode", func(t *testing.T) {
		if _, err := resolveUpdateReminders("sometimes", nil); err == nil || !strings.Contains(err.Error(), "invalid reminders_mode") {
			t.Errorf("want invalid-mode error, got %v", err)
		}
	})
	t.Run("bad spec", func(t *testing.T) {
		if _, err := resolveUpdateReminders("custom", []string{"nope"}); err == nil {
			t.Error("want parse error")
		}
	})
}

func TestCreateEventReminderConflict(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})
	_, _, err := s.createEvent(context.Background(), nil, createEventArgs{
		Summary: "X", Start: "2026-06-15 09:00", End: "2026-06-15 10:00",
		Reminders: []string{"15m"}, NoReminders: true,
	})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("want reminders/no_reminders conflict, got %v", err)
	}
}

func TestCreateEventBadReminder(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})
	_, _, err := s.createEvent(context.Background(), nil, createEventArgs{
		Summary: "X", Start: "2026-06-15 09:00", End: "2026-06-15 10:00",
		Reminders: []string{"whenever"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid reminder offset") {
		t.Fatalf("want bad-reminder error, got %v", err)
	}
}

func TestUpdateEventBadStart(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})
	_, _, err := s.updateEvent(context.Background(), nil, updateEventArgs{
		EventID: "abc",
		Start:   "next tuesday",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid date/time") {
		t.Fatalf("want parse error with format hint, got %v", err)
	}
}

func TestUpdateEventBadOccurrence(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})
	_, _, err := s.updateEvent(context.Background(), nil, updateEventArgs{
		EventID:    "abc",
		Occurrence: "15/06/2026",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid date/time") {
		t.Fatalf("want parse error with format hint, got %v", err)
	}
}

func TestDeleteEventBadOccurrence(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})
	_, _, err := s.deleteEvent(context.Background(), nil, deleteEventArgs{
		EventID:    "abc",
		Occurrence: "not a time",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid date/time") {
		t.Fatalf("want parse error with format hint, got %v", err)
	}
}

func TestSessionBootstrapErrorSurfaces(t *testing.T) {
	s := failingServer(errors.New("not logged in; run `proton-cal login` first"))
	_, _, err := s.listCalendars(context.Background(), nil, listCalendarsArgs{})
	if err == nil || !strings.Contains(err.Error(), "proton-cal login") {
		t.Fatalf("want login-directing error, got %v", err)
	}
}

func TestServiceBootstrapCachedAfterSuccess(t *testing.T) {
	calls := 0
	s := &server{bootstrap: func() (*calsvc.Service, error) {
		calls++
		return calsvc.NewDetached(config.Config{Timezone: "UTC"}), nil
	}}
	for range 3 {
		if _, err := s.service(); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("bootstrap called %d times, want 1", calls)
	}
}
