package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"github.com/cheeseandcereal/proton-cal/pkg/calsvc"
)

// outWriter and errWriter are the package's output sinks (swapped by tests
// to capture output). All command rendering goes through these.
var (
	outWriter io.Writer = os.Stdout
	errWriter io.Writer = os.Stderr
)

// serviceFactory builds the authenticated service a command runs against;
// a package var so tests can inject one without touching the real session.
var serviceFactory = newService

// newService restores the authenticated service from the saved session,
// wiring human notices (e.g. "Using calendar: ...") to errWriter.
func newService() (*calsvc.Service, error) {
	svc, err := calsvc.New(noCache)
	if err != nil {
		return nil, err
	}
	svc.Notify = func(msg string) { fmt.Fprintln(errWriter, msg) }
	return svc, nil
}

// outputJSON reports whether the global --output flag selected JSON.
func outputJSON() bool { return outputFormat == "json" }

// humanOut returns the human-readable output stream: errWriter under JSON
// (outWriter is reserved for the JSON document), otherwise outWriter.
func humanOut() io.Writer {
	if outputJSON() {
		return errWriter
	}
	return outWriter
}

// colorEnabled reports whether ANSI color should be emitted: only the human
// text view on a real terminal, with NO_COLOR unset and --no-color not given.
func colorEnabled() bool {
	if noColor || outputJSON() || os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := outWriter.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// printJSON writes v as indented JSON to outWriter.
func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(outWriter, string(data))
	return nil
}
