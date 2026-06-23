// Package event implements Proton Calendar event operations: querying,
// decrypting, creating, updating and deleting events, including the
// recurrence orchestration (exception rows, EXDATEs, series cleanup).
//
// The write path encrypts via
// pkg/pgp, builds fragments via pkg/ical, and talks to the API
// through pkg/papi raw calls (go-proton-api has no calendar write
// support).
package event

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/recurrence"
)

// Now returns the current time; a package var so tests can pin it
// (DTSTAMP/CREATED derive from it).
var Now = time.Now

// NewUID generates an iCalendar UID for new events; a package var so tests can pin it.
var NewUID = func() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return hex.EncodeToString(b[:])
}

// ErrDecryptDegraded marks operations refused because not all cards could be
// decrypted/parsed (stale keys or corrupt data). Merging from a half-decrypted
// event would silently blank unreadable fields; refresh keys and retry.
var ErrDecryptDegraded = errors.New("event could not be fully decrypted")

// Event is a decrypted calendar event. Times are absolute instants; rendering
// is a frontend concern.
type Event struct {
	EventID    string
	UID        string
	CalendarID string

	Summary     string
	Description string
	Location    string

	Start, End    time.Time
	StartTimezone string
	AllDay        bool

	RRule        string
	RecurrenceID time.Time // zero = not a single-edit exception row
	Exdates      []time.Time

	// Sequence is the shared-signed SEQUENCE, bumped on update writes.
	Sequence int

	// Enrichment fields (read-only display): Color/Notifications from the row,
	// Organizer/Attendees/Conference from the cards; IsOrganizer/MoreAttendees mirror row flags.
	Color         string
	IsOrganizer   bool
	MoreAttendees bool
	// Notifications are the event's own reminders; NotificationsSet distinguishes
	// "explicitly none" (set, len 0) from "inherit default" (not set). See caltypes.RawEvent.
	Notifications    []caltypes.Notification
	NotificationsSet bool
	Organizer        *Person
	Attendees        []Attendee
	Conference       *Conference

	// conf* accumulate conference data spread across shared signed (ID/provider)
	// and shared encrypted (URL/host) cards; assembleConference folds them in.
	confID       string
	confProvider string
	confURL      string
	confHost     string

	// DecryptFailed reports at least one card failed to decrypt/parse. Read paths
	// render what they can; write paths refuse to merge from such an event.
	DecryptFailed bool
}

// Person is an organizer/attendee identity.
type Person struct {
	Email string
	CN    string
}

// Attendee is a decrypted attendee with its live RSVP status. Most fields come
// from the encrypted ATTENDEE card; Status is the row RSVP joined by token (-1 if no match).
type Attendee struct {
	Email    string
	CN       string
	Role     string
	PartStat string
	RSVP     string
	Token    string
	Status   int
}

// Conference is the event's video-conferencing data. Provider follows
// VIDEO_CONFERENCE_PROVIDER (1 = Zoom, 2 = Meet); Password parsed from URL "#pwd-".
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

// CreateOptions describes a new event. For all-day events End is the EXCLUSIVE
// iCal end (day after the last day); frontends do the inclusive→exclusive conversion.
type CreateOptions struct {
	Summary     string
	Description string
	Location    string
	Start       time.Time
	End         time.Time
	TZName      string // IANA zone for serialization; ""/"UTC" → Z form
	AllDay      bool
	RRule       string // verbatim RRULE value ("" = not recurring)

	// Reminders + RemindersSet drive the wire tri-state (per caltypes.RawEvent):
	// unset -> null (inherit default); set len 0 -> [] (none); set len>0 -> custom array.
	Reminders    []caltypes.Notification
	RemindersSet bool
	// Color is the per-event color override ("#RRGGBB"); "" = inherit (null).
	Color string

	// Exception-row fields (single-occurrence edits): UID must equal the master's,
	// RecurrenceID the original occurrence start, Sequence >= master SEQUENCE (server-enforced).
	UID          string
	RecurrenceID *time.Time
	Sequence     int
}

// RemindersUpdate updates reminders (nil *RemindersUpdate = keep current).
// Inherit reverts to the calendar default (null); else List is the new set (empty = none).
type RemindersUpdate struct {
	Inherit bool
	List    []caltypes.Notification
}

// ColorUpdate updates color (nil *ColorUpdate = keep current); Value is the
// canonical palette hex. Proton has no color "inherit" sentinel: calsvc resolves
// "revert to default" to the calendar's own color before constructing this.
type ColorUpdate struct {
	Value string
}

// UpdateOptions describes a partial update; nil pointers mean "keep current".
// SEQUENCE bumps only on significant changes (times, recurrence, exdates) per RFC 5546.
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
	// Reminders/Color: nil = keep current; non-nil overrides (see RemindersUpdate /
	// ColorUpdate). Neither is significant (no SEQUENCE bump).
	Reminders *RemindersUpdate
	Color     *ColorUpdate
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
