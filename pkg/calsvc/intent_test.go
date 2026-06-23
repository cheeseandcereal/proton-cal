package calsvc

import "testing"

// TestResolveUpdateReminders locks the four-state mapping that both frontends
// depend on: Inherit:true (revert to default -> null) vs List:nil (explicit
// none -> []) is load-bearing (see event/write.go) and must not be flattened.
func TestResolveUpdateReminders(t *testing.T) {
	tests := []struct {
		name     string
		mode     ReminderMode
		specs    []string
		wantNil  bool
		wantInh  bool
		wantList int
		wantErr  bool
	}{
		{name: "keep", mode: ReminderKeep, wantNil: true},
		{name: "inherit reverts to default", mode: ReminderInherit, wantInh: true},
		{name: "none is explicit empty", mode: ReminderNone, wantList: 0},
		{name: "custom parses specs", mode: ReminderCustom, specs: []string{"15m", "1h"}, wantList: 2},
		{name: "custom requires at least one", mode: ReminderCustom, wantErr: true},
		{name: "custom rejects bad spec", mode: ReminderCustom, specs: []string{"nope"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveUpdateReminders(tt.mode, tt.specs)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil (%+v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if got != nil {
					t.Fatalf("want nil (keep), got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("want non-nil update")
			}
			if got.Inherit != tt.wantInh {
				t.Errorf("Inherit = %v, want %v", got.Inherit, tt.wantInh)
			}
			if len(got.List) != tt.wantList {
				t.Errorf("List len = %d, want %d", len(got.List), tt.wantList)
			}
		})
	}
}

// TestResolveCreateReminders locks the create-side tri-state: inherit (nil,
// false) vs none (nil, true) vs custom (list, true).
func TestResolveCreateReminders(t *testing.T) {
	tests := []struct {
		name     string
		mode     ReminderMode
		specs    []string
		wantList int
		wantSet  bool
		wantErr  bool
	}{
		{name: "inherit", mode: ReminderInherit, wantSet: false},
		{name: "keep is inherit on create", mode: ReminderKeep, wantSet: false},
		{name: "none", mode: ReminderNone, wantSet: true},
		{name: "custom", mode: ReminderCustom, specs: []string{"15m", "1h"}, wantList: 2, wantSet: true},
		{name: "custom requires entries", mode: ReminderCustom, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			list, set, err := ResolveCreateReminders(tt.mode, tt.specs)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(list) != tt.wantList || set != tt.wantSet {
				t.Errorf("got (len %d, set %v), want (len %d, set %v)", len(list), set, tt.wantList, tt.wantSet)
			}
		})
	}
}

func TestResolveColorCreate(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		wantHex string
		wantErr bool
	}{
		{name: "empty inherits", spec: "", wantHex: ""},
		{name: "default inherits", spec: "default", wantHex: ""},
		{name: "name resolves", spec: "strawberry", wantHex: "#EC3E7C"},
		{name: "invalid", spec: "notacolor", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveColorCreate(tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.wantHex {
				t.Errorf("got %q, want %q", got, tt.wantHex)
			}
		})
	}
}

func TestResolveColorUpdate(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    ColorUpdate
		wantErr bool
	}{
		{name: "default reverts", spec: "default", want: ColorUpdate{Inherit: true}},
		{name: "name resolves", spec: "strawberry", want: ColorUpdate{Hex: "#EC3E7C"}},
		{name: "invalid", spec: "notacolor", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveColorUpdate(tt.spec)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil || *got != tt.want {
				t.Errorf("got %+v, want %+v", got, &tt.want)
			}
		})
	}
}

func TestParseReminderMode(t *testing.T) {
	cases := map[string]ReminderMode{
		"":        ReminderKeep,
		"keep":    ReminderKeep,
		"inherit": ReminderInherit,
		"none":    ReminderNone,
		"custom":  ReminderCustom,
	}
	for in, want := range cases {
		got, err := ParseReminderMode(in)
		if err != nil || got != want {
			t.Errorf("ParseReminderMode(%q) = (%v, %v), want (%v, nil)", in, got, err, want)
		}
	}
	if _, err := ParseReminderMode("bogus"); err == nil {
		t.Error("ParseReminderMode(bogus) should error")
	}
}
