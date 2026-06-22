package calcolor

import (
	"strings"
	"testing"
)

func TestResolve(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"strawberry", "#EC3E7C"},
		{"Strawberry", "#EC3E7C"},
		{"  pine  ", "#0F735A"},
		{"#ec3e7c", "#EC3E7C"},
		{"#EC3E7C", "#EC3E7C"},
		{"ec3e7c", "#EC3E7C"}, // missing # tolerated
		{"#415DF0", "#415DF0"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := Resolve(tt.in)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("Resolve(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsDefault(t *testing.T) {
	for _, s := range []string{"default", "Default", "  DEFAULT  "} {
		if !IsDefault(s) {
			t.Errorf("IsDefault(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "strawberry", "#EC3E7C", "defaults"} {
		if IsDefault(s) {
			t.Errorf("IsDefault(%q) = true, want false", s)
		}
	}
	// "default" is not a palette color: Resolve must reject it (callers
	// handle the sentinel before calling Resolve).
	if _, err := Resolve("default"); err == nil {
		t.Error("Resolve(\"default\") should error; it is a sentinel, not a color")
	}
}

func TestResolveErrors(t *testing.T) {
	for _, in := range []string{"", "  ", "#00FF00", "chartreuse", "navy", "#123"} {
		_, err := Resolve(in)
		if err == nil {
			t.Errorf("Resolve(%q) should error", in)
		} else if !strings.Contains(err.Error(), "strawberry") {
			t.Errorf("Resolve(%q) error should hint friendly names: %v", in, err)
		}
	}
}

func TestValidAndNameOf(t *testing.T) {
	if !Valid("#EC3E7C") || !Valid("#ec3e7c") {
		t.Error("strawberry hex should be valid")
	}
	if Valid("#00FF00") {
		t.Error("#00FF00 should be invalid")
	}
	if NameOf("#0F735A") != "pine" {
		t.Errorf("NameOf pine hex = %q", NameOf("#0F735A"))
	}
	if NameOf("#00FF00") != "" {
		t.Error("unknown hex should have no name")
	}
}

func TestLabel(t *testing.T) {
	if got := Label("#EC3E7C"); got != "strawberry (#EC3E7C)" {
		t.Errorf("Label = %q", got)
	}
	// Unknown hex (e.g. a custom calendar color) renders bare.
	if got := Label("#123456"); got != "#123456" {
		t.Errorf("Label custom = %q", got)
	}
	if Label("") != "" {
		t.Error("empty label")
	}
}
