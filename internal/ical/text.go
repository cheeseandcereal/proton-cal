package ical

import (
	"strings"
	"unicode/utf8"
)

// escapeText escapes a TEXT value per RFC 5545 §3.3.11 (backslash/semicolon/
// comma backslash-escaped, newline -> "\n"), preventing property injection.
func escapeText(s string) string {
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
			// Bare CR (or CRLF's CR) dropped; a following LF is escaped above.
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// unescapeText reverses escapeText per RFC 5545 §3.3.11 (\n and \N -> newline);
// a trailing lone backslash is kept verbatim (tolerant).
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

// foldLine folds a content line per RFC 5545 §3.1: over 75 octets, split on
// CRLF+space, never mid-rune; shorter lines returned unchanged.
func foldLine(line string) string {
	if len(line) <= maxLineOctets {
		return line
	}
	var b strings.Builder
	b.Grow(len(line) + 3*(len(line)/maxLineOctets+1))
	budget := maxLineOctets
	for len(line) > budget {
		cut := budget
		// Back up to a rune start so we never split mid-rune.
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

// unfoldLines splits raw data into logical lines, joining folded
// continuations (CRLF/LF + space/tab) per RFC 5545 §3.1; accepts stray CR.
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
