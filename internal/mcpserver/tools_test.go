package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal-go/internal/config"
	"github.com/cheeseandcereal/proton-cal-go/internal/front"
)

// stubServer returns a server whose bootstrap yields a session with the
// given config and no calendars/client. Only good for exercising paths that
// fail before touching the network.
func stubServer(cfg config.Config) *server {
	return &server{bootstrap: func(context.Context) (*session, error) {
		return &session{cfg: cfg}, nil
	}}
}

// failingServer returns a server whose bootstrap always fails.
func failingServer(err error) *server {
	return &server{bootstrap: func(context.Context) (*session, error) {
		return nil, err
	}}
}

func TestResolveCreateTimes(t *testing.T) {
	t.Run("timed without end errors", func(t *testing.T) {
		_, _, err := resolveCreateTimes("2026-06-15 09:00", "", false, "UTC")
		if err == nil || !strings.Contains(err.Error(), "end is required") {
			t.Fatalf("want 'end is required' error, got %v", err)
		}
	})

	t.Run("timed parses in zone", func(t *testing.T) {
		start, end, err := resolveCreateTimes("2026-06-15 09:00", "2026-06-15 09:30", false, "UTC")
		if err != nil {
			t.Fatal(err)
		}
		if got := start.Format("2006-01-02 15:04"); got != "2026-06-15 09:00" {
			t.Errorf("start = %s", got)
		}
		if d := end.Sub(start); d != 30*time.Minute {
			t.Errorf("duration = %v, want 30m", d)
		}
	})

	t.Run("all-day default end is start plus one day", func(t *testing.T) {
		start, end, err := resolveCreateTimes("2026-06-15", "", true, "UTC")
		if err != nil {
			t.Fatal(err)
		}
		if d := end.Sub(start); d != 24*time.Hour {
			t.Errorf("duration = %v, want 24h", d)
		}
	})

	t.Run("all-day end is inclusive", func(t *testing.T) {
		start, end, err := resolveCreateTimes("2026-06-15", "2026-06-16", true, "UTC")
		if err != nil {
			t.Fatal(err)
		}
		if d := end.Sub(start); d != 48*time.Hour {
			t.Errorf("duration = %v, want 48h (inclusive end -> exclusive +24h)", d)
		}
	})

	t.Run("all-day end before start errors", func(t *testing.T) {
		_, _, err := resolveCreateTimes("2026-06-15", "2026-06-13", true, "UTC")
		if err == nil || !strings.Contains(err.Error(), "end date") {
			t.Fatalf("want end-before-start error, got %v", err)
		}
	})

	t.Run("bad timed start errors", func(t *testing.T) {
		if _, _, err := resolveCreateTimes("tomorrow", "2026-06-15 10:00", false, "UTC"); err == nil {
			t.Fatal("want parse error for 'tomorrow'")
		}
	})

	t.Run("all-day rejects datetime form", func(t *testing.T) {
		if _, _, err := resolveCreateTimes("2026-06-15 09:00", "", true, "UTC"); err == nil {
			t.Fatal("want parse error for datetime with all_day")
		}
	})
}

func TestValidateUpdateArgs(t *testing.T) {
	tests := []struct {
		name       string
		occurrence string
		noRepeat   bool
		rec        front.RecurrenceFlags
		wantErr    string
	}{
		{name: "no args ok"},
		{name: "repeat alone ok", rec: front.RecurrenceFlags{Repeat: "weekly"}},
		{name: "occurrence alone ok", occurrence: "2026-06-15 09:00"},
		{name: "no_repeat alone ok", noRepeat: true},
		{
			name: "no_repeat conflicts with repeat", noRepeat: true,
			rec: front.RecurrenceFlags{Repeat: "daily"}, wantErr: "no_repeat",
		},
		{
			name: "no_repeat conflicts with rrule", noRepeat: true,
			rec: front.RecurrenceFlags{RawRRule: "FREQ=DAILY"}, wantErr: "no_repeat",
		},
		{
			name: "occurrence conflicts with rrule", occurrence: "2026-06-15 09:00",
			rec: front.RecurrenceFlags{RawRRule: "FREQ=DAILY"}, wantErr: "occurrence",
		},
		{
			name: "occurrence conflicts with repeat", occurrence: "2026-06-15 09:00",
			rec: front.RecurrenceFlags{Repeat: "weekly"}, wantErr: "occurrence",
		},
		{
			name: "occurrence conflicts with no_repeat", occurrence: "2026-06-15 09:00",
			noRepeat: true, wantErr: "occurrence",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateUpdateArgs(tt.occurrence, tt.noRepeat, tt.rec)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
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

func TestUpdateEventConflictsBeforeSession(t *testing.T) {
	// Conflict validation must run before the session bootstrap (so it
	// works logged-out and is cheap).
	s := failingServer(errors.New("bootstrap must not be reached"))

	_, _, err := s.updateEvent(context.Background(), nil, updateEventArgs{
		EventID:  "abc",
		NoRepeat: true,
		Repeat:   "daily",
	})
	if err == nil || !strings.Contains(err.Error(), "no_repeat cannot be combined") {
		t.Fatalf("want no_repeat conflict, got %v", err)
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

func TestUpdateEventBadStart(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})
	_, _, err := s.updateEvent(context.Background(), nil, updateEventArgs{
		EventID: "abc",
		Start:   "next tuesday",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("want invalid-arguments error, got %v", err)
	}
}

func TestUpdateEventBadOccurrence(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})
	_, _, err := s.updateEvent(context.Background(), nil, updateEventArgs{
		EventID:    "abc",
		Occurrence: "15/06/2026",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("want invalid-arguments error, got %v", err)
	}
}

func TestDeleteEventBadOccurrence(t *testing.T) {
	s := stubServer(config.Config{Timezone: "UTC"})
	_, _, err := s.deleteEvent(context.Background(), nil, deleteEventArgs{
		EventID:    "abc",
		Occurrence: "not a time",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid arguments") {
		t.Fatalf("want invalid-arguments error, got %v", err)
	}
}

func TestSessionBootstrapErrorSurfaces(t *testing.T) {
	s := failingServer(errors.New("not logged in; run `proton-cal login` first"))
	_, _, err := s.listCalendars(context.Background(), nil, listCalendarsArgs{})
	if err == nil || !strings.Contains(err.Error(), "proton-cal login") {
		t.Fatalf("want login-directing error, got %v", err)
	}
}

func TestSessionBootstrapCachedAfterSuccess(t *testing.T) {
	calls := 0
	s := &server{bootstrap: func(context.Context) (*session, error) {
		calls++
		return &session{cfg: config.Config{Timezone: "UTC"}}, nil
	}}
	for range 3 {
		if _, err := s.session(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("bootstrap called %d times, want 1", calls)
	}
}
