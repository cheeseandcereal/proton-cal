// Package cli implements the proton-cal cobra commands.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// outputFormat is the global --output/-o flag: "text" (default) or "json".
// With "json", machine-readable JSON goes to stdout and human messages to
// stderr.
var outputFormat string

// noColor is the global --no-color flag: never emit ANSI color, even when
// stdout is a terminal.
var noColor bool

// noCache is the global --no-cache flag: bypass the on-disk bootstrap
// cache entirely (neither read nor written).
var noCache bool

// newRootCmd builds the full command tree. It is a function (not a package
// singleton) so tests can construct an isolated tree per invocation without
// inheriting cobra's retained flag state.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "proton-cal",
		Short:         "Proton Calendar CLI - read and write events via the Proton API",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			switch outputFormat {
			case "text", "json":
				return nil
			default:
				return fmt.Errorf("invalid --output %q (want text or json)", outputFormat)
			}
		},
	}
	root.PersistentFlags().StringVarP(&outputFormat, "output", "o", "text",
		"output format: text or json (json on stdout, human messages on stderr)")
	root.PersistentFlags().BoolVar(&noColor, "no-color", false,
		"disable ANSI color output (color is auto-disabled when not a terminal or NO_COLOR is set)")
	root.PersistentFlags().BoolVar(&noCache, "no-cache", false,
		"bypass the on-disk bootstrap cache (key material and calendar list)")
	root.AddCommand(
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
	return root
}

// Execute runs the root command.
func Execute() error {
	return newRootCmd().Execute()
}
