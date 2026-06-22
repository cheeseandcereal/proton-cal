package caljson

import (
	"reflect"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/recurrence"
)

func ts(year int, month time.Month, day, hour, minute int) int64 {
	return time.Date(year, month, day, hour, minute, 0, 0, time.UTC).Unix()
}

func listedTimed() event.Listed {
	raw := &caltypes.RawEvent{ID: "evt1"}
	return event.Listed{
		Occurrence: recurrence.Occurrence{
			Event: raw,
			Start: ts(2026, 6, 12, 9, 0),
			End:   ts(2026, 6, 12, 9, 30),
		},
		Event: &event.Event{
			EventID:     "evt1",
			UID:         "uid1",
			CalendarID:  "cal1",
			Summary:     "Standup",
			Description: "Weekly sync",
			Location:    "Zoom",
			Start:       time.Unix(ts(2026, 6, 12, 9, 0), 0).UTC(),
			End:         time.Unix(ts(2026, 6, 12, 9, 30), 0).UTC(),
		},
	}
}

func TestOccurrenceTimed(t *testing.T) {
	l := listedTimed()
	l.Occurrence.Event.RRule = "FREQ=DAILY"
	l.Event.RRule = "FREQ=DAILY"
	loc := time.FixedZone("UTC+2", 2*60*60)
	got := Occurrence(l, loc, calendar.Settings{}, calendar.Info{})
	want := Event{
		ID:                "evt1",
		UID:               "uid1",
		Summary:           "Standup",
		Description:       "Weekly sync",
		Location:          "Zoom",
		Start:             "2026-06-12T11:00:00+02:00",
		End:               "2026-06-12T11:30:00+02:00",
		AllDay:            false,
		Recurring:         true,
		EditedOccurrence:  false,
		OccurrenceStartTS: ts(2026, 6, 12, 9, 0),
		RRule:             "FREQ=DAILY",
		CalendarID:        "cal1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Occurrence() = %+v, want %+v", got, want)
	}
}

func TestOccurrenceAllDayUsesUTC(t *testing.T) {
	l := listedTimed()
	l.Event.AllDay = true
	l.Occurrence.Start = ts(2026, 6, 12, 0, 0)
	l.Occurrence.End = ts(2026, 6, 13, 0, 0)
	loc := time.FixedZone("UTC-7", -7*60*60)
	got := Occurrence(l, loc, calendar.Settings{}, calendar.Info{})
	if got.Start != "2026-06-12T00:00:00Z" || got.End != "2026-06-13T00:00:00Z" {
		t.Errorf("all-day start/end = %q / %q, want UTC-anchored dates", got.Start, got.End)
	}
	if !got.AllDay {
		t.Error("AllDay = false, want true")
	}
}

func TestOccurrenceEditedOccurrence(t *testing.T) {
	l := listedTimed()
	l.Occurrence.Event.RecurrenceID = ts(2026, 6, 12, 8, 0)
	l.Event.RecurrenceID = time.Unix(ts(2026, 6, 12, 8, 0), 0).UTC()
	got := Occurrence(l, time.UTC, calendar.Settings{}, calendar.Info{})
	if !got.EditedOccurrence || got.Recurring {
		t.Errorf("EditedOccurrence=%v Recurring=%v, want true/false", got.EditedOccurrence, got.Recurring)
	}
}

func TestEventDetailEnrichment(t *testing.T) {
	ev := &event.Event{
		EventID: "evt1", UID: "uid1", CalendarID: "cal1",
		Summary:     "Test Event",
		Location:    "Some Test Location",
		Start:       time.Unix(ts(2026, 6, 24, 8, 0), 0).UTC(),
		End:         time.Unix(ts(2026, 6, 24, 8, 30), 0).UTC(),
		Color:       "#EC3E7C",
		IsOrganizer: true,
		Organizer:   &event.Person{Email: "adam@adamcrowder.net", CN: "adam"},
		Attendees: []event.Attendee{
			{Email: "adacrowd@amazon.com", CN: "adacrowd", Role: "REQ-PARTICIPANT", Status: 3},
		},
		Conference: &event.Conference{
			Provider: "2", ID: "MQYTXG4HKC",
			URL: "https://meet.proton.me/join/id-MQYTXG4HKC#pwd-x", Password: "x",
		},
		Notifications:    []caltypes.Notification{{Type: 1, Trigger: "-PT1H"}},
		NotificationsSet: true,
	}

	j := EventDetail(ev, time.UTC, calendar.Settings{}, calendar.Info{})
	if j.Color != "#EC3E7C" || !j.IsOrganizer {
		t.Errorf("color/isOrganizer = %q/%v", j.Color, j.IsOrganizer)
	}
	if j.Organizer == nil || j.Organizer.Email != "adam@adamcrowder.net" {
		t.Errorf("organizer = %+v", j.Organizer)
	}
	if len(j.Attendees) != 1 || j.Attendees[0].Status != "accepted" {
		t.Errorf("attendees = %+v", j.Attendees)
	}
	if j.Conference == nil || j.Conference.Provider != "Proton Meet" {
		t.Errorf("conference = %+v", j.Conference)
	}
	if len(j.Notifications) != 1 || j.Notifications[0].Trigger != "-PT1H" {
		t.Errorf("notifications = %+v", j.Notifications)
	}
}

func TestEventDetailInheritsCalendarDefaults(t *testing.T) {
	set := calendar.Settings{
		DefaultPartDayNotifications: []caltypes.Notification{{Type: 1, Trigger: "-PT15M"}},
	}
	cal := calendar.Info{Color: "#415DF0"}
	// Event with no own reminders/color inherits the calendar defaults.
	ev := &event.Event{
		EventID: "e", Summary: "X",
		Start: time.Unix(ts(2026, 6, 24, 8, 0), 0).UTC(),
		End:   time.Unix(ts(2026, 6, 24, 8, 30), 0).UTC(),
	}
	j := EventDetail(ev, time.UTC, set, cal)
	if j.Color != "#415DF0" {
		t.Errorf("inherited color = %q, want #415DF0", j.Color)
	}
	if len(j.Notifications) != 1 || j.Notifications[0].Trigger != "-PT15M" {
		t.Errorf("inherited notifications = %+v", j.Notifications)
	}
}

func TestCalendarOf(t *testing.T) {
	c := calendar.Info{ID: "id1", Name: "Personal", Color: "#415DF0", Type: 0, Email: "a@b"}
	got := CalendarOf(c, true)
	if got.ID != "id1" || got.Name != "Personal" || got.Color != "#415DF0" || !got.IsDefault || got.Email != "a@b" {
		t.Errorf("CalendarOf = %+v", got)
	}
}
