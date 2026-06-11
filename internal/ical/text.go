package ical

import (
	"strings"
	"unicode/utf8"
)

// EscapeText escapes a TEXT property value per RFC 5545 §3.3.11:
// backslash, semicolon and comma are escaped with a backslash, and
// newlines become the literal two-character sequence "\n".
//
// (The Python reference implementation did NOT escape TEXT values —
// that was a latent injection bug; the Go version fixes it.)
func EscapeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case ';':
			b.WriteString(`\;`)
		case ',':
			b.WriteString(`\,`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			// Bare CR (or the CR of a CRLF pair) is dropped; the LF that
			// follows, if any, is escaped above.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// unescapeText reverses EscapeText per RFC 5545 §3.3.11. Both \n and \N
// decode to a newline. A trailing lone backslash is kept verbatim
// (tolerant parsing; never an error).
func unescapeText(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch s[i] {
		case 'n', 'N':
			b.WriteByte('\n')
		case '\\', ';', ',':
			b.WriteByte(s[i])
		default:
			// Unknown escape: keep both characters verbatim.
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// maxLineOctets is the RFC 5545 §3.1 content line limit, excluding CRLF.
const maxLineOctets = 75

// FoldLine folds a single content line per RFC 5545 §3.1: lines longer
// than 75 octets are split into multiple lines joined by CRLF followed
// by a single space, never splitting a multi-byte UTF-8 rune. Lines at
// or under the limit are returned unchanged.
func FoldLine(line string) string {
	if len(line) <= maxLineOctets {
		return line
	}
	var b strings.Builder
	b.Grow(len(line) + 3*(len(line)/maxLineOctets+1))
	budget := maxLineOctets
	for len(line) > budget {
		cut := budget
		// Back up so we never split in the middle of a UTF-8 rune:
		// a cut point is valid only at a rune start boundary.
		for cut > 0 && !utf8.RuneStart(line[cut]) {
			cut--
		}
		if cut == 0 {
			// Degenerate (rune wider than the budget); emit it whole.
			_, size := utf8.DecodeRuneInString(line)
			cut = size
		}
		b.WriteString(line[:cut])
		b.WriteString("\r\n ")
		line = line[cut:]
		// Continuation lines start with a space, leaving 74 octets.
		budget = maxLineOctets - 1
	}
	b.WriteString(line)
	return b.String()
}

// unfoldLines splits raw iCalendar data into logical content lines,
// joining folded continuations (a CRLF or LF followed by a space or tab)
// per RFC 5545 §3.1. Accepts CRLF, LF and stray CR line endings.
func unfoldLines(data string) []string {
	raw := strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(raw))
	for _, l := range raw {
		l = strings.TrimSuffix(l, "\r")
		if len(l) > 0 && (l[0] == ' ' || l[0] == '\t') && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
			continue
		}
		if l == "" {
			continue
		}
		lines = append(lines, l)
	}
	return lines
}
