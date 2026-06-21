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

// noCache is the global --no-cache flag: bypass the on-disk bootstrap
// cache entirely (neither read nor written).
var noCache bool

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false,
		"machine-readable JSON output on stdout (human messages go to stderr)")
	rootCmd.PersistentFlags().BoolVar(&noCache, "no-cache", false,
		"bypass the on-disk bootstrap cache (key material and calendar list)")
	rootCmd.AddCommand(
		newLoginCmd(),
		newLogoutCmd(),
		newCalendarsCmd(),
		newEventsCmd(),
		newGetCmd(),
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
