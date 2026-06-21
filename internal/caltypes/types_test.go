package caltypes

import (
	"encoding/json"
	"testing"
)

func TestRawEventNotificationsTriState(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSet bool
		wantLen int
	}{
		{"absent", `{"ID":"e1"}`, false, 0},
		{"null", `{"ID":"e1","Notifications":null}`, false, 0},
		{"empty", `{"ID":"e1","Notifications":[]}`, true, 0},
		{"custom", `{"ID":"e1","Notifications":[{"Type":1,"Trigger":"-PT15M"}]}`, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw RawEvent
			if err := json.Unmarshal([]byte(tc.body), &raw); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if raw.NotificationsSet != tc.wantSet {
				t.Errorf("NotificationsSet = %v, want %v", raw.NotificationsSet, tc.wantSet)
			}
			if len(raw.Notifications) != tc.wantLen {
				t.Errorf("len(Notifications) = %d, want %d", len(raw.Notifications), tc.wantLen)
			}
		})
	}
}

// TestRawEventUnmarshalKeepsOtherFields guards against the custom
// UnmarshalJSON dropping ordinary fields.
func TestRawEventUnmarshalKeepsOtherFields(t *testing.T) {
	var raw RawEvent
	body := `{"ID":"e1","UID":"u1","Color":"#EC3E7C","FullDay":1,"Notifications":[{"Type":0,"Trigger":"-PT1H"}]}`
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if raw.ID != "e1" || raw.UID != "u1" || raw.Color != "#EC3E7C" || !raw.IsAllDay() {
		t.Errorf("fields not preserved: %+v", raw)
	}
	if !raw.NotificationsSet || len(raw.Notifications) != 1 || raw.Notifications[0].Type != 0 {
		t.Errorf("notifications = %+v set=%v", raw.Notifications, raw.NotificationsSet)
	}
}
