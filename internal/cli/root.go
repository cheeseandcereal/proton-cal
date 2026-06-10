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

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}
