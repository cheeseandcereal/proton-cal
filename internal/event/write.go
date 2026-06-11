package event

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/ical"
	"github.com/cheeseandcereal/proton-cal/internal/icaltime"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
	"github.com/cheeseandcereal/proton-cal/internal/pgp"
)

var jsonNull = json.RawMessage("null")

// sealCards signs the two plaintext cards and encrypts+signs the two
// encrypted cards of an event, assembling the sync Event body.
//
// With nil session keys (creates) fresh session keys are generated and the
// body carries their key packets. With the event's existing session keys
// (updates) no key packets are emitted - the server keeps the originals.
func sealCards(frags ical.Fragments, addrKR, calKR *crypto.KeyRing, sharedSK, calSK *crypto.SessionKey) (*eventBody, error) {
	// Sign plaintext parts with the address key.
	sharedSignedSig, err := pgp.SignDetached(frags.SharedSigned, addrKR)
	if err != nil {
		return nil, fmt.Errorf("signing shared part: %w", err)
	}
	calSignedSig, err := pgp.SignDetached(frags.CalendarSigned, addrKR)
	if err != nil {
		return nil, fmt.Errorf("signing calendar part: %w", err)
	}

	var sharedKP, sharedData, sharedSig, calKP, calData, calSig string
	if sharedSK == nil {
		// Encrypt+sign encrypted parts with fresh session keys encrypted
		// to the calendar public key.
		sharedKP, sharedData, sharedSig, err = pgp.EncryptAndSign(frags.SharedEncrypted, calKR, addrKR)
		if err != nil {
			return nil, fmt.Errorf("encrypting shared part: %w", err)
		}
		calKP, calData, calSig, err = pgp.EncryptAndSign(frags.CalendarEncrypted, calKR, addrKR)
		if err != nil {
			return nil, fmt.Errorf("encrypting calendar part: %w", err)
		}
	} else {
		sharedData, sharedSig, err = pgp.EncryptWithSessionKeyAndSign(frags.SharedEncrypted, sharedSK, addrKR)
		if err != nil {
			return nil, fmt.Errorf("re-encrypting shared part: %w", err)
		}
		calData, calSig, err = pgp.EncryptWithSessionKeyAndSign(frags.CalendarEncrypted, calSK, addrKR)
		if err != nil {
			return nil, fmt.Errorf("re-encrypting calendar part: %w", err)
		}
	}

	return &eventBody{
		Permissions:       1,
		SharedKeyPacket:   sharedKP,
		CalendarKeyPacket: calKP,
		SharedEventContent: []caltypes.EventPart{
			{Type: caltypes.CardSigned, Data: frags.SharedSigned, Signature: sharedSignedSig},
			{Type: caltypes.CardEncryptedAndSigned, Data: sharedData, Signature: sharedSig},
		},
		CalendarEventContent: []caltypes.EventPart{
			{Type: caltypes.CardSigned, Data: frags.CalendarSigned, Signature: calSignedSig},
			{Type: caltypes.CardEncryptedAndSigned, Data: calData, Signature: calSig},
		},
		AttendeesEventContent: []caltypes.EventPart{},
		Attendees:             []struct{}{},
		Notifications:         jsonNull,
		Color:                 jsonNull,
	}, nil
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

	body, err := sealCards(frags, access.AddrKR, access.KR, nil, nil)
	if err != nil {
		return nil, err
	}

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

// mergeUpdate merges a partial update into the decrypted current state,
// producing the VEvent the cards are rebuilt from. Pure (no I/O).
//
// Semantics:
//   - SEQUENCE (RFC 5546): bumped only on date/time or recurrence changes.
//     Field-only edits keep it - otherwise a master update would leapfrog
//     its exception rows, which the server rejects.
//   - Times keep their stored zone unless explicitly re-zoned.
//   - Recurrence and EXDATEs are preserved unless replaced or cleared.
//   - STATUS/TRANSP/COMMENT/CREATED are preserved verbatim: this tool does
//     not edit them, and rebuilding the cards without them would silently
//     reset web-client-set values.
func mergeUpdate(current *Event, opts UpdateOptions, dtstamp time.Time) ical.VEvent {
	sequence := current.Sequence
	if opts.Significant() {
		sequence++
	}

	tzEff := opts.TZName
	if tzEff == "" {
		tzEff = icaltime.OrUTC(current.StartTimezone)
	}

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
	status := current.Status
	transp := current.Transp
	comment := current.Comment

	merged := ical.VEvent{
		UID:          current.UID,
		DTStamp:      dtstamp,
		Start:        &start,
		End:          &end,
		TZName:       tzEff,
		AllDay:       current.AllDay,
		Summary:      &summary,
		Description:  &description,
		Location:     &location,
		Status:       &status,
		Transp:       &transp,
		Comment:      &comment,
		Sequence:     &sequence,
		RRule:        rrule,
		Exdates:      exdates,
		RecurrenceID: recurrenceID,
	}
	if !current.Created.IsZero() {
		created := current.Created
		merged.Created = &created
	}
	return merged
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

	frags, err := ical.BuildFragments(mergeUpdate(current, opts, Now().UTC()))
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

	body, err := sealCards(frags, access.AddrKR, nil, sharedSK, calSK)
	if err != nil {
		return nil, err
	}

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
