package calsvc

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/config"
)

// recordingAPI serves canned GET bodies and records write calls (method, path,
// body), so update/delete routing and partial bodies can be asserted without a
// session. It satisfies papi.API.
type recordingAPI struct {
	bodies map[string]string // GET path -> JSON body
	puts   []recordedWrite
	dels   []string
}

type recordedWrite struct {
	path string
	body map[string]any
}

func (r *recordingAPI) Get(_ context.Context, path string, _ url.Values, out any) error {
	body := r.bodies[path]
	if body == "" {
		body = `{}`
	}
	return jsonUnmarshal(body, out)
}

func (r *recordingAPI) Put(_ context.Context, path string, body, _ any) error {
	m, _ := body.(map[string]any)
	r.puts = append(r.puts, recordedWrite{path: path, body: m})
	return nil
}

func (r *recordingAPI) Post(context.Context, string, any, any) error { return nil }

func (r *recordingAPI) Delete(_ context.Context, path string, _ any) error {
	r.dels = append(r.dels, path)
	return nil
}

func jsonUnmarshal(body string, out any) error {
	if out == nil {
		return nil
	}
	return (&fakeRawAPI{bodies: map[string]string{"x": body}}).Get(context.Background(), "x", nil, out)
}

// calListWithType builds a one-calendar GET /calendar/v1 body with a given type.
func calListWithType(id, name string, typ int) string {
	return `{"Calendars":[{"ID":"` + id + `","Type":` + itoa(typ) +
		`,"Members":[{"ID":"m-` + id + `","Name":"` + name + `","Color":"#112233"}]}]}`
}

func itoa(i int) string {
	switch i {
	case 0:
		return "0"
	case 1:
		return "1"
	case 2:
		return "2"
	}
	return "0"
}

func strp(s string) *string { return &s }

func TestUpdateCalendarRefusesNonNormal(t *testing.T) {
	api := &recordingAPI{bodies: map[string]string{
		"/calendar/v1": calListWithType("h1", "Holidays", 2),
	}}
	s := detachedWithAPI(config.Config{}, nil)
	s.api, s.freshAPI = api, api

	_, err := s.UpdateCalendar(context.Background(), UpdateCalendarInput{Selector: "h1", Name: strp("X")})
	if err == nil || !strings.Contains(err.Error(), "only owned (normal)") {
		t.Fatalf("want non-normal refusal, got %v", err)
	}
	if len(api.puts) != 0 {
		t.Error("must not write for a non-normal calendar")
	}
}

func TestUpdateCalendarRejectsDefaultColorSentinel(t *testing.T) {
	api := &recordingAPI{bodies: map[string]string{
		"/calendar/v1": calListWithType("c1", "Work", 0),
	}}
	s := detachedWithAPI(config.Config{}, nil)
	s.api, s.freshAPI = api, api

	for _, c := range []string{"", "default"} {
		_, err := s.UpdateCalendar(context.Background(), UpdateCalendarInput{Selector: "c1", Color: strp(c)})
		if err == nil || !strings.Contains(err.Error(), "no inheritable default color") {
			t.Errorf("color %q: want rejection, got %v", c, err)
		}
	}
	if len(api.puts) != 0 {
		t.Error("must not write on a rejected color")
	}
}

func TestUpdateCalendarOnlyChangedCalls(t *testing.T) {
	api := &recordingAPI{bodies: map[string]string{
		"/calendar/v1": calListWithType("c1", "Work", 0),
	}}
	s := detachedWithAPI(config.Config{}, nil)
	s.api, s.freshAPI = api, api

	// Only a settings change (duration): expect exactly one PUT, to settings.
	_, err := s.UpdateCalendar(context.Background(), UpdateCalendarInput{
		Selector:        "c1",
		DefaultDuration: intp(45),
	})
	if err != nil {
		t.Fatalf("UpdateCalendar: %v", err)
	}
	settingsPuts := 0
	memberPuts := 0
	for _, p := range api.puts {
		switch {
		case strings.HasSuffix(p.path, "/settings"):
			settingsPuts++
			if p.body["DefaultEventDuration"] != 45 {
				t.Errorf("settings body = %+v", p.body)
			}
		case strings.Contains(p.path, "/members/"):
			memberPuts++
		}
	}
	if settingsPuts != 1 || memberPuts != 0 {
		t.Errorf("want 1 settings PUT and 0 member PUTs, got settings=%d member=%d", settingsPuts, memberPuts)
	}
}

func TestUpdateCalendarMakeDefault(t *testing.T) {
	api := &recordingAPI{bodies: map[string]string{
		"/calendar/v1": calListWithType("c1", "Work", 0),
	}}
	s := detachedWithAPI(config.Config{}, nil)
	s.api, s.freshAPI = api, api

	_, err := s.UpdateCalendar(context.Background(), UpdateCalendarInput{Selector: "c1", MakeDefault: true})
	if err != nil {
		t.Fatalf("UpdateCalendar: %v", err)
	}
	found := false
	for _, p := range api.puts {
		if p.path == "/settings/calendar" && p.body["DefaultCalendarID"] == "c1" {
			found = true
		}
	}
	if !found {
		t.Errorf("make-default did not PUT /settings/calendar with the ID; puts=%+v", api.puts)
	}
}

func TestUpdateCalendarNothingToDo(t *testing.T) {
	s := detachedWithAPI(config.Config{}, nil)
	if _, err := s.UpdateCalendar(context.Background(), UpdateCalendarInput{Selector: "c1"}); err == nil {
		t.Fatal("want error for empty update")
	}
}

func TestDeleteCalendarManagedRoute(t *testing.T) {
	api := &recordingAPI{bodies: map[string]string{
		"/calendar/v1": calListWithType("h1", "Holidays", 2),
	}}
	s := detachedWithAPI(config.Config{}, nil)
	s.api, s.freshAPI = api, api

	if err := s.DeleteCalendar(context.Background(), DeleteCalendarInput{Selector: "h1"}); err != nil {
		t.Fatalf("managed delete: %v", err)
	}
	if len(api.dels) != 1 || api.dels[0] != "/calendar/v1/h1/managed" {
		t.Errorf("dels = %v", api.dels)
	}
}

func TestDeleteCalendarNormalRequiresPassword(t *testing.T) {
	api := &recordingAPI{bodies: map[string]string{
		"/calendar/v1": calListWithType("c1", "Work", 0),
	}}
	s := detachedWithAPI(config.Config{}, nil)
	s.api, s.freshAPI = api, api

	err := s.DeleteCalendar(context.Background(), DeleteCalendarInput{Selector: "c1"})
	if err == nil || !strings.Contains(err.Error(), "re-authentication") {
		t.Fatalf("want password-required error, got %v", err)
	}
	if len(api.dels) != 0 {
		t.Error("must not delete without the password")
	}
}

func TestDeleteCalendarSubscribedRefused(t *testing.T) {
	api := &recordingAPI{bodies: map[string]string{
		"/calendar/v1": calListWithType("s1", "Feeds", 1),
	}}
	s := detachedWithAPI(config.Config{}, nil)
	s.api, s.freshAPI = api, api

	err := s.DeleteCalendar(context.Background(), DeleteCalendarInput{Selector: "s1"})
	if err == nil || !strings.Contains(err.Error(), "subscribed") {
		t.Fatalf("want subscribed refusal, got %v", err)
	}
}

func intp(i int) *int { return &i }

func TestUpdateCalendarReminderSets(t *testing.T) {
	api := &recordingAPI{bodies: map[string]string{
		"/calendar/v1": calListWithType("c1", "Work", 0),
	}}
	s := detachedWithAPI(config.Config{}, nil)
	s.api, s.freshAPI = api, api

	part := []caltypes.Notification{{Type: 1, Trigger: "-PT30M"}}
	_, err := s.UpdateCalendar(context.Background(), UpdateCalendarInput{
		Selector:         "c1",
		PartDayReminders: &part,
	})
	if err != nil {
		t.Fatalf("UpdateCalendar: %v", err)
	}
	var settingsBody map[string]any
	for _, p := range api.puts {
		if strings.HasSuffix(p.path, "/settings") {
			settingsBody = p.body
		}
	}
	if settingsBody == nil {
		t.Fatal("no settings PUT recorded")
	}
	if _, ok := settingsBody["DefaultPartDayNotifications"]; !ok {
		t.Errorf("reminder set not sent: %+v", settingsBody)
	}
	if _, present := settingsBody["DefaultFullDayNotifications"]; present {
		t.Error("untouched full-day set must be omitted")
	}
}
