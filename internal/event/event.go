// Package event implements Proton Calendar event operations: querying,
// decrypting, creating, updating and deleting events, including the
// recurrence orchestration (exception rows, EXDATEs, series cleanup).
//
// The write path encrypts via
// internal/pgp, builds fragments via internal/ical, and talks to the API
// through internal/papi raw calls (go-proton-api has no calendar write
// support).
package event

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/recurrence"
)

// Now returns the current time; a package variable so tests can pin it
// (DTSTAMP/CREATED in fragments derive from it).
var Now = time.Now

// NewUID generates an iCalendar UID for new events; a package variable so
// tests can pin it.
var NewUID = func() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return hex.EncodeToString(b[:])
}

// Event is a decrypted calendar event.
type Event struct {
	EventID    string `json:"id"`
	UID        string `json:"uid"`
	CalendarID string `json:"calendar_id"`

	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"`
	Location    string `json:"location,omitempty"`
	Status      string `json:"status,omitempty"`

	StartTime     int64  `json:"start_ts"`
	EndTime       int64  `json:"end_ts"`
	StartTimezone string `json:"start_timezone,omitempty"`
	EndTimezone   string `json:"end_timezone,omitempty"`
	AllDay        bool   `json:"all_day"`

	RRule        string  `json:"rrule,omitempty"`
	RecurrenceID int64   `json:"recurrence_id,omitempty"`
	Exdates      []int64 `json:"exdates,omitempty"`

	// RawSharedSigned is the verbatim shared-signed fragment (SEQUENCE
	// extraction for updates).
	RawSharedSigned string `json:"-"`
	Sequence        int    `json:"-"`
}

// IsRecurring reports whether the event is a recurring series master.
func (e *Event) IsRecurring() bool { return e.RRule != "" }

// Listed is one expanded occurrence with its decrypted event.
type Listed struct {
	Occurrence recurrence.Occurrence
	Event      *Event
}

// CreateOptions describes a new event. For all-day events Start/End are
// dates and End is the EXCLUSIVE iCal end (day after the last day) -
// frontends are responsible for the inclusive→exclusive conversion.
type CreateOptions struct {
	Summary     string
	Description string
	Location    string
	Start       time.Time
	End         time.Time
	TZName      string // IANA zone for serialization; ""/"UTC" → Z form
	AllDay      bool
	RRule       string // verbatim RRULE value ("" = not recurring)

	// Exception-row fields (single-occurrence edits): UID must equal the
	// master's UID, RecurrenceID the original occurrence start, Sequence
	// >= the master's SEQUENCE (server-enforced).
	UID          string
	RecurrenceID *time.Time
	Sequence     int
}

// UpdateOptions describes a partial update; nil pointers mean "keep
// current". SEQUENCE is bumped only on significant changes (times,
// recurrence, exdates) per RFC 5546 - field-only edits keep it.
type UpdateOptions struct {
	Summary     *string
	Description *string
	Location    *string
	Start       *time.Time
	End         *time.Time
	TZName      string // "" = keep the event's stored timezone
	RRule       *string
	ClearRRule  bool
	AddExdates  []time.Time
}

// Significant reports whether the update carries time/recurrence changes
// (bumps SEQUENCE; on a master it also invalidates single-edit exceptions).
func (o UpdateOptions) Significant() bool {
	return o.Start != nil || o.End != nil || o.RRule != nil || o.ClearRRule || len(o.AddExdates) > 0
}

// DeleteResult describes what SmartDelete actually deleted.
type DeleteResult struct {
	Kind        string `json:"kind"` // "occurrence" | "series" | "event"
	RowsDeleted int    `json:"rows_deleted"`
}

// UpdateOutcome describes what SmartUpdate did.
type UpdateOutcome struct {
	Updated           *caltypes.RawEvent `json:"-"`
	EditedOccurrence  bool               `json:"edited_occurrence"`
	RemovedExceptions int                `json:"removed_exceptions"`
}
