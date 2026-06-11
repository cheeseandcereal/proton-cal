package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

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

// humanOut returns the stream for human-readable output: stderr when --json
// is active (stdout is reserved for the JSON document), stdout otherwise.
func humanOut() io.Writer {
	if jsonOutput {
		return os.Stderr
	}
	return os.Stdout
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
