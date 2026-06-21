package ical

import (
	"strings"
)

// MergeCard names one of the four decrypted fragments fed to MergeFragments.
// The order/identity matters only for SharedSigned, which wins ties on
// structural single-valued properties.
type MergeCard struct {
	// Shared reports whether this is the shared-signed card (the
	// authoritative source for structural props on a tie).
	SharedSigned bool
	// Data is the decrypted fragment text (a VCALENDAR/VEVENT wrapper).
	Data string
}

// structuralProps are the single-valued properties owned by the shared-signed
// card; on a duplicate across cards, the shared-signed card's value wins.
var structuralProps = map[string]bool{
	"UID":           true,
	"DTSTAMP":       true,
	"DTSTART":       true,
	"DTEND":         true,
	"RRULE":         true,
	"RECURRENCE-ID": true,
	"SEQUENCE":      true,
}

// multiValuedProps may legitimately appear more than once; they are unioned
// (exact-duplicate lines deduped) rather than overwritten.
var multiValuedProps = map[string]bool{
	"EXDATE":     true,
	"ATTENDEE":   true,
	"RDATE":      true,
	"CATEGORIES": true,
	"COMMENT":    true,
}

// strippedProps are dropped from the merged output. X-PM-SESSION-KEY is a
// durable decryption capability (the symmetric key for the event's encrypted
// content); it is never part of a normally-fetched event and is meaningless
// to a standard ICS consumer, so it must never leak into an export.
var strippedProps = map[string]bool{
	"X-PM-SESSION-KEY": true,
}

// MergeFragments reconstructs a single standards-complete VCALENDAR/VEVENT
// from the decrypted per-card fragments. It unions every property line
// (preserving unknown/third-party properties verbatim), resolves duplicate
// single-valued properties (shared-signed card wins structural props;
// first-seen wins otherwise), unions multi-valued properties as a set,
// strips X-PM-SESSION-KEY, and re-wraps with VERSION:2.0 and the given
// prodID. Nested components (e.g. VALARM) found inside any card are preserved
// verbatim.
//
// Property values are kept exactly as decrypted (TEXT escaping intact);
// content lines are folded for output.
func MergeFragments(prodID string, cards ...MergeCard) string {
	// Ordered list of merged top-level property lines (logical, unfolded),
	// plus an index so structural overrides can replace in place.
	var propOrder []string              // property NAME in first-seen order
	propLine := make(map[string]string) // NAME -> chosen full logical line
	seenExact := make(map[string]bool)  // exact logical line -> present (multi-valued dedup)
	var multi []string                  // multi-valued lines in first-seen order
	multiNames := make(map[string]bool) // NAME -> is multi-valued (for ordering)
	var components []string             // verbatim nested-component blocks

	for _, card := range cards {
		lines := unfoldLines(card.Data)
		for _, block := range collectComponents(lines) {
			components = append(components, block)
		}
		for _, line := range topLevelEventLines(lines) {
			name, _, _, ok := splitContentLine(line)
			if !ok {
				continue
			}
			if strippedProps[name] {
				continue
			}
			switch {
			case multiValuedProps[name]:
				if !seenExact[line] {
					seenExact[line] = true
					multi = append(multi, line)
					multiNames[name] = true
				}
			default:
				_, seen := propLine[name]
				switch {
				case !seen:
					propLine[name] = line
					propOrder = append(propOrder, name)
				case structuralProps[name] && card.SharedSigned:
					// Shared-signed card overrides an earlier value.
					propLine[name] = line
				}
			}
		}
	}

	out := make([]string, 0, len(propOrder)+len(multi)+len(components)+4)
	out = append(out, "BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:"+prodID, "BEGIN:VEVENT")
	for _, name := range propOrder {
		out = append(out, foldLine(propLine[name]))
	}
	for _, line := range multi {
		out = append(out, foldLine(line))
	}
	for _, block := range components {
		out = append(out, block)
	}
	out = append(out, "END:VEVENT", "END:VCALENDAR")
	return strings.Join(out, "\r\n")
}

// topLevelEventLines returns the property lines belonging directly to the
// VEVENT (i.e. not inside a nested component such as VALARM, and not the
// VCALENDAR/VEVENT boundary lines themselves). Input without any VEVENT is
// treated as a bare property list.
func topLevelEventLines(lines []string) []string {
	hasVEvent := false
	for _, l := range lines {
		if strings.EqualFold(strings.TrimSpace(l), "BEGIN:VEVENT") {
			hasVEvent = true
			break
		}
	}

	inEvent := !hasVEvent
	depth := 0 // nesting depth below the VEVENT (e.g. inside VALARM)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		name, _, value, ok := splitContentLine(line)
		if ok {
			switch name {
			case "BEGIN":
				if hasVEvent {
					if strings.EqualFold(value, "VEVENT") && !inEvent && depth == 0 {
						inEvent = true
					} else if inEvent {
						depth++
					}
				} else {
					depth++
				}
				continue
			case "END":
				if hasVEvent {
					if inEvent && depth > 0 {
						depth--
					} else if inEvent && strings.EqualFold(value, "VEVENT") {
						inEvent = false
					}
				} else if depth > 0 {
					depth--
				}
				continue
			}
		}
		if inEvent && depth == 0 {
			out = append(out, line)
		}
	}
	return out
}

// collectComponents returns verbatim, CRLF-joined nested-component blocks
// (e.g. VALARM) found directly inside the VEVENT. Each returned block is a
// self-contained BEGIN:.../END:... unit with folded lines. Cards in the
// normal read path carry none; this preserves any that do.
func collectComponents(lines []string) []string {
	var blocks []string
	var cur []string
	inEvent := false
	depth := 0
	for _, line := range lines {
		name, _, value, ok := splitContentLine(line)
		if !ok {
			if depth > 0 {
				cur = append(cur, foldLine(line))
			}
			continue
		}
		switch name {
		case "BEGIN":
			if strings.EqualFold(value, "VEVENT") && !inEvent && depth == 0 {
				inEvent = true
				continue
			}
			if inEvent {
				depth++
				cur = append(cur, foldLine(line))
				continue
			}
		case "END":
			if inEvent && strings.EqualFold(value, "VEVENT") && depth == 0 {
				inEvent = false
				continue
			}
			if depth > 0 {
				cur = append(cur, foldLine(line))
				depth--
				if depth == 0 {
					blocks = append(blocks, strings.Join(cur, "\r\n"))
					cur = nil
				}
				continue
			}
		default:
			if depth > 0 {
				cur = append(cur, foldLine(line))
			}
		}
	}
	return blocks
}
