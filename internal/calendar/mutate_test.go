package calendar

import (
	"context"
	"net/url"
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
)

// captureAPI records the last write request (method, path, body) and returns
// a canned error.
type captureAPI struct {
	method, path string
	body         any
	err          error
}

func (c *captureAPI) Get(context.Context, string, url.Values, any) error { return nil }

func (c *captureAPI) Put(_ context.Context, path string, body, _ any) error {
	c.method, c.path, c.body = "PUT", path, body
	return c.err
}

func (c *captureAPI) Post(_ context.Context, path string, body, _ any) error {
	c.method, c.path, c.body = "POST", path, body
	return c.err
}

func (c *captureAPI) Delete(_ context.Context, path string, _ any) error {
	c.method, c.path, c.body = "DELETE", path, nil
	return c.err
}

func strptr(s string) *string { return &s }
func intptr(i int) *int       { return &i }
func boolptr(b bool) *bool    { return &b }

func TestUpdateMemberBuildsPartialBody(t *testing.T) {
	api := &captureAPI{}
	err := UpdateMember(context.Background(), api, "cal1", "mem1", MemberPatch{
		Name:  strptr("New Name"),
		Color: strptr("#A839A4"),
	})
	if err != nil {
		t.Fatalf("UpdateMember: %v", err)
	}
	if api.method != "PUT" || api.path != "/calendar/v1/cal1/members/mem1" {
		t.Fatalf("got %s %s", api.method, api.path)
	}
	body, ok := api.body.(map[string]any)
	if !ok {
		t.Fatalf("body type %T", api.body)
	}
	if body["Name"] != "New Name" || body["Color"] != "#A839A4" {
		t.Errorf("body = %+v", body)
	}
	if _, present := body["Description"]; present {
		t.Error("Description must be omitted when not set")
	}
}

func TestUpdateMemberEmptyRejected(t *testing.T) {
	api := &captureAPI{}
	if err := UpdateMember(context.Background(), api, "cal1", "mem1", MemberPatch{}); err == nil {
		t.Fatal("want error for empty patch")
	}
	if api.method != "" {
		t.Error("empty patch must not hit the API")
	}
}

func TestUpdateMemberBadColorMapsFriendlyError(t *testing.T) {
	api := &captureAPI{err: &papi.Error{Status: 400, Code: codeNotValidProtonColor, Message: "Not a valid Proton color"}}
	err := UpdateMember(context.Background(), api, "cal1", "mem1", MemberPatch{Color: strptr("#123456")})
	if err == nil {
		t.Fatal("want error")
	}
	if got := err.Error(); !contains(got, "Proton palette color") {
		t.Errorf("error = %q, want palette-color hint", got)
	}
}

func TestUpdateSettingsBuildsPartialBody(t *testing.T) {
	api := &captureAPI{}
	err := UpdateSettings(context.Background(), api, "cal1", SettingsPatch{
		DefaultEventDuration: intptr(60),
		MakesUserBusy:        boolptr(false),
		PartDayNotifications: &[]caltypes.Notification{{Type: 1, Trigger: "-PT30M"}},
	})
	if err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	if api.path != "/calendar/v1/cal1/settings" {
		t.Fatalf("path = %s", api.path)
	}
	body := api.body.(map[string]any)
	if body["DefaultEventDuration"] != 60 {
		t.Errorf("duration = %v", body["DefaultEventDuration"])
	}
	if body["MakesUserBusy"] != 0 {
		t.Errorf("MakesUserBusy = %v (want 0)", body["MakesUserBusy"])
	}
	if _, present := body["DefaultFullDayNotifications"]; present {
		t.Error("full-day notifications must be omitted when not set")
	}
	pd, ok := body["DefaultPartDayNotifications"].([]map[string]any)
	if !ok || len(pd) != 1 || pd[0]["Trigger"] != "-PT30M" || pd[0]["Type"] != 1 {
		t.Errorf("part-day notifications = %+v", body["DefaultPartDayNotifications"])
	}
}

func TestUpdateSettingsClearNotificationsSendsEmptySlice(t *testing.T) {
	api := &captureAPI{}
	empty := []caltypes.Notification{}
	if err := UpdateSettings(context.Background(), api, "cal1", SettingsPatch{PartDayNotifications: &empty}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	body := api.body.(map[string]any)
	pd, ok := body["DefaultPartDayNotifications"].([]map[string]any)
	if !ok || pd == nil {
		t.Fatalf("cleared notifications must serialize as a non-nil empty slice, got %+v", body["DefaultPartDayNotifications"])
	}
	if len(pd) != 0 {
		t.Errorf("want empty slice, got %+v", pd)
	}
}

func TestUpdateSettingsEmptyRejected(t *testing.T) {
	api := &captureAPI{}
	if err := UpdateSettings(context.Background(), api, "cal1", SettingsPatch{}); err == nil {
		t.Fatal("want error for empty patch")
	}
	if api.method != "" {
		t.Error("empty patch must not hit the API")
	}
}

func TestDeleteCalendarRoutes(t *testing.T) {
	normal := &captureAPI{}
	if err := DeleteCalendar(context.Background(), normal, "cal1", false); err != nil {
		t.Fatalf("normal delete: %v", err)
	}
	if normal.method != "DELETE" || normal.path != "/calendar/v1/cal1" {
		t.Errorf("normal: %s %s", normal.method, normal.path)
	}

	managed := &captureAPI{}
	if err := DeleteCalendar(context.Background(), managed, "cal2", true); err != nil {
		t.Fatalf("managed delete: %v", err)
	}
	if managed.path != "/calendar/v1/cal2/managed" {
		t.Errorf("managed path = %s", managed.path)
	}
}

func TestSetDefaultCalendarID(t *testing.T) {
	api := &captureAPI{}
	if err := SetDefaultCalendarID(context.Background(), api, "cal9"); err != nil {
		t.Fatalf("SetDefaultCalendarID: %v", err)
	}
	if api.path != "/settings/calendar" {
		t.Fatalf("path = %s", api.path)
	}
	body := api.body.(map[string]any)
	if body["DefaultCalendarID"] != "cal9" {
		t.Errorf("body = %+v", body)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
