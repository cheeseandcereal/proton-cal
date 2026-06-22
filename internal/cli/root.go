// Package cli implements the proton-cal cobra commands.
package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

// ErrReported signals that an error has already been written to stderr by
// the CLI (e.g. a usage error: the "Error:" line plus the usage block).
// main wraps Execute and must not re-print the message for this sentinel;
// it should simply exit non-zero.
var ErrReported = errors.New("error already reported")

// errMissingSubcommand is returned by a parent (grouping) command's RunE
// when it is invoked without a subcommand. It is treated as a usage error,
// so the command's usage is printed and the process exits non-zero.
var errMissingSubcommand = errors.New("a subcommand is required")

// usageError tags a failure that should print the offending command's usage
// (e.g. a missing or malformed positional argument). Genuine runtime errors
// are left untagged so they do not trigger a usage dump.
type usageError struct{ err error }

func (e *usageError) Error() string { return e.err.Error() }
func (e *usageError) Unwrap() error { return e.err }

// requireArgs wraps a cobra positional-args validator so that any validation
// failure is tagged as a usageError. This is the single mechanism by which
// argument-shape problems (too few/too many/unknown subcommand) are routed
// to the usage-printing path.
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
	return executeRoot(newRootCmd())
}

// executeRoot runs the command tree and centralizes usage-error handling so
// production (Execute) and tests exercise the same path. On a usage error
// (missing/invalid positional args or a parent invoked with no subcommand),
// it prints the conventional "Error: ..." line followed by the offending
// command's usage block to that command's stderr, then returns ErrReported
// so the caller exits non-zero without re-printing. Genuine runtime errors
// and explicit --help are passed through untouched.
func executeRoot(root *cobra.Command) error {
	cmd, err := root.ExecuteC()
	if err != nil && isUsageError(err) {
		w := cmd.ErrOrStderr()
		fmt.Fprintln(w, "Error:", err.Error())
		fmt.Fprint(w, cmd.UsageString())
		return ErrReported
	}
	return err
}
