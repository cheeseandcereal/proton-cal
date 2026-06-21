package cli

import (
	"fmt"
	"strconv"
	"strings"
)

// swatch renders a small colored block approximating a #RRGGBB color using a
// 24-bit truecolor ANSI background, followed by a trailing space, e.g.
// "\x1b[48;2;236;62;124m  \x1b[0m". It returns "" when color is disabled or
// the value is not a valid #RRGGBB hex string, so callers can prefix it
// unconditionally.
func swatch(hex string) string {
	if !colorEnabled() {
		return ""
	}
	r, g, b, ok := parseHexColor(hex)
	if !ok {
		return ""
	}
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm  \x1b[0m ", r, g, b)
}

// parseHexColor parses a "#RRGGBB" string into its 8-bit components.
func parseHexColor(hex string) (r, g, b int, ok bool) {
	s := strings.TrimPrefix(hex, "#")
	if len(s) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return int(v>>16) & 0xff, int(v>>8) & 0xff, int(v) & 0xff, true
}
