package cli

import (
	"bytes"
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
)

// runCLI executes one command line in-process against the given service
// factory, capturing stdout and stderr. It builds a fresh command tree per
// call and restores every mutated package global (writers, factory, and the
// persistent flag vars) on cleanup, so tests do not leak state into each
// other. A nil factory leaves serviceFactory untouched (for pure
// arg-parsing/validation cases that must fail before any service is built).
func runCLI(t *testing.T, factory func() (*calsvc.Service, error), args ...string) (stdout, stderr string, err error) {
	t.Helper()

	var outBuf, errBuf bytes.Buffer
	prevOut, prevErr := outWriter, errWriter
	prevFactory := serviceFactory
	prevFormat, prevColor, prevCache := outputFormat, noColor, noCache
	t.Cleanup(func() {
		outWriter, errWriter = prevOut, prevErr
		serviceFactory = prevFactory
		outputFormat, noColor, noCache = prevFormat, prevColor, prevCache
	})

	outWriter, errWriter = &outBuf, &errBuf
	if factory != nil {
		serviceFactory = factory
	}
	// Reset persistent flags to defaults; cobra re-parses them from args.
	outputFormat, noColor, noCache = "text", false, false

	// A fresh root tree avoids cobra retaining flag state across runs.
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	err = root.Execute()
	return outBuf.String(), errBuf.String(), err
}
