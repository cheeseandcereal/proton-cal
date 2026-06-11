// Command proton-cal is a CLI (and MCP server) for reading and writing
// Proton Calendar events via Proton's undocumented internal API.
package main

import (
	"fmt"
	"os"

	"github.com/cheeseandcereal/proton-cal/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
