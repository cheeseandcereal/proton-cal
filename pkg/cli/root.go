// Package cli implements the proton-cal cobra commands.
package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cheeseandcereal/proton-cal/pkg/mcpserver"
)

// version is the build version, stamped via
// -ldflags "-X github.com/cheeseandcereal/proton-cal/pkg/cli.version=..." by
// the Makefile and goreleaser; "dev" for a plain `go build`.
var version = "dev"

// ErrReported signals an error already written to stderr by the CLI; the
// caller must not re-print it, only exit non-zero.
var ErrReported = errors.New("error already reported")

// errMissingSubcommand is returned by a parent command invoked without a
// subcommand; treated as a usage error (prints usage, exits non-zero).
var errMissingSubcommand = errors.New("a subcommand is required")

// usageError tags a failure that should print the command's usage (e.g. a bad
// positional arg); runtime errors stay untagged to avoid a usage dump.
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// requireArgs wraps a cobra positional-args validator so any failure is tagged
// as a usageError, routing argument-shape problems to the usage-printing path.
func requireArgs(v cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := v(cmd, args); err != nil {
			return &usageError{err: err}
		}
		return nil
	}
}

// isUsageError reports whether err should trigger printing the command's
// usage block (a tagged arg-validation failure or a missing subcommand).
func isUsageError(err error) bool {
	var ue *usageError
	return errors.As(err, &ue) || errors.Is(err, errMissingSubcommand)
}

// outputFormat is the global --output/-o flag: "text" (default) or "json"
// (JSON to stdout, human messages to stderr).
var outputFormat string

// noColor is the global --no-color flag: never emit ANSI color, even when
// stdout is a terminal.
var noColor bool

// noCache is the global --no-cache flag: bypass the on-disk bootstrap
// cache entirely (neither read nor written).
var noCache bool

// newRootCmd builds the full command tree; a function (not a singleton) so
// tests get an isolated tree without cobra's retained flag state.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "proton-cal",
		Short:         "Proton Calendar CLI - read and write events via the Proton API",
		Version:       version,
		Args:          requireArgs(cobra.NoArgs),
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
		RunE: func(_ *cobra.Command, _ []string) error {
			return errMissingSubcommand
		},
	}
	root.SetVersionTemplate("proton-cal {{.Version}}\n")
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
	// Keep the MCP server's advertised version in lockstep with the CLI build
	// version (single source of truth stamped via ldflags).
	mcpserver.Version = version
	return executeRoot(newRootCmd())
}

// executeRoot runs the command tree and centralizes usage-error handling. On
// a usage error it prints "Error: ..." plus the command's usage to stderr and
// returns ErrReported; runtime errors and --help pass through untouched.
func executeRoot(root *cobra.Command) error {
	cmd, err := root.ExecuteC()
	if err != nil && isUsageError(err) {
		w := cmd.ErrOrStderr()
		fmt.Fprintln(w, "Error:", err.Error())
		fmt.Fprint(w, cmd.UsageString())
		return ErrReported
	}
	// On a terminal, enrich an invalid-color error with per-color swatches
	// (no-op otherwise); the caller still prints it.
	return swatchedColorError(err)
}
