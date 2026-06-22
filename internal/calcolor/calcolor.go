// Package calcolor is the fixed Proton Calendar accent-color palette with
// friendly names. The Proton API rejects any color outside this palette
// ("Not a valid Proton color", code 2011), so a per-event color must be one
// of these (or a calendar's own color, which is itself drawn from the same
// palette). Sourced from the Proton web client's calendar accent colors and
// verified live against the API.
package calcolor

import (
	"fmt"
	"sort"
	"strings"
)

// color pairs a friendly name with its canonical uppercase hex.
type color struct {
	Name string
	Hex  string
}

// palette is the Proton accent palette (ACCENT_COLORS_MAP from the Proton web
// client, packages/shared/lib/colors.ts), in its display order. These are the
// only colors the Proton API accepts for an event ("Not a valid Proton color"
// otherwise).
var palette = []color{
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

// DefaultSentinel is the special color spec meaning "use the calendar's
// color" (Proton has no per-event "no color" state once one is set; reverting
// means setting the event color to the calendar's own color). Frontends
// detect it with IsDefault before calling Resolve.
const DefaultSentinel = "default"

// IsDefault reports whether spec is the "default" sentinel (case-insensitive).
func IsDefault(spec string) bool {
	return strings.EqualFold(strings.TrimSpace(spec), DefaultSentinel)
}

// Resolve turns a user color spec (a friendly name like "strawberry" or a hex
// like "#EC3E7C", case-insensitive) into the canonical uppercase hex. Unknown
// colors (and the "default" sentinel, which callers must handle separately)
// error with a hint listing the valid friendly names.
func Resolve(spec string) (string, error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return "", fmt.Errorf("empty color (valid colors: %s, or %q)", Names(), DefaultSentinel)
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
	return "", fmt.Errorf("invalid color %q; valid colors: %s, or %q for the calendar color", spec, Names(), DefaultSentinel)
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

// Names returns the friendly names as a comma-separated, sorted list (for
// error hints).
func Names() string {
	names := make([]string, 0, len(palette))
	for _, c := range palette {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
