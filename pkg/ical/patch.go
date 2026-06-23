package ical

import (
	"maps"
	"slices"
	"strings"
	"time"
)

// CardPatch describes property-level edits to a decrypted VEVENT card,
// preserving every other line verbatim (so conferencing, attendees, organizer
// and third-party properties a rebuild-from-fields would drop survive).
type CardPatch struct {
	// Set replaces or inserts a single-valued property: key is the upper-case
	// NAME, value is the line after NAME, e.g. ":new value" or ";TZID=...:...".
	Set map[string]string
	// Delete removes every line whose property name is listed (single- or
	// multi-valued). Applied before Set and Append.
	Delete map[string]bool
	// Append adds verbatim logical lines (name+value, unfolded), e.g. extra
	// EXDATEs; exact duplicates already in the card are skipped.
	Append []string
}

// PatchCard applies a CardPatch and returns re-wrapped card text matching
// BuildFragments output (no VERSION/PRODID, no trailing CRLF), so it can be
// re-signed/encrypted. Property order is preserved (Set replaces in place or
// appends); nested components and unlisted properties are kept verbatim.
func PatchCard(card string, patch CardPatch) string {
	lines := unfoldLines(card)

	// Partition into top-level property lines vs nested-component blocks,
	// preserving order. We rebuild only the property region.
	props := eventContentLines(lines)
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

// DateValue formats a date(-time) property body (after the name) for the
// zone/all-day form, mirroring the forms BuildFragments emits.
func DateValue(t time.Time, tzName string, allDay bool) (string, error) {
	line, err := dtProp("X", t, tzName, allDay)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(line, "X"), nil
}
