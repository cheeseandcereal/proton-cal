package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
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

// cliFakeAPI serves canned GET bodies (read-only) so resolution-dependent CLI
// paths can be exercised; writes are no-ops.
type cliFakeAPI struct{ bodies map[string]string }

func (f cliFakeAPI) Get(_ context.Context, path string, _ url.Values, out any) error {
	body := f.bodies[path]
	if body == "" {
		body = `{}`
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(body), out)
}

func (cliFakeAPI) Put(context.Context, string, any, any) error  { return nil }
func (cliFakeAPI) Post(context.Context, string, any, any) error { return nil }
func (cliFakeAPI) Delete(context.Context, string, any) error    { return nil }

// fakeAPIFactory returns a serviceFactory whose read path is served by the
// given canned bodies (calendar list etc.), so resolution succeeds offline.
func fakeAPIFactory(bodies map[string]string) func() (*calsvc.Service, error) {
	return func() (*calsvc.Service, error) {
		return calsvc.NewWithAPI(config.Config{Timezone: "UTC"}, cliFakeAPI{bodies: bodies}), nil
	}
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
		// "timed missing end" is no longer a pre-network error: the end now
		// defaults to the calendar's duration, resolved after the calendar is
		// unlocked (covered by calsvc.TestApplyDefaultDuration).
		{"bad start", []string{"create", "event", "Lunch", "--start", "nope", "--end", "2026-06-15 10:00"}, "invalid date"},
		{"rrule conflicts repeat", []string{"create", "event", "X", "--start", "2026-06-15 09:00", "--end", "2026-06-15 10:00", "--rrule", "FREQ=DAILY", "--repeat", "daily"}, "rrule cannot be combined"},
		{"modifiers require repeat", []string{"create", "event", "X", "--start", "2026-06-15 09:00", "--end", "2026-06-15 10:00", "--count", "3"}, "require repeat"},
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
	_, _, err := runCLI(t, detachedFactory, "create", "event", "NoStart")
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
		{"no-repeat with repeat", []string{"update", "event", "evt", "--no-repeat", "--repeat", "daily"}, "no-repeat cannot be combined"},
		{"occurrence with rrule", []string{"update", "event", "evt", "--occurrence", "2026-06-15 09:00", "--rrule", "FREQ=DAILY"}, "occurrence"},
		{"bad start", []string{"update", "event", "evt", "--start", "whenever"}, "invalid date"},
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

func TestCLICreateReminderColorValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"bad reminder", []string{"create", "event", "X", "--start", "2026-06-15 09:00", "--end", "2026-06-15 10:00", "--reminder", "soon"}, "invalid reminder offset"},
		{"reminder + no-reminders", []string{"create", "event", "X", "--start", "2026-06-15 09:00", "--end", "2026-06-15 10:00", "--reminder", "15m", "--no-reminders"}, "mutually exclusive"},
		{"bad color", []string{"create", "event", "X", "--start", "2026-06-15 09:00", "--end", "2026-06-15 10:00", "--color", "red"}, "invalid color"},
		{"empty color", []string{"create", "event", "X", "--start", "2026-06-15 09:00", "--end", "2026-06-15 10:00", "--color", ""}, "requires a value"},
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

func TestCLIUpdateReminderConflicts(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"reminder + reminders-default", []string{"update", "event", "evt", "--reminder", "15m", "--reminders-default"}, "mutually exclusive"},
		{"no-reminders + reminders-default", []string{"update", "event", "evt", "--no-reminders", "--reminders-default"}, "mutually exclusive"},
		{"bad color", []string{"update", "event", "evt", "--color", "chartreuse"}, "invalid color"},
		{"empty color", []string{"update", "event", "evt", "--color", ""}, "requires a value"},
		{"bad reminder", []string{"update", "event", "evt", "--reminder", "nope"}, "invalid reminder offset"},
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
	_, _, err := runCLI(t, detachedFactory, "delete", "event", "evt", "--occurrence", "15/06/2026")
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
	renderCalendars(&buf, cals, "id2")
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

// TestCLIGetParentNoSubcommand verifies the bare "get" command (no
// subcommand) errors as a usage error: usage goes to stderr (naming its
// subcommands), stdout stays empty, and ErrReported is returned.
func TestCLIGetParentNoSubcommand(t *testing.T) {
	out, errOut, err := runCLI(t, nil, "get")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("get: err = %v, want ErrReported", err)
	}
	if out != "" {
		t.Errorf("stdout should be empty on usage error; got:\n%s", out)
	}
	if !strings.Contains(errOut, "event") || !strings.Contains(errOut, "calendar") {
		t.Errorf("get usage missing subcommands; got:\n%s", errOut)
	}
	if !strings.Contains(errOut, "Error:") || !strings.Contains(errOut, "Usage:") {
		t.Errorf("get usage missing Error/Usage lines; got:\n%s", errOut)
	}
}

// The restructured create/update/delete are parents: a bare invocation (no
// subcommand) is a usage error that prints usage to stderr and exits
// non-zero, rather than treating an arg as an event.
func TestCLIMutationParentsNoSubcommand(t *testing.T) {
	for _, parent := range []string{"create", "update", "delete"} {
		out, errOut, err := runCLI(t, nil, parent)
		if !errors.Is(err, ErrReported) {
			t.Fatalf("%s: err = %v, want ErrReported", parent, err)
		}
		if out != "" {
			t.Errorf("%s: stdout should be empty; got:\n%s", parent, out)
		}
		if !strings.Contains(errOut, "event") {
			t.Errorf("%s usage missing event subcommand; got:\n%s", parent, errOut)
		}
	}
}

// TestCLIBareRootNoSubcommand verifies the bare root command is a usage
// error: usage to stderr, empty stdout, ErrReported.
func TestCLIBareRootNoSubcommand(t *testing.T) {
	out, errOut, err := runCLI(t, nil)
	if !errors.Is(err, ErrReported) {
		t.Fatalf("bare root: err = %v, want ErrReported", err)
	}
	if out != "" {
		t.Errorf("stdout should be empty; got:\n%s", out)
	}
	if !strings.Contains(errOut, "Usage:") {
		t.Errorf("bare root usage missing; got:\n%s", errOut)
	}
}

// TestCLILeafMissingArg verifies leaf commands that require a positional
// argument emit a usage error (Error: + usage on stderr, empty stdout,
// ErrReported) when invoked without it.
func TestCLILeafMissingArg(t *testing.T) {
	for _, args := range [][]string{
		{"get", "event"},
		{"create", "event"},
		{"update", "event"},
		{"delete", "event"},
		{"delete", "calendar"},
	} {
		name := strings.Join(args, " ")
		out, errOut, err := runCLI(t, nil, args...)
		if !errors.Is(err, ErrReported) {
			t.Errorf("%s: err = %v, want ErrReported", name, err)
			continue
		}
		if out != "" {
			t.Errorf("%s: stdout should be empty; got:\n%s", name, out)
		}
		if !strings.Contains(errOut, "Error:") || !strings.Contains(errOut, "Usage:") {
			t.Errorf("%s: stderr missing Error/Usage; got:\n%s", name, errOut)
		}
		// The conventional order is the Error line first, then usage.
		if i, j := strings.Index(errOut, "Error:"), strings.Index(errOut, "Usage:"); i >= j {
			t.Errorf("%s: want Error line before Usage block; got:\n%s", name, errOut)
		}
	}
}

// TestCLIHelpFlagSucceeds verifies an explicit --help prints to stdout and
// returns no error (exit 0), even for commands that require arguments.
func TestCLIHelpFlagSucceeds(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"get", "--help"},
		{"get", "event", "--help"},
		{"create", "event", "-h"},
	} {
		name := strings.Join(args, " ")
		out, _, err := runCLI(t, nil, args...)
		if err != nil {
			t.Errorf("%s: err = %v, want nil", name, err)
		}
		if !strings.Contains(out, "Usage:") {
			t.Errorf("%s: help missing from stdout; got:\n%s", name, out)
		}
	}
}

// TestCLIRuntimeErrorNoUsage verifies a genuine runtime error (here, the
// service failing to build) after successful arg validation is NOT treated
// as a usage error: no usage block, and the error is returned verbatim (not
// ErrReported) so main prints it.
func TestCLIRuntimeErrorNoUsage(t *testing.T) {
	boom := errors.New("service unavailable")
	factory := func() (*calsvc.Service, error) { return nil, boom }
	out, errOut, err := runCLI(t, factory, "get", "event", "evt")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if errors.Is(err, ErrReported) {
		t.Error("runtime error must not be ErrReported")
	}
	if strings.Contains(out, "Usage:") || strings.Contains(errOut, "Usage:") {
		t.Errorf("runtime error must not print usage; stdout=%q stderr=%q", out, errOut)
	}
}

// TestCLIUnknownSubcommand verifies an unknown subcommand under a parent is
// a usage error (usage printed, ErrReported).
func TestCLIUnknownSubcommand(t *testing.T) {
	out, errOut, err := runCLI(t, nil, "get", "bogus")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("err = %v, want ErrReported", err)
	}
	if out != "" {
		t.Errorf("stdout should be empty; got:\n%s", out)
	}
	if !strings.Contains(errOut, "Usage:") {
		t.Errorf("unknown subcommand usage missing; got:\n%s", errOut)
	}
}

func TestCLIDeleteCalendarRequiresYes(t *testing.T) {
	// Without --yes, the command resolves the target (dry run) and refuses,
	// naming the calendar (name, type, ID) it WOULD have deleted.
	factory := fakeAPIFactory(map[string]string{
		"/calendar/v1": `{"Calendars":[{"ID":"id-work","Type":0,"Members":[{"ID":"m1","Name":"Work","Color":"#112233"}]}]}`,
	})
	_, _, err := runCLI(t, factory, "delete", "calendar", "Work")
	if err == nil {
		t.Fatal("want refusal without --yes")
	}
	for _, want := range []string{"--yes", `"Work"`, "id-work", "normal"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q missing %q", err.Error(), want)
		}
	}
}

func TestCLIUpdateCalendarRejectsDefaultColor(t *testing.T) {
	// "default" color is rejected before any network call (a calendar has no
	// inheritable color), so the detached factory is never invoked.
	_, _, err := runCLI(t, detachedFactory, "update", "calendar", "Work", "--color", "default")
	if err == nil || !strings.Contains(err.Error(), "no inheritable default color") {
		t.Fatalf("want default-color rejection, got %v", err)
	}
}
