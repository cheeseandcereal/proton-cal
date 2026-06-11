package cli

import (
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
)

func TestCalTypeString(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{0, "normal"},
		{1, "subscribed"},
		{2, "holidays"},
		{7, "type 7"},
	}
	for _, tt := range tests {
		if got := calTypeString(tt.in); got != tt.want {
			t.Errorf("calTypeString(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsDefaultCalendar(t *testing.T) {
	cal := calendar.Info{ID: "cal-id-1", Name: "Personal"}
	tests := []struct {
		name     string
		selector string
		want     bool
	}{
		{name: "empty selector", selector: "", want: false},
		{name: "by ID", selector: "cal-id-1", want: true},
		{name: "by name case-insensitive", selector: "personal", want: true},
		{name: "no match", selector: "Work", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDefaultCalendar(cal, tt.selector); got != tt.want {
				t.Errorf("isDefaultCalendar(%+v, %q) = %v, want %v", cal, tt.selector, got, tt.want)
			}
		})
	}
}
