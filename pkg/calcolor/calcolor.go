// Package calcolor is the fixed Proton Calendar accent-color palette with
// friendly names. The API rejects any color outside this palette ("Not a valid
// Proton color", code 2011), so a per-event color must be one of these (or a
// calendar's own color, itself drawn from the same palette). Sourced from the
// web client and verified live against the API.
package calcolor

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
)

// ErrInvalidColor wraps Resolve failures for an unknown or empty color spec.
// Callers detect it with errors.Is to render a richer valid-color hint.
var ErrInvalidColor = errors.New("invalid color")

// Color is a palette entry: a friendly Name and its canonical uppercase Hex.
type Color struct {
	Name string
	Hex  string
}

// palette is the Proton accent palette (web client ACCENT_COLORS_MAP) in display
// order; the only colors the API accepts ("Not a valid Proton color" otherwise).
var palette = []Color{
	{"purple", "#8080FF"},
	{"pink", "#DB60D6"},
	{"strawberry", "#EC3E7C"},
	{"carrot", "#F78400"},
	{"sahara", "#936D58"},
	{"enzian", "#5252CC"},
	{"plum", "#A839A4"},
	{"cerise", "#BA1E55"},
	{"copper", "#C44800"},
	{"soil", "#54473F"},
	{"slateblue", "#415DF0"},
	{"pacific", "#179FD9"},
	{"reef", "#1DA583"},
	{"fern", "#3CBB3A"},
	{"olive", "#B4A40E"},
	{"cobalt", "#273EB2"},
	{"ocean", "#0A77A6"},
	{"pine", "#0F735A"},
	{"forest", "#258723"},
	{"pickle", "#807304"},
}

// byName maps a lowercased friendly name to its hex.
var byName = func() map[string]string {
	m := make(map[string]string, len(palette))
	for _, c := range palette {
		m[c.Name] = c.Hex
	}
	return m
}()

// byHex maps an uppercased hex to its friendly name.
var byHex = func() map[string]string {
	m := make(map[string]string, len(palette))
	for _, c := range palette {
		m[c.Hex] = c.Name
	}
	return m
}()

// DefaultSentinel is the color spec meaning "use the calendar's color": Proton
// has no per-event "no color" state, so reverting sets the calendar's own color.
const DefaultSentinel = "default"

// IsDefault reports whether spec is the "default" sentinel (case-insensitive).
func IsDefault(spec string) bool {
	return strings.EqualFold(strings.TrimSpace(spec), DefaultSentinel)
}

// Resolve turns a friendly name or hex (case-insensitive) into canonical
// uppercase hex. Unknown colors and the "default" sentinel (handled separately
// by callers) error with a valid-names hint.
func Resolve(spec string) (string, error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return "", fmt.Errorf("%w: empty color (valid colors: %s, or %q)", ErrInvalidColor, Names(), DefaultSentinel)
	}
	if hex, ok := byName[strings.ToLower(s)]; ok {
		return hex, nil
	}
	hex := strings.ToUpper(s)
	if !strings.HasPrefix(hex, "#") {
		hex = "#" + hex
	}
	if _, ok := byHex[hex]; ok {
		return hex, nil
	}
	return "", fmt.Errorf("%w %q; valid colors: %s, or %q for the calendar color", ErrInvalidColor, spec, Names(), DefaultSentinel)
}

// Valid reports whether hex (canonical uppercase, "#RRGGBB") is in the palette.
func Valid(hex string) bool {
	_, ok := byHex[strings.ToUpper(hex)]
	return ok
}

// NameOf returns the friendly name for a hex, or "" when not in the palette.
func NameOf(hex string) string {
	return byHex[strings.ToUpper(hex)]
}

// Label renders a color for display: "strawberry (#EC3E7C)" when the hex is a
// known palette color, else just the hex (e.g. a custom calendar color).
func Label(hex string) string {
	if hex == "" {
		return ""
	}
	if name := NameOf(hex); name != "" {
		return name + " (" + strings.ToUpper(hex) + ")"
	}
	return hex
}

// Names returns the palette as a sorted comma-separated list with each name's
// hex in parentheses (for error hints), e.g. "carrot (#F78400), ...".
func Names() string {
	entries := make([]string, 0, len(palette))
	for _, c := range palette {
		entries = append(entries, c.Name+" ("+c.Hex+")")
	}
	sort.Strings(entries)
	return strings.Join(entries, ", ")
}

// RandomHex returns the canonical hex of a random palette color, for picking
// a calendar color when the user supplies none (mirrors the web client, which
// assigns a random accent color to a newly created calendar).
func RandomHex() string {
	return palette[rand.IntN(len(palette))].Hex
}

// Palette returns the valid colors sorted by friendly name (matching Names'
// order), so callers can render the valid-color list themselves.
func Palette() []Color {
	out := make([]Color, len(palette))
	copy(out, palette)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
