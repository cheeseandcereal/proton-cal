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
	"errors"
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

// ErrDecryptDegraded marks operations refused because the event's cards
// could not all be decrypted/parsed (wrong or stale calendar keys, or
// corrupt data). Callers holding cached key material should refresh it and
// retry; merging an update from a half-decrypted event would silently blank
// the unreadable fields.
var ErrDecryptDegraded = errors.New("event could not be fully decrypted")

// Event is a decrypted calendar event. Times are absolute instants;
// rendering (including any unix-timestamp encoding) is a frontend concern.
type Event struct {
	EventID    string
	UID        string
	CalendarID string

	Summary     string
	Description string
	Location    string
	Status      string    // CONFIRMED unless the calendar-signed card says otherwise
	Transp      string    // OPAQUE unless the calendar-signed card says otherwise
	Comment     string    // calendar-encrypted card COMMENT
	Created     time.Time // original creation time; zero when the card lacks CREATED

	Start, End    time.Time
	StartTimezone string
	EndTimezone   string
	AllDay        bool

	RRule        string
	RecurrenceID time.Time // zero = not a single-edit exception row
	Exdates      []time.Time

	// RawSharedSigned is the verbatim shared-signed fragment (kept for
	// diagnostics; SEQUENCE is parsed into Sequence).
	RawSharedSigned string
	Sequence        int

	// Enrichment fields (read-only display). Color and Notifications come
	// from the plaintext row; Organizer/Attendees/Conference are parsed from
	// the cards. IsOrganizer/MoreAttendees mirror the row flags.
	Color         string
	IsOrganizer   bool
	MoreAttendees bool
	Notifications []caltypes.Notification
	Organizer     *Person
	Attendees     []Attendee
	Conference    *Conference

	// conf* are scratch accumulators for conference data spread across the
	// shared signed (ID/provider) and shared encrypted (URL/host) cards;
	// assembleConference folds them into Conference after all cards merge.
	confID       string
	confProvider string
	confURL      string
	confHost     string

	// DecryptFailed reports that at least one card failed to decrypt or
	// parse (the rest of the event is still populated). Read paths render
	// what they can; write paths refuse to merge from such an event.
	DecryptFailed bool
}

// Person is an organizer/attendee identity.
type Person struct {
	Email string
	CN    string
}

// Attendee is a decrypted attendee with its live RSVP status. Email/CN/Role/
// PartStat/RSVP come from the encrypted ATTENDEE card; Status is the live
// API RSVP from the plaintext row (caltypes.AttendeeToken.Status), joined by
// token. Status is -1 when no matching row token was found.
type Attendee struct {
	Email    string
	CN       string
	Role     string
	PartStat string
	RSVP     string
	Token    string
	Status   int
}

// Conference is the event's video-conferencing data (Proton Meet/Zoom),
// reassembled from the shared signed (ID/provider) and shared encrypted
// (URL/host) cards. Provider follows VIDEO_CONFERENCE_PROVIDER (1 = Zoom,
// 2 = Meet). Password is parsed from the URL "#pwd-" fragment when present.
type Conference struct {
	Provider string
	ID       string
	URL      string
	Password string
	Host     string
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

// DeleteKind classifies what a SmartDelete deleted.
type DeleteKind string

// DeleteKind values.
const (
	DeletedOccurrence DeleteKind = "occurrence"
	DeletedSeries     DeleteKind = "series"
	DeletedEvent      DeleteKind = "event"
)

// DeleteResult describes what SmartDelete actually deleted.
type DeleteResult struct {
	Kind        DeleteKind `json:"kind"`
	RowsDeleted int        `json:"rows_deleted"`
}

// UpdateOutcome describes what SmartUpdate did.
type UpdateOutcome struct {
	Updated           *caltypes.RawEvent `json:"-"`
	EditedOccurrence  bool               `json:"edited_occurrence"`
	RemovedExceptions int                `json:"removed_exceptions"`
}
