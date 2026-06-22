package cli

import (
	"bytes"
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
)

// runCLI executes one command line in-process, capturing stdout/stderr. It uses
// a fresh command tree and restores every mutated package global on cleanup so
// tests don't leak state. A nil factory leaves serviceFactory untouched.
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

	// Fresh root tree avoids cobra retaining flag state; route through
	// executeRoot for the same usage-error handling as production.
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	err = executeRoot(root)
	return outBuf.String(), errBuf.String(), err
}
