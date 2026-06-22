package auth

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// Prompter abstracts interactive input so the login flow is testable.
type Prompter interface {
	// Prompt reads a line of visible input (username, TOTP code, pasted
	// token). The returned string has the trailing newline removed.
	Prompt(label string) (string, error)
	// PromptSecret reads a line of hidden input (passwords).
	PromptSecret(label string) (string, error)
	// Notify shows a user-facing progress/instruction message (on stderr).
	Notify(msg string)
}

// terminalPrompter is a Prompter over stdin/stderr, falling back to a visible
// read (with a warning) for hidden input when stdin is not a terminal.
type terminalPrompter struct {
	in *bufio.Reader
}

// NewTerminalPrompter returns a Prompter over stdin/stderr (hidden input via
// golang.org/x/term, with a visible fallback when stdin is not a terminal).
func NewTerminalPrompter() Prompter {
	return &terminalPrompter{in: bufio.NewReader(os.Stdin)}
}

func (p *terminalPrompter) Prompt(label string) (string, error) {
	fmt.Fprintf(os.Stderr, "%s: ", label)
	line, err := p.in.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (p *terminalPrompter) PromptSecret(label string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		p.Notify("warning: stdin is not a terminal; input will be echoed")
		return p.Prompt(label)
	}
	fmt.Fprintf(os.Stderr, "%s: ", label)
	secret, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading hidden input: %w", err)
	}
	return string(secret), nil
}

func (p *terminalPrompter) Notify(msg string) {
	fmt.Fprintln(os.Stderr, msg)
}
