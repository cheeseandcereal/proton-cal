// Package caltypes defines the plain event-row wire types shared by the
// recurrence and event packages. It has no dependencies on other internal
// packages, breaking the cycle recurrence -> event row types <- event.
//
// These types intentionally do NOT come from go-proton-api: its calendar
// types are stale (verified live against the API).
package caltypes

import "encoding/json"

// CardType is a calendar card type (CALENDAR_CARD_TYPE in the web client).
type CardType int

// Calendar card types.
const (
	CardClear              CardType = 0
	CardEncrypted          CardType = 1
	CardSigned             CardType = 2
	CardEncryptedAndSigned CardType = 3
)

// EventPart is one content card of an event.
type EventPart struct {
	Type      CardType `json:"Type"`
	Data      string   `json:"Data"`
	Signature string   `json:"Signature,omitempty"`
	Author    string   `json:"Author,omitempty"`
	MemberID  string   `json:"MemberID,omitempty"`
}

// IsEncrypted reports whether the part's data is PGP-encrypted.
func (p EventPart) IsEncrypted() bool {
	return p.Type == CardEncrypted || p.Type == CardEncryptedAndSigned
}

// RawEvent is a raw calendar event row as returned by the API
// (GET /calendar/v1/{calID}/events and .../events/{eventID}).
//
// Recurrence metadata is plaintext and denormalized by the server:
// RRule is the verbatim RRULE value, Exdates are unix timestamps,
// RecurrenceID is the unix timestamp of the original occurrence start
// (present only on single-edit "exception" rows).
type RawEvent struct {
	ID            string `json:"ID"`
	UID           string `json:"UID"`
	CalendarID    string `json:"CalendarID"`
	SharedEventID string `json:"SharedEventID,omitempty"`

	CreateTime   int64 `json:"CreateTime"`
	LastEditTime int64 `json:"LastEditTime"`

	StartTime     int64  `json:"StartTime"`
	StartTimezone string `json:"StartTimezone"`
	EndTime       int64  `json:"EndTime"`
	EndTimezone   string `json:"EndTimezone"`
	FullDay       int    `json:"FullDay"`

	RRule        string  `json:"RRule,omitempty"`
	RecurrenceID int64   `json:"RecurrenceID,omitempty"`
	Exdates      []int64 `json:"Exdates,omitempty"`

	Author      string `json:"Author,omitempty"`
	Permissions int    `json:"Permissions,omitempty"`

	// Color is the per-event color override (CSS hex, e.g. "#EC3E7C"), or
	// empty when the event uses the calendar's color. Plaintext row field,
	// not an iCal property.
	Color string `json:"Color,omitempty"`
	// IsOrganizer is 1 when this account organizes the event.
	IsOrganizer int `json:"IsOrganizer,omitempty"`

	// Notifications are the event's own reminders/alarms (plaintext row
	// field). The API distinguishes three states, which NotificationsSet
	// captures (set by UnmarshalJSON):
	//   - null/absent  -> NotificationsSet false: inherit the calendar's
	//     default reminders (DefaultPart/FullDayNotifications).
	//   - []           -> NotificationsSet true,  len 0: explicitly none.
	//   - [{...}]       -> NotificationsSet true, len>0: custom reminders.
	// The web client's getHasDefaultNotifications is exactly !Notifications.
	Notifications []Notification `json:"Notifications,omitempty"`
	// NotificationsSet reports whether Notifications was present and non-null
	// on the wire (distinguishing explicit-none [] from inherit null).
	NotificationsSet bool `json:"-"`

	// Attendees carries the per-attendee anonymized token and live RSVP
	// status. Attendee identities (email/name) live encrypted in the
	// AttendeesEvents card and join to these rows by Token.
	Attendees     []AttendeeToken `json:"Attendees,omitempty"`
	AttendeesInfo AttendeesInfo   `json:"AttendeesInfo,omitempty"`

	SharedKeyPacket   string `json:"SharedKeyPacket,omitempty"`
	CalendarKeyPacket string `json:"CalendarKeyPacket,omitempty"`

	SharedEvents    []EventPart `json:"SharedEvents,omitempty"`
	CalendarEvents  []EventPart `json:"CalendarEvents,omitempty"`
	AttendeesEvents []EventPart `json:"AttendeesEvents,omitempty"`
	PersonalEvents  []EventPart `json:"PersonalEvents,omitempty"`
}

// UnmarshalJSON decodes a RawEvent and records whether Notifications was
// present and non-null, so callers can distinguish "inherit calendar default"
// (null/absent) from "explicitly no reminders" ([]).
func (e *RawEvent) UnmarshalJSON(data []byte) error {
	type alias RawEvent // avoid recursion
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*e = RawEvent(a)

	// Re-inspect the raw Notifications token: a non-null value (an array,
	// possibly empty) means the reminders are explicitly set on the event.
	var probe struct {
		Notifications json.RawMessage `json:"Notifications"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	e.NotificationsSet = len(probe.Notifications) > 0 && string(probe.Notifications) != "null"
	return nil
}

// Notification is one reminder/alarm on an event. Type is the API alarm
// kind (0 = email, 1 = device/display); Trigger is an iCal duration offset
// relative to the event start (e.g. "-PT1H", "-PT15M").
type Notification struct {
	Type    int    `json:"Type"`
	Trigger string `json:"Trigger"`
}

// AttendeeToken is the plaintext per-attendee row: an opaque token (matching
// the encrypted ATTENDEE card's X-PM-TOKEN) and the live RSVP Status.
// Status is ATTENDEE_STATUS_API: 0 = needs-action, 1 = tentative,
// 2 = declined, 3 = accepted.
type AttendeeToken struct {
	ID         string `json:"ID"`
	Token      string `json:"Token"`
	Status     int    `json:"Status"`
	UpdateTime *int64 `json:"UpdateTime"`
}

// AttendeesInfo wraps the attendee list with the MoreAttendees flag
// (1 = the server truncated the list).
type AttendeesInfo struct {
	MoreAttendees int `json:"MoreAttendees"`
}

// IsAllDay reports whether this is a full-day (date) event.
func (e *RawEvent) IsAllDay() bool { return e.FullDay != 0 }

// IsMaster reports whether this row is a recurring series master.
func (e *RawEvent) IsMaster() bool { return e.RRule != "" && e.RecurrenceID == 0 }

// IsException reports whether this row is a single-edit exception row.
func (e *RawEvent) IsException() bool { return e.RecurrenceID != 0 }
