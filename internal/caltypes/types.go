// Package caltypes defines plain data types for Proton Calendar API
// responses, shared across packages. It has no dependencies on other
// internal packages so that recurrence/event/calendar can all import it.
//
// These types intentionally do NOT come from go-proton-api: its calendar
// types are stale (e.g. the calendar list no longer carries Name/Color at
// the top level - verified live against the API).
package caltypes

// Calendar card types (CALENDAR_CARD_TYPE in the web client).
const (
	CardClear              = 0
	CardEncrypted          = 1
	CardSigned             = 2
	CardEncryptedAndSigned = 3
)

// EventPart is one content card of an event.
type EventPart struct {
	Type      int    `json:"Type"`
	Data      string `json:"Data"`
	Signature string `json:"Signature,omitempty"`
	Author    string `json:"Author,omitempty"`
	MemberID  string `json:"MemberID,omitempty"`
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

// Calendar is one entry of GET /calendar/v1. Display metadata
// (Name/Description/Color) lives on the per-user member entry (verified
// live; older API responses had them top-level, supported as fallback).
type Calendar struct {
	ID          string           `json:"ID"`
	Type        int              `json:"Type"` // 0 = normal, 1 = subscribed (2 observed: holidays)
	CreateTime  int64            `json:"CreateTime"`
	Members     []CalendarMember `json:"Members"`
	Name        string           `json:"Name,omitempty"`        // legacy top-level fallback
	Description string           `json:"Description,omitempty"` // legacy top-level fallback
	Color       string           `json:"Color,omitempty"`       // legacy top-level fallback
}

// CalendarMember is a member entry on a calendar (the per-user view).
type CalendarMember struct {
	ID          string `json:"ID"`
	AddressID   string `json:"AddressID"`
	CalendarID  string `json:"CalendarID"`
	Email       string `json:"Email"`
	Name        string `json:"Name"`
	Description string `json:"Description"`
	Color       string `json:"Color"`
	Display     int    `json:"Display"`
	Permissions int    `json:"Permissions"`
	Flags       int    `json:"Flags"`
}
