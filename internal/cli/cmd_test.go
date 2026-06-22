package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
	"github.com/cheeseandcereal/proton-cal/internal/config"
)

// detachedFactory returns a serviceFactory backed by a detached Service:
// validation and arg-parsing paths run to completion (they fail before any
// network use), while a successful domain call would panic on the nil
// client - so these tests only assert on the pre-network behavior.
func detachedFactory() (*calsvc.Service, error) {
	return calsvc.NewDetached(config.Config{Timezone: "UTC"}), nil
}

func TestCLIInvalidOutputFlag(t *testing.T) {
	_, _, err := runCLI(t, nil, "calendars", "-o", "yaml")
	if err == nil || !strings.Contains(err.Error(), "invalid --output") {
		t.Fatalf("want invalid --output error, got %v", err)
	}
}

func TestCLICreateValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"timed missing end", []string{"create", "Lunch", "--start", "2026-06-15 09:00"}, "end is required"},
		{"bad start", []string{"create", "Lunch", "--start", "nope", "--end", "2026-06-15 10:00"}, "invalid date"},
		{"rrule conflicts repeat", []string{"create", "X", "--start", "2026-06-15 09:00", "--end", "2026-06-15 10:00", "--rrule", "FREQ=DAILY", "--repeat", "daily"}, "rrule cannot be combined"},
		{"modifiers require repeat", []string{"create", "X", "--start", "2026-06-15 09:00", "--end", "2026-06-15 10:00", "--count", "3"}, "require repeat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCLI(t, detachedFactory, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("args %v: want error containing %q, got %v", tt.args, tt.want, err)
			}
		})
	}
}

func TestCLICreateRequiresStartFlag(t *testing.T) {
	_, _, err := runCLI(t, detachedFactory, "create", "NoStart")
	if err == nil || !strings.Contains(err.Error(), "start") {
		t.Fatalf("want required-flag error mentioning start, got %v", err)
	}
}

func TestCLIUpdateConflicts(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no-repeat with repeat", []string{"update", "evt", "--no-repeat", "--repeat", "daily"}, "no-repeat cannot be combined"},
		{"occurrence with rrule", []string{"update", "evt", "--occurrence", "2026-06-15 09:00", "--rrule", "FREQ=DAILY"}, "occurrence"},
		{"bad start", []string{"update", "evt", "--start", "whenever"}, "invalid date"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := runCLI(t, detachedFactory, tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("args %v: want %q, got %v", tt.args, tt.want, err)
			}
		})
	}
}

func TestCLIGetEventICSWithJSONConflict(t *testing.T) {
	_, _, err := runCLI(t, detachedFactory, "get", "event", "evt", "--ics", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "--ics cannot be combined") {
		t.Fatalf("want ics/json conflict, got %v", err)
	}
}

func TestCLIDeleteBadOccurrence(t *testing.T) {
	_, _, err := runCLI(t, detachedFactory, "delete", "evt", "--occurrence", "15/06/2026")
	if err == nil || !strings.Contains(err.Error(), "invalid date") {
		t.Fatalf("want occurrence parse error, got %v", err)
	}
}

func TestCLIUnknownFieldSelector(t *testing.T) {
	_, _, err := runCLI(t, detachedFactory, "get", "event", "evt", "--fields", "bogusfield")
	if err == nil {
		t.Fatalf("want unknown-field error, got nil")
	}
}

func TestRenderCalendars(t *testing.T) {
	var buf bytes.Buffer
	cals := []calendar.Info{
		{ID: "id1", Name: "Personal", Color: "#415DF0", Type: 0, Description: "my cal"},
		{ID: "id2", Name: "Work", Color: "#EC3E7C", Type: 0},
	}
	renderCalendars(&buf, cals, "Work")
	out := buf.String()
	if !strings.Contains(out, "Personal (normal)") || !strings.Contains(out, "ID: id1") {
		t.Errorf("missing Personal header/id:\n%s", out)
	}
	if !strings.Contains(out, "Work (normal)  [default]") {
		t.Errorf("missing default marker on Work:\n%s", out)
	}
	if !strings.Contains(out, "Color: ") || !strings.Contains(out, "#415DF0") {
		t.Errorf("missing color row:\n%s", out)
	}
	if !strings.Contains(out, "Description: my cal") {
		t.Errorf("missing description:\n%s", out)
	}
}

func TestRenderCalendarsEmpty(t *testing.T) {
	var buf bytes.Buffer
	renderCalendars(&buf, nil, "")
	if !strings.Contains(buf.String(), "No calendars found.") {
		t.Errorf("empty render = %q", buf.String())
	}
}

// TestCLIGetParentShowsHelp verifies the bare "get" command prints help to
// the captured stdout rather than erroring (it has subcommands).
func TestCLIGetParentHelp(t *testing.T) {
	out, _, err := runCLI(t, nil, "get")
	if err != nil {
		t.Fatalf("get help: %v", err)
	}
	if !strings.Contains(out, "event") || !strings.Contains(out, "calendar") {
		t.Errorf("get help missing subcommands; got:\n%s", out)
	}
}
