package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
)

// newService restores the authenticated service from the saved session,
// wiring human notices (e.g. "Using calendar: ...") to stderr.
func newService() (*calsvc.Service, error) {
	svc, err := calsvc.New(noCache)
	if err != nil {
		return nil, err
	}
	svc.Notify = func(msg string) { fmt.Fprintln(os.Stderr, msg) }
	return svc, nil
}

// outputJSON reports whether the global --output flag selected JSON.
func outputJSON() bool { return outputFormat == "json" }

// humanOut returns the stream for human-readable output: stderr when JSON
// output is active (stdout is reserved for the JSON document), stdout
// otherwise.
func humanOut() io.Writer {
	if outputJSON() {
		return os.Stderr
	}
	return os.Stdout
}

// colorEnabled reports whether ANSI color should be emitted: only for the
// human text view on a real terminal, with NO_COLOR unset and --no-color
// not given. JSON and ICS output is never colorized.
func colorEnabled() bool {
	if noColor || outputJSON() || os.Getenv("NO_COLOR") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// printJSON writes v as indented JSON to stdout.
func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
