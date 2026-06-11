package event

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/ical"
	"github.com/cheeseandcereal/proton-cal/internal/icaltime"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
	"github.com/cheeseandcereal/proton-cal/internal/pgp"
)

var jsonNull = json.RawMessage("null")

// newEventBody assembles the common Event body:
// signed cards carry the plaintext fragment + detached signature, encrypted
// cards the base64 data packet + a signature over the PLAINTEXT.
func newEventBody(frags ical.Fragments, sharedSignedSig, sharedEncData, sharedEncSig, calSignedSig, calEncData, calEncSig string) *eventBody {
	return &eventBody{
		Permissions: 1,
		SharedEventContent: []caltypes.EventPart{
			{Type: caltypes.CardSigned, Data: frags.SharedSigned, Signature: sharedSignedSig},
			{Type: caltypes.CardEncryptedAndSigned, Data: sharedEncData, Signature: sharedEncSig},
		},
		CalendarEventContent: []caltypes.EventPart{
			{Type: caltypes.CardSigned, Data: frags.CalendarSigned, Signature: calSignedSig},
			{Type: caltypes.CardEncryptedAndSigned, Data: calEncData, Signature: calEncSig},
		},
		AttendeesEventContent: []caltypes.EventPart{},
		Attendees:             []struct{}{},
		Notifications:         jsonNull,
		Color:                 jsonNull,
	}
}

// Create encrypts and creates an event via the sync endpoint. Returns the
// created raw event row echoed by the server.
func Create(ctx context.Context, client papi.API, access *calendar.Access, opts CreateOptions) (*caltypes.RawEvent, error) {
	uid := opts.UID
	if uid == "" {
		uid = NewUID()
	}
	frags, err := ical.BuildFragments(ical.VEvent{
		UID:          uid,
		DTStamp:      Now().UTC(),
		Start:        &opts.Start,
		End:          &opts.End,
		TZName:       opts.TZName,
		AllDay:       opts.AllDay,
		Summary:      &opts.Summary,
		Description:  &opts.Description,
		Location:     &opts.Location,
		Sequence:     &opts.Sequence,
		RRule:        opts.RRule,
		RecurrenceID: opts.RecurrenceID,
	})
	if err != nil {
		return nil, fmt.Errorf("building event fragments: %w", err)
	}

	// Sign plaintext parts with the address key.
	sharedSignedSig, err := pgp.SignDetached(frags.SharedSigned, access.AddrKR)
	if err != nil {
		return nil, fmt.Errorf("signing shared part: %w", err)
	}
	calSignedSig, err := pgp.SignDetached(frags.CalendarSigned, access.AddrKR)
	if err != nil {
		return nil, fmt.Errorf("signing calendar part: %w", err)
	}

	// Encrypt+sign encrypted parts with fresh session keys encrypted to
	// the calendar public key.
	sharedKP, sharedData, sharedSig, err := pgp.EncryptAndSign(frags.SharedEncrypted, access.KR, access.AddrKR)
	if err != nil {
		return nil, fmt.Errorf("encrypting shared part: %w", err)
	}
	calKP, calData, calSig, err := pgp.EncryptAndSign(frags.CalendarEncrypted, access.KR, access.AddrKR)
	if err != nil {
		return nil, fmt.Errorf("encrypting calendar part: %w", err)
	}

	body := newEventBody(frags, sharedSignedSig, sharedData, sharedSig, calSignedSig, calData, calSig)
	body.SharedKeyPacket = sharedKP
	body.CalendarKeyPacket = calKP

	isImport := 0
	overwrite := 0
	resp, err := putSync(ctx, client, access.CalendarID, syncReq{
		MemberID: access.MemberID,
		IsImport: &isImport,
		Events:   []syncEventReq{{Overwrite: &overwrite, Event: body}},
	})
	if err != nil {
		return nil, err
	}
	created, err := resp.firstEvent()
	if err != nil {
		return nil, fmt.Errorf("creating event: %w", err)
	}
	return created, nil
}

// update fetches, merges, re-encrypts (REUSING the event's existing session
// keys; no new key packets) and PUTs an event. Returns the updated raw
// event when the server echoes it (may be nil on success).
func update(ctx context.Context, client papi.API, access *calendar.Access, eventID string, opts UpdateOptions) (*caltypes.RawEvent, error) {
	raw, err := Get(ctx, client, access.CalendarID, eventID)
	if err != nil {
		return nil, err
	}
	current, err := Decrypt(raw, access.KR)
	if err != nil {
		return nil, err
	}

	// SEQUENCE semantics (RFC 5546): bump only on date/time or recurrence
	// changes. Field-only edits keep the sequence - otherwise a master
	// update would leapfrog its exception rows, which the server rejects.
	sequence := current.Sequence
	if opts.Significant() {
		sequence++
	}

	// Times keep their stored zone unless explicitly re-zoned.
	tzEff := opts.TZName
	if tzEff == "" {
		tzEff = icaltime.OrUTC(current.StartTimezone)
	}

	// Recurrence: preserve unless replaced or cleared.
	var rrule string
	var exdates []time.Time
	if !opts.ClearRRule {
		rrule = current.RRule
		if opts.RRule != nil {
			rrule = *opts.RRule
		}
		exdates = append(exdates, current.Exdates...)
		exdates = append(exdates, opts.AddExdates...)
	}

	var recurrenceID *time.Time
	if !current.RecurrenceID.IsZero() {
		t := current.RecurrenceID
		recurrenceID = &t
	}

	start := current.Start
	if opts.Start != nil {
		start = *opts.Start
	}
	end := current.End
	if opts.End != nil {
		end = *opts.End
	}
	summary := strOr(opts.Summary, current.Summary)
	description := strOr(opts.Description, current.Description)
	location := strOr(opts.Location, current.Location)

	merged := ical.VEvent{
		UID:         current.UID,
		DTStamp:     Now().UTC(),
		Start:       &start,
		End:         &end,
		TZName:      tzEff,
		AllDay:      current.AllDay,
		Summary:     &summary,
		Description: &description,
		Location:    &location,
		// Preserve fields this tool does not edit: STATUS/TRANSP
		// (calendar-signed card), COMMENT (calendar-encrypted card) and
		// the original CREATED. Rebuilding the cards without them would
		// silently reset web-client-set values (TENTATIVE status,
		// free/transparent time blocks, ...).
		Status:       &current.Status,
		Transp:       &current.Transp,
		Comment:      &current.Comment,
		Sequence:     &sequence,
		RRule:        rrule,
		Exdates:      exdates,
		RecurrenceID: recurrenceID,
	}
	if !current.Created.IsZero() {
		created := current.Created
		merged.Created = &created
	}

	frags, err := ical.BuildFragments(merged)
	if err != nil {
		return nil, fmt.Errorf("building event fragments: %w", err)
	}

	// Reuse the event's existing session keys: the update payload carries
	// no key packets and the server keeps the originals.
	sharedSK, err := pgp.DecryptSessionKey(raw.SharedKeyPacket, access.KR)
	if err != nil {
		return nil, fmt.Errorf("extracting shared session key: %w", err)
	}
	calSK, err := pgp.DecryptSessionKey(raw.CalendarKeyPacket, access.KR)
	if err != nil {
		return nil, fmt.Errorf("extracting calendar session key: %w", err)
	}

	sharedSignedSig, err := pgp.SignDetached(frags.SharedSigned, access.AddrKR)
	if err != nil {
		return nil, fmt.Errorf("signing shared part: %w", err)
	}
	calSignedSig, err := pgp.SignDetached(frags.CalendarSigned, access.AddrKR)
	if err != nil {
		return nil, fmt.Errorf("signing calendar part: %w", err)
	}
	sharedData, sharedSig, err := pgp.EncryptWithSessionKeyAndSign(frags.SharedEncrypted, sharedSK, access.AddrKR)
	if err != nil {
		return nil, fmt.Errorf("re-encrypting shared part: %w", err)
	}
	calData, calSig, err := pgp.EncryptWithSessionKeyAndSign(frags.CalendarEncrypted, calSK, access.AddrKR)
	if err != nil {
		return nil, fmt.Errorf("re-encrypting calendar part: %w", err)
	}

	body := newEventBody(frags, sharedSignedSig, sharedData, sharedSig, calSignedSig, calData, calSig)

	resp, err := putSync(ctx, client, access.CalendarID, syncReq{
		MemberID: access.MemberID,
		Events:   []syncEventReq{{ID: eventID, Event: body}},
	})
	if err != nil {
		return nil, err
	}
	updated, err := resp.firstEvent()
	if err != nil {
		return nil, fmt.Errorf("updating event %s: %w", eventID, err)
	}
	return updated, nil
}
