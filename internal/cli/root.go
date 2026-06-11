// Package cli implements the proton-cal cobra commands.
package cli

import (
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:           "proton-cal",
	Short:         "Proton Calendar CLI - read and write events via the Proton API",
	SilenceUsage:  true,
	SilenceErrors: true,
}

// jsonOutput is the global --json flag: machine-readable JSON on stdout,
// human extras on stderr.
var jsonOutput bool

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false,
		"machine-readable JSON output on stdout (human messages go to stderr)")
	rootCmd.AddCommand(
		newLoginCmd(),
		newLogoutCmd(),
		newCalendarsCmd(),
		newEventsCmd(),
		newCreateCmd(),
		newUpdateCmd(),
		newDeleteCmd(),
		newMCPCmd(),
	)
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}
