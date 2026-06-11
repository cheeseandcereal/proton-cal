package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

// newMCPCmd registers the MCP server command. It is a stub until the
// internal/mcpserver package lands; this file is intentionally the only
// place that wires it, so replacing the stub is a one-file change.
func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server (stdio) for AI tool integration",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return errors.New("MCP server not wired yet")
		},
	}
}

func init() {
	rootCmd.AddCommand(newMCPCmd())
}
