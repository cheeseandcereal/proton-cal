package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
	"github.com/cheeseandcereal/proton-cal/internal/config"
)

// stubServer returns a server whose bootstrap yields a detached service
// with the given config (no session/client). Only good for exercising
// paths that fail before touching the network.
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

func TestCreateEventTimedWithoutEnd(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})
	_, _, err := s.createEvent(context.Background(), nil, createEventArgs{
		Summary: "Standup",
		Start:   "2026-06-15 09:00",
	})
	if err == nil || !strings.Contains(err.Error(), "end is required") {
		t.Fatalf("want 'end is required' error, got %v", err)
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
	if err := applyClearFields(&in, []string{"bogus"}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("want unknown-field error, got %v", err)
	}
}

func TestUpdateEventUnknownClearField(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})
	_, _, err := s.updateEvent(context.Background(), nil, updateEventArgs{
		EventID:     "abc",
		ClearFields: []string{"color"},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("want unknown clear field error, got %v", err)
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
