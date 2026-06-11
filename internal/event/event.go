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
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal-go/internal/calendar"
	"github.com/cheeseandcereal/proton-cal-go/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal-go/internal/papi"
	"github.com/cheeseandcereal/proton-cal-go/internal/recurrence"
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

// Decrypt decrypts a raw event into an Event. Lenient: signature
// verification is skipped and an undecryptable part degrades to missing
// fields rather than failing the whole event (a hard error is returned only
// when nothing useful could be extracted... see implementation).
func Decrypt(raw *caltypes.RawEvent, calKR *crypto.KeyRing) (*Event, error) {
	return decryptImpl(raw, calKR)
}

// Query fetches raw events for a calendar with full pagination and
// client-side window filtering (the server ignores Start/End params).
// Recurring masters always survive the filter (they are expanded later).
// Sorted by StartTime.
func Query(ctx context.Context, client *papi.Client, calendarID string, start, end int64, tzName string) ([]*caltypes.RawEvent, error) {
	return queryImpl(ctx, client, calendarID, start, end, tzName)
}

// Get fetches a single raw event.
func Get(ctx context.Context, client *papi.Client, calendarID, eventID string) (*caltypes.RawEvent, error) {
	return getImpl(ctx, client, calendarID, eventID)
}

// GetByUID fetches all raw rows sharing an iCal UID (master + exceptions);
// the UID query param filters server-side (verified live).
func GetByUID(ctx context.Context, client *papi.Client, calendarID, uid string) ([]*caltypes.RawEvent, error) {
	return getByUIDImpl(ctx, client, calendarID, uid)
}

// Listed is one expanded occurrence with its decrypted event (or the
// decryption error for that row; one bad event never kills a listing).
type Listed struct {
	Occurrence recurrence.Occurrence
	Event      *Event // nil when Err != nil
	Err        error
}

// ListWindow queries, expands and decrypts all occurrences overlapping
// [start, end), deduplicating decryption per event row.
func ListWindow(ctx context.Context, client *papi.Client, calKR *crypto.KeyRing, calendarID string, start, end int64, tzName string) ([]Listed, error) {
	return listWindowImpl(ctx, client, calKR, calendarID, start, end, tzName)
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

// Create encrypts and creates an event via the sync endpoint. Returns the
// created raw event row echoed by the server.
func Create(ctx context.Context, client *papi.Client, access *calendar.Access, opts CreateOptions) (*caltypes.RawEvent, error) {
	return createImpl(ctx, client, access, opts)
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

// Update fetches, merges, re-encrypts (REUSING the event's existing session
// keys; no new key packets) and PUTs an event. Returns the updated raw
// event when the server echoes it (may be nil on success).
func Update(ctx context.Context, client *papi.Client, access *calendar.Access, eventID string, opts UpdateOptions) (*caltypes.RawEvent, error) {
	return updateImpl(ctx, client, access, eventID, opts)
}

// Delete deletes raw event rows by ID in a single sync call.
func Delete(ctx context.Context, client *papi.Client, calendarID string, eventIDs []string, memberID string) error {
	return deleteImpl(ctx, client, calendarID, eventIDs, memberID)
}

// ResolveSeries resolves a recurring series from any of its rows: returns
// the master and all same-UID rows. Errors when the event is not recurring.
func ResolveSeries(ctx context.Context, client *papi.Client, calendarID, eventID string) (master *caltypes.RawEvent, related []*caltypes.RawEvent, err error) {
	return resolveSeriesImpl(ctx, client, calendarID, eventID)
}

// DeleteSeriesExceptions deletes all exception rows of a series except
// keepEventID (used when a series-level change invalidates single edits).
// Returns the number of rows deleted.
func DeleteSeriesExceptions(ctx context.Context, client *papi.Client, calendarID, uid, memberID, keepEventID string) (int, error) {
	return deleteSeriesExceptionsImpl(ctx, client, calendarID, uid, memberID, keepEventID)
}

// DeleteResult describes what SmartDelete actually deleted.
type DeleteResult struct {
	Kind        string `json:"kind"` // "occurrence" | "series" | "event"
	RowsDeleted int    `json:"rows_deleted"`
}

// SmartDelete picks the right delete strategy for the addressed target:
//   - occurrenceTS != 0: delete that single occurrence (EXDATE on the
//     master; an existing exception row for it is deleted too).
//   - occurrenceTS == 0 and the row is an exception: delete just that
//     occurrence (EXDATE + row).
//   - master row: delete the whole series (master + all same-UID rows; the
//     server orphans exceptions otherwise - see RESEARCH.md).
//   - plain event: delete the row.
func SmartDelete(ctx context.Context, client *papi.Client, access *calendar.Access, eventID string, occurrenceTS int64) (*DeleteResult, error) {
	return smartDeleteImpl(ctx, client, access, eventID, occurrenceTS)
}

// UpdateOutcome describes what SmartUpdate did.
type UpdateOutcome struct {
	Updated           *caltypes.RawEvent `json:"-"`
	EditedOccurrence  bool               `json:"edited_occurrence"`
	RemovedExceptions int                `json:"removed_exceptions"`
}

// SmartUpdate picks the right update strategy for the addressed target:
//   - occurrenceTS != 0: edit ONE occurrence (update its existing exception
//     row, or create a fresh exception row seeded from the master with
//     SEQUENCE >= the master's). Recurrence options are rejected here.
//   - otherwise: update the event/series; when a significant change hits a
//     master, its now-invalid exception rows are deleted afterwards.
func SmartUpdate(ctx context.Context, client *papi.Client, access *calendar.Access, eventID string, opts UpdateOptions, occurrenceTS int64) (*UpdateOutcome, error) {
	return smartUpdateImpl(ctx, client, access, eventID, opts, occurrenceTS)
}
