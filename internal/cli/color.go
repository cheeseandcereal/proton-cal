package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/cheeseandcereal/proton-cal/internal/calcolor"
)

// swatchedColorError prefixes each valid color in a calcolor.ErrInvalidColor
// with a terminal swatch; other errors (and color-disabled) pass through.
func swatchedColorError(err error) error {
	if err == nil || !errors.Is(err, calcolor.ErrInvalidColor) || !colorEnabled() {
		return err
	}
	hint := validColorHint()
	// Swap calcolor.Names()'s plain list for the swatched rendering.
	msg := strings.Replace(err.Error(), calcolor.Names(), hint, 1)
	return errors.New(msg)
}

// validColorHint renders the valid palette colors as "<swatch>name (#HEX)",
// comma-separated, in the same order as calcolor.Names().
func validColorHint() string {
	pal := calcolor.Palette()
	parts := make([]string, 0, len(pal))
	for _, c := range pal {
		parts = append(parts, swatch(c.Hex)+c.Name+" ("+c.Hex+")")
	}
	return strings.Join(parts, ", ")
}

// swatch renders a #RRGGBB color as a 24-bit truecolor ANSI block plus a
// trailing space; returns "" when color is disabled or the hex is invalid.
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
