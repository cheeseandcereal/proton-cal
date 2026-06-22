// Command proton-cal is a CLI (and MCP server) for reading and writing
// Proton Calendar events via Proton's undocumented internal API.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/cheeseandcereal/proton-cal/pkg/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		// Usage errors are already printed to stderr by the CLI; just exit.
		if !errors.Is(err, cli.ErrReported) {
			fmt.Fprintln(os.Stderr, "Error:", err)
		}
		os.Exit(1)
	}
}
