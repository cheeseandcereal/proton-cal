package ical

import (
	"maps"
	"slices"
	"strings"
	"time"
)

// CardPatch describes property-level edits applied to a single decrypted
// VEVENT card, preserving every other line verbatim. It is the basis for
// faithful updates: instead of rebuilding a card from a known set of fields
// (which silently drops conferencing, attendees, organizer and any
// third-party properties), the original card text is kept and only the named
// properties are changed.
type CardPatch struct {
	// Set replaces (or inserts, when absent) a single-valued property. The
	// key is the upper-case property NAME; the value is the full logical
	// content line WITHOUT the name (i.e. everything after "NAME"), e.g.
	// ":new value" or ";TZID=...:20260101T090000". An empty/zero entry is
	// invalid; use Delete to remove.
	Set map[string]string
	// Delete removes every line whose property name is listed (single- or
	// multi-valued). Applied before Set and Append.
	Delete map[string]bool
	// Append adds new logical lines verbatim (already including the property
	// name and value, unfolded), e.g. for additional EXDATEs. Exact-duplicate
	// lines already present in the card are skipped.
	Append []string
}

// PatchCard applies a CardPatch to a decrypted VEVENT card and returns the
// re-wrapped card text (VCALENDAR/VEVENT wrapper, CRLF separators, no
// VERSION/PRODID, no trailing CRLF - matching BuildFragments output, so the
// result can be re-signed/re-encrypted like a freshly built fragment).
//
// Property order is preserved: a Set on an existing property replaces it in
// place; a Set on an absent property is appended after the last existing
// top-level property. Nested components (e.g. VALARM) and all unlisted
// properties are kept verbatim.
func PatchCard(card string, patch CardPatch) string {
	lines := unfoldLines(card)

	// Partition into top-level property lines vs nested-component blocks,
	// preserving order. We rebuild only the property region.
	props := topLevelEventLines(lines)
	components := collectComponents(lines)

	existing := make(map[string]bool, len(props))
	out := make([]string, 0, len(props)+len(patch.Append))
	for _, line := range props {
		name, _, _, ok := splitContentLine(line)
		if !ok {
			out = append(out, line)
			continue
		}
		if patch.Delete[name] {
			continue
		}
		if repl, set := patch.Set[name]; set {
			out = append(out, name+repl)
			existing[name] = true
			continue
		}
		existing[name] = true
		out = append(out, line)
	}

	// Insert Set entries for properties that were not already present, in a
	// stable order (sorted by name) so output is deterministic.
	for _, name := range slices.Sorted(maps.Keys(patch.Set)) {
		if existing[name] || patch.Delete[name] {
			continue
		}
		out = append(out, name+patch.Set[name])
	}

	// Append new multi-valued lines, skipping exact duplicates.
	present := make(map[string]bool, len(out))
	for _, l := range out {
		present[l] = true
	}
	for _, l := range patch.Append {
		if present[l] {
			continue
		}
		present[l] = true
		out = append(out, l)
	}

	wrapped := make([]string, 0, len(out)+len(components)+4)
	wrapped = append(wrapped, "BEGIN:VCALENDAR", "BEGIN:VEVENT")
	for _, l := range out {
		wrapped = append(wrapped, foldLine(l))
	}
	wrapped = append(wrapped, components...)
	wrapped = append(wrapped, "END:VEVENT", "END:VCALENDAR")
	return strings.Join(wrapped, "\r\n")
}

// EscapeText exposes RFC 5545 TEXT escaping for callers building Set values
// for TEXT properties (SUMMARY/DESCRIPTION/LOCATION).
func EscapeText(s string) string { return escapeText(s) }

// DateValue formats a date(-time) property body (everything after the
// property name) for the given zone/all-day form, e.g. ";TZID=...:2026..." or
// ":20260101T090000Z". It mirrors the forms BuildFragments emits.
func DateValue(t time.Time, tzName string, allDay bool) (string, error) {
	line, err := dtProp("X", t, tzName, allDay)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(line, "X"), nil
}
