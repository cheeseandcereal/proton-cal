// Package reminders parses user-facing reminder specifications into the
// caltypes.Notification wire form. The CLI and MCP frontends share this
// parser; the tri-state assembly (keep / inherit / none / custom) stays in
// each frontend, which has the flag/argument context to decide it.
package reminders

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
)

// Notification type values (NOTIFICATION_TYPE_API in the web client).
const (
	typeEmail  = 0
	typeNotify = 1
)

// MaxReminders caps reminders per event: Proton rejects large lists, so this
// errors first. Conservative pending a live-verified exact limit.
const MaxReminders = 10

// Parse turns one user reminder spec into a caltypes.Notification. Accepts a
// shorthand offset (e.g. "1h30m", before start), an optional "notify:"/"email:"
// prefix, or a raw iCal trigger verbatim ("-PT15M", must start with "-P").
// For all-day events triggers measure from midnight start (so "1d" -> "-P1D").
func Parse(spec string) (caltypes.Notification, error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return caltypes.Notification{}, errors.New("empty reminder")
	}

	typ := typeNotify
	if i := strings.IndexByte(s, ':'); i >= 0 {
		switch strings.ToLower(strings.TrimSpace(s[:i])) {
		case "email", "mail", "e":
			typ = typeEmail
		case "notify", "display", "popup", "n":
			typ = typeNotify
		default:
			return caltypes.Notification{}, fmt.Errorf("unknown reminder type %q (use notify: or email:)", s[:i])
		}
		s = strings.TrimSpace(s[i+1:])
		if s == "" {
			return caltypes.Notification{}, errors.New("missing reminder offset after type prefix")
		}
	}

	trigger, err := parseTrigger(s)
	if err != nil {
		return caltypes.Notification{}, err
	}
	return caltypes.Notification{Type: typ, Trigger: trigger}, nil
}

// ParseList parses and validates reminder specs, enforcing the per-event cap.
// Empty input yields nil (callers decide "none" vs "inherit").
func ParseList(specs []string) ([]caltypes.Notification, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	if len(specs) > MaxReminders {
		return nil, fmt.Errorf("too many reminders: %d (max %d)", len(specs), MaxReminders)
	}
	out := make([]caltypes.Notification, 0, len(specs))
	for _, spec := range specs {
		n, err := Parse(spec)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// parseTrigger converts an offset spec to an iCal duration trigger string
// (always a negative offset, i.e. before the event start).
func parseTrigger(s string) (string, error) {
	// Raw iCal duration passthrough.
	if len(s) >= 2 {
		switch s[0] {
		case '-':
			if isICalDuration(s[1:]) {
				return s, nil
			}
		case '+':
			return "", errors.New("reminders fire before the event; use a plain offset like 15m or a negative trigger like -PT15M")
		}
	}

	d, err := parseShorthand(s)
	if err != nil {
		return "", err
	}
	if d <= 0 {
		return "", fmt.Errorf("reminder offset must be positive (got %q)", s)
	}
	return "-" + formatICalDuration(d), nil
}

// isICalDuration reports whether s (without sign) looks like an iCal duration
// such as "PT15M", "P1D", "P1DT2H", "P1W".
func isICalDuration(s string) bool {
	if len(s) < 2 || (s[0] != 'P' && s[0] != 'p') {
		return false
	}
	// Defer the strict shape to the server; we only sanity-check the prefix
	// and that the remainder is duration-ish (digits, T, and unit letters).
	for _, r := range s[1:] {
		switch {
		case r >= '0' && r <= '9':
		case r == 'T' || r == 't':
		case r == 'W' || r == 'w' || r == 'D' || r == 'd' ||
			r == 'H' || r == 'h' || r == 'M' || r == 'm' || r == 'S' || r == 's':
		default:
			return false
		}
	}
	return true
}

// parseShorthand parses "15m", "1h30m", "2d", "1w" (combinations allowed)
// into a duration. Units: w(eeks), d(ays), h(ours), m(inutes), s(econds).
func parseShorthand(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, errors.New("empty reminder offset")
	}
	var total time.Duration
	num := 0
	haveNum := false
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
			num = num*10 + int(c-'0')
			haveNum = true
		case c == 'w' || c == 'd' || c == 'h' || c == 'm' || c == 's':
			if !haveNum {
				return 0, fmt.Errorf("invalid reminder offset %q (expected e.g. 15m, 1h, 2d)", s)
			}
			var unit time.Duration
			switch c {
			case 'w':
				unit = 7 * 24 * time.Hour
			case 'd':
				unit = 24 * time.Hour
			case 'h':
				unit = time.Hour
			case 'm':
				unit = time.Minute
			case 's':
				unit = time.Second
			}
			total += time.Duration(num) * unit
			num = 0
			haveNum = false
		default:
			return 0, fmt.Errorf("invalid reminder offset %q (expected e.g. 15m, 1h, 2d; or a raw trigger like -PT15M)", s)
		}
	}
	if haveNum {
		return 0, fmt.Errorf("reminder offset %q is missing a unit (w/d/h/m/s)", s)
	}
	return total, nil
}

// formatICalDuration renders a positive duration as an iCal duration without
// sign, e.g. 90*time.Minute -> "PT1H30M", 24h -> "P1D", 7*24h -> "P1W".
func formatICalDuration(d time.Duration) string {
	// Whole weeks render as P<n>W (no other components).
	if d%(7*24*time.Hour) == 0 {
		return fmt.Sprintf("P%dW", int(d/(7*24*time.Hour)))
	}
	days := int(d / (24 * time.Hour))
	rem := d % (24 * time.Hour)
	hours := int(rem / time.Hour)
	rem %= time.Hour
	mins := int(rem / time.Minute)
	rem %= time.Minute
	secs := int(rem / time.Second)

	var b strings.Builder
	b.WriteByte('P')
	if days > 0 {
		fmt.Fprintf(&b, "%dD", days)
	}
	if hours > 0 || mins > 0 || secs > 0 {
		b.WriteByte('T')
		if hours > 0 {
			fmt.Fprintf(&b, "%dH", hours)
		}
		if mins > 0 {
			fmt.Fprintf(&b, "%dM", mins)
		}
		if secs > 0 {
			fmt.Fprintf(&b, "%dS", secs)
		}
	}
	return b.String()
}
