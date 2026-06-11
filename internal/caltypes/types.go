// Package caltypes defines the plain event-row wire types shared by the
// recurrence and event packages. It has no dependencies on other internal
// packages, breaking the cycle recurrence -> event row types <- event.
//
// These types intentionally do NOT come from go-proton-api: its calendar
// types are stale (verified live against the API).
package caltypes

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

	SharedKeyPacket   string `json:"SharedKeyPacket,omitempty"`
	CalendarKeyPacket string `json:"CalendarKeyPacket,omitempty"`

	SharedEvents    []EventPart `json:"SharedEvents,omitempty"`
	CalendarEvents  []EventPart `json:"CalendarEvents,omitempty"`
	AttendeesEvents []EventPart `json:"AttendeesEvents,omitempty"`
	PersonalEvents  []EventPart `json:"PersonalEvents,omitempty"`
}

// IsAllDay reports whether this is a full-day (date) event.
func (e *RawEvent) IsAllDay() bool { return e.FullDay != 0 }

// IsMaster reports whether this row is a recurring series master.
func (e *RawEvent) IsMaster() bool { return e.RRule != "" && e.RecurrenceID == 0 }

// IsException reports whether this row is a single-edit exception row.
func (e *RawEvent) IsException() bool { return e.RecurrenceID != 0 }
