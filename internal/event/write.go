package event

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/ical"
	"github.com/cheeseandcereal/proton-cal/internal/icaltime"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
	"github.com/cheeseandcereal/proton-cal/internal/pgp"
)

var jsonNull = json.RawMessage("null")
var jsonEmptyArray = json.RawMessage("[]")

// sealCards builds the sync Event body for a CREATE: it signs the two
// plaintext cards and encrypts+signs the two encrypted cards with fresh
// session keys encrypted to the calendar public key, emitting their key
// packets. Updates do not go through here - they patch the existing cards in
// place and reuse the event's session keys (see buildUpdateBody).
func sealCards(frags ical.Fragments, addrKR, calKR *crypto.KeyRing) (*eventBody, error) {
	// Sign plaintext parts with the address key.
	sharedSignedSig, err := pgp.SignDetached(frags.SharedSigned, addrKR)
	if err != nil {
		return nil, fmt.Errorf("signing shared part: %w", err)
	}
	calSignedSig, err := pgp.SignDetached(frags.CalendarSigned, addrKR)
	if err != nil {
		return nil, fmt.Errorf("signing calendar part: %w", err)
	}

	sharedKP, sharedData, sharedSig, err := pgp.EncryptAndSign(frags.SharedEncrypted, calKR, addrKR)
	if err != nil {
		return nil, fmt.Errorf("encrypting shared part: %w", err)
	}
	calKP, calData, calSig, err := pgp.EncryptAndSign(frags.CalendarEncrypted, calKR, addrKR)
	if err != nil {
		return nil, fmt.Errorf("encrypting calendar part: %w", err)
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
		Attendees:             jsonEmptyArray,
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

	body, err := sealCards(frags, access.AddrKR, access.KR)
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

// update fetches the event, patches the existing cards in place (reusing the
// event's session keys; no new key packets) and PUTs it. Returns the updated
// raw event when the server echoes it (may be nil on success).
func update(ctx context.Context, client papi.API, access *calendar.Access, eventID string, opts UpdateOptions) (*caltypes.RawEvent, error) {
	raw, err := Get(ctx, client, access.CalendarID, eventID)
	if err != nil {
		return nil, err
	}
	current, err := Decrypt(raw, access.KR)
	if err != nil {
		return nil, err
	}
	if current.DecryptFailed {
		return nil, fmt.Errorf("updating event %s: %w", eventID, ErrDecryptDegraded)
	}

	// Patch the existing cards in place, preserving every property the
	// update does not touch (conferencing, organizer, attendees, third-party
	// X- props, COMMENT, etc.). Rebuilding from a fixed field set would
	// silently drop them.
	body, err := buildUpdateBody(raw, current, opts, access)
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

// buildUpdateBody re-seals an event for update by patching the existing
// decrypted cards in place: only the properties the update touches are
// changed, every other property (conferencing, organizer, attendees,
// third-party X- props, COMMENT, ...) is preserved verbatim. The event's
// existing session keys are reused; the body carries no key packets.
func buildUpdateBody(raw *caltypes.RawEvent, current *Event, opts UpdateOptions, access *calendar.Access) (*eventBody, error) {
	sharedSK, err := pgp.DecryptSessionKey(raw.SharedKeyPacket, access.KR)
	if err != nil {
		return nil, fmt.Errorf("extracting shared session key: %w", err)
	}
	// Some events (e.g. created by the Proton web app) carry no encrypted
	// calendar card, and therefore no calendar key packet. Only events with
	// a calendar key packet have an encrypted calendar card to reseal.
	var calSK *crypto.SessionKey
	if raw.CalendarKeyPacket != "" {
		calSK, err = pgp.DecryptSessionKey(raw.CalendarKeyPacket, access.KR)
		if err != nil {
			return nil, fmt.Errorf("extracting calendar session key: %w", err)
		}
	}

	signedPatch, encPatch := sharedCardPatches(current, opts)

	sharedParts, err := resealCard(raw.SharedEvents, raw.SharedKeyPacket, sharedSK, access, signedPatch, encPatch)
	if err != nil {
		return nil, fmt.Errorf("resealing shared card: %w", err)
	}
	calParts, err := resealCard(raw.CalendarEvents, raw.CalendarKeyPacket, calSK, access, ical.CardPatch{}, ical.CardPatch{})
	if err != nil {
		return nil, fmt.Errorf("resealing calendar card: %w", err)
	}
	// Attendees card is encrypted with the shared session key; left verbatim.
	attParts, err := resealCard(raw.AttendeesEvents, raw.SharedKeyPacket, sharedSK, access, ical.CardPatch{}, ical.CardPatch{})
	if err != nil {
		return nil, fmt.Errorf("resealing attendees card: %w", err)
	}
	if attParts == nil {
		attParts = []caltypes.EventPart{}
	}

	// Preserve the event's existing personal/row data exactly as the web
	// client does on update (formatData): re-send Notifications, Color and
	// the clear Attendees rows. Notifications must keep their tri-state -
	// null (inherit the calendar default), [] (explicitly none) or the
	// custom array - so an update neither wipes reminders nor silently
	// re-enables the calendar default on an event whose reminders were
	// removed.
	notifications, err := marshalNotifications(raw.Notifications, raw.NotificationsSet)
	if err != nil {
		return nil, fmt.Errorf("encoding notifications: %w", err)
	}
	color := jsonNull
	if raw.Color != "" {
		if color, err = json.Marshal(raw.Color); err != nil {
			return nil, fmt.Errorf("encoding color: %w", err)
		}
	}
	attendees, err := marshalAttendees(raw.Attendees)
	if err != nil {
		return nil, fmt.Errorf("encoding attendees: %w", err)
	}

	return &eventBody{
		Permissions:           1,
		SharedEventContent:    sharedParts,
		CalendarEventContent:  calParts,
		AttendeesEventContent: attParts,
		Attendees:             attendees,
		Notifications:         notifications,
		Color:                 color,
	}, nil
}

// marshalNotifications preserves the event's notification tri-state on the
// wire: null when the event inherits the calendar default (!set), [] when
// reminders are explicitly none (set, empty), or the custom array.
func marshalNotifications(ns []caltypes.Notification, set bool) (json.RawMessage, error) {
	if !set {
		return jsonNull, nil
	}
	if len(ns) == 0 {
		return jsonEmptyArray, nil
	}
	return json.Marshal(ns)
}

// marshalAttendees renders the clear Attendees rows for an update body
// (token + live status + verbatim comment), mirroring the web client. An
// event with no attendees serializes as an empty array.
func marshalAttendees(tokens []caltypes.AttendeeToken) (json.RawMessage, error) {
	if len(tokens) == 0 {
		return jsonEmptyArray, nil
	}
	out := make([]attendeeClear, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, attendeeClear{Token: t.Token, Status: t.Status, Comment: jsonNull})
	}
	return json.Marshal(out)
}

// resealCard re-signs (plaintext parts) and re-encrypts (encrypted parts,
// reusing sessionKey) every part of one card slice, applying signedPatch to
// the signed/clear part and encPatch to the encrypted part. Part order and
// types are preserved. Cards with no encrypted part ignore sessionKey.
func resealCard(parts []caltypes.EventPart, keyPacketB64 string, sessionKey *crypto.SessionKey, access *calendar.Access, signedPatch, encPatch ical.CardPatch) ([]caltypes.EventPart, error) {
	out := make([]caltypes.EventPart, 0, len(parts))
	for _, part := range parts {
		switch {
		case part.Type == caltypes.CardClear || part.Type == caltypes.CardSigned:
			plain := ical.PatchCard(part.Data, signedPatch)
			sig, err := pgp.SignDetached(plain, access.AddrKR)
			if err != nil {
				return nil, fmt.Errorf("signing part: %w", err)
			}
			out = append(out, caltypes.EventPart{Type: caltypes.CardSigned, Data: plain, Signature: sig})
		case part.IsEncrypted():
			plain, err := pgp.DecryptPart(part.Data, keyPacketB64, access.KR)
			if err != nil {
				return nil, fmt.Errorf("decrypting part for reseal: %w", err)
			}
			plain = ical.PatchCard(plain, encPatch)
			data, sig, err := pgp.EncryptWithSessionKeyAndSign(plain, sessionKey, access.AddrKR)
			if err != nil {
				return nil, fmt.Errorf("re-encrypting part: %w", err)
			}
			out = append(out, caltypes.EventPart{Type: caltypes.CardEncryptedAndSigned, Data: data, Signature: sig})
		default:
			out = append(out, part)
		}
	}
	return out, nil
}

// sharedCardPatches translates an UpdateOptions into the property edits for
// the two shared cards. The signed card owns the structural properties
// (DTSTART/DTEND/RRULE/EXDATE/SEQUENCE); the encrypted card owns the
// user-visible text (SUMMARY/DESCRIPTION/LOCATION).
func sharedCardPatches(current *Event, opts UpdateOptions) (signed, enc ical.CardPatch) {
	signed = ical.CardPatch{Set: map[string]string{}, Delete: map[string]bool{}}
	enc = ical.CardPatch{Set: map[string]string{}, Delete: map[string]bool{}}

	tzEff := opts.TZName
	if tzEff == "" {
		tzEff = icaltime.OrUTC(current.StartTimezone)
	}
	// Reformat times when the start/end changes OR the zone changes (the
	// stored DTSTART/DTEND carry the old zone and must be rewritten).
	rezone := opts.TZName != "" && opts.TZName != icaltime.OrUTC(current.StartTimezone)
	if opts.Start != nil || rezone {
		start := current.Start
		if opts.Start != nil {
			start = *opts.Start
		}
		if v, err := ical.DateValue(start, tzEff, current.AllDay); err == nil {
			signed.Set["DTSTART"] = v
		}
	}
	if opts.End != nil || rezone {
		end := current.End
		if opts.End != nil {
			end = *opts.End
		}
		if v, err := ical.DateValue(end, tzEff, current.AllDay); err == nil {
			signed.Set["DTEND"] = v
		}
	}
	if opts.Significant() {
		signed.Set["SEQUENCE"] = fmt.Sprintf(":%d", current.Sequence+1)
	}
	if opts.ClearRRule {
		signed.Delete["RRULE"] = true
		signed.Delete["EXDATE"] = true
	} else if opts.RRule != nil {
		signed.Set["RRULE"] = ":" + *opts.RRule
	}
	for _, ex := range opts.AddExdates {
		if v, err := ical.DateValue(ex, tzEff, current.AllDay); err == nil {
			signed.Append = append(signed.Append, "EXDATE"+v)
		}
	}

	// Text fields: an explicit empty value removes the property (matching the
	// original build, which omits empty SUMMARY/DESCRIPTION/LOCATION).
	applyText(&enc, "SUMMARY", opts.Summary)
	applyText(&enc, "DESCRIPTION", opts.Description)
	applyText(&enc, "LOCATION", opts.Location)
	return signed, enc
}

// applyText records a TEXT property edit: nil = keep current (no-op), ""
// = delete, otherwise set the escaped value.
func applyText(p *ical.CardPatch, name string, v *string) {
	if v == nil {
		return
	}
	if *v == "" {
		p.Delete[name] = true
		return
	}
	p.Set[name] = ":" + ical.EscapeText(*v)
}
