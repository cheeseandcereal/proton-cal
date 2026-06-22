package cli

import (
	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/internal/mcpserver"
)

// newMCPCmd registers the MCP server command, wired to internal/mcpserver.
func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server (stdio) for AI tool integration",
		Long: `Run the Model Context Protocol server.

The server speaks MCP (JSON-RPC) over stdio: stdout carries the protocol,
all diagnostics go to stderr. It exposes Proton Calendar tools
(list_calendars, list_events, create_event, update_event, delete_event)
so AI tools like Claude Code or opencode can read and write your calendar.

Requires a saved session: run ` + "`proton-cal login`" + ` first. The server
never prompts; without a session every tool call returns an error.

Example MCP client configuration (e.g. in an mcpServers section):

  {
    "mcpServers": {
      "proton-calendar": {
        "command": "proton-cal",
        "args": ["mcp"]
      }
    }
  }`,
		Args: requireArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return mcpserver.Run(cmd.Context())
		},
	}
}
