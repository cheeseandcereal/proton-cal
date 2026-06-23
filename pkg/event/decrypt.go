package event

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/ical"
	"github.com/cheeseandcereal/proton-cal/pkg/papi"
	"github.com/cheeseandcereal/proton-cal/pkg/pgp"
	"github.com/cheeseandcereal/proton-cal/pkg/recurrence"
)

// Decrypt decrypts a raw event row's cards into an Event. Lenient: skips
// signature verification and any unparseable part (setting Event.DecryptFailed);
// errors only on a nil raw event.
func Decrypt(raw *caltypes.RawEvent, calKR *crypto.KeyRing) (*Event, error) {
	if raw == nil {
		return nil, errors.New("event: nil raw event")
	}
	exdates := make([]time.Time, 0, len(raw.Exdates))
	for _, ts := range raw.Exdates {
		exdates = append(exdates, time.Unix(ts, 0).UTC())
	}
	var recurrenceID time.Time
	if raw.RecurrenceID != 0 {
		recurrenceID = time.Unix(raw.RecurrenceID, 0).UTC()
	}
	ev := &Event{
		EventID:          raw.ID,
		UID:              raw.UID,
		CalendarID:       raw.CalendarID,
		Start:            time.Unix(raw.StartTime, 0).UTC(),
		End:              time.Unix(raw.EndTime, 0).UTC(),
		StartTimezone:    raw.StartTimezone,
		AllDay:           raw.IsAllDay(),
		RRule:            raw.RRule,
		RecurrenceID:     recurrenceID,
		Exdates:          exdates,
		Color:            raw.Color,
		IsOrganizer:      raw.IsOrganizer != 0,
		MoreAttendees:    raw.AttendeesInfo.MoreAttendees != 0,
		Notifications:    raw.Notifications,
		NotificationsSet: raw.NotificationsSet,
	}
	// Shared parts (encrypted + signed); the signed fragment carries SEQUENCE.
	mergeParts(ev, raw.SharedEvents, raw.SharedKeyPacket, calKR, true)
	// Calendar parts (signed STATUS/TRANSP, encrypted COMMENT) decrypt fine
	// but surface no fields beyond DecryptFailed tracking.
	mergeParts(ev, raw.CalendarEvents, raw.CalendarKeyPacket, calKR, false)
	// Attendee identities encrypted with the shared session key.
	mergeParts(ev, raw.AttendeesEvents, raw.SharedKeyPacket, calKR, true)

	enrichAttendeeStatus(ev, raw.Attendees)
	assembleConference(ev)
	// Proton embeds the conference join block into DESCRIPTION; strip it for
	// the structured field. Raw cards (used by BuildICS) keep the block.
	if ev.Conference != nil {
		ev.Description = ical.StripConferenceBlock(ev.Description)
	}
	return ev, nil
}

// enrichAttendeeStatus joins live RSVP status from the plaintext row onto
// decrypted attendees by token; unmatched attendees get Status -1.
func enrichAttendeeStatus(ev *Event, rows []caltypes.AttendeeToken) {
	statusByToken := make(map[string]int, len(rows))
	for _, r := range rows {
		statusByToken[r.Token] = r.Status
	}
	for i := range ev.Attendees {
		if st, ok := statusByToken[ev.Attendees[i].Token]; ok {
			ev.Attendees[i].Status = st
		} else {
			ev.Attendees[i].Status = -1
		}
	}
}

// assembleConference builds a Conference from the gathered fields, parsing
// the password from the URL's "#pwd-" fragment. No-op without conference data.
func assembleConference(ev *Event) {
	if ev.confID == "" && ev.confURL == "" {
		return
	}
	c := &Conference{
		Provider: ev.confProvider,
		ID:       ev.confID,
		URL:      ev.confURL,
		Host:     ev.confHost,
	}
	if i := strings.Index(ev.confURL, "#pwd-"); i >= 0 {
		c.Password = ev.confURL[i+len("#pwd-"):]
	}
	ev.Conference = c
}

// partPlaintext returns one card part's clear text (verbatim for clear/signed,
// decrypted for encrypted). signed reports a non-encrypted card; err is non-nil
// only when an encrypted card cannot be decrypted (nil key ring counts as failure).
func partPlaintext(part caltypes.EventPart, keyPacketB64 string, calKR *crypto.KeyRing) (data string, signed bool, err error) {
	switch {
	case part.Type == caltypes.CardClear || part.Type == caltypes.CardSigned:
		return part.Data, true, nil
	case part.IsEncrypted():
		if calKR == nil {
			return "", false, errNoKeyRing
		}
		plain, derr := pgp.DecryptPart(part.Data, keyPacketB64, calKR)
		return plain, false, derr
	default:
		return "", false, nil
	}
}

// errNoKeyRing marks an encrypted card undecryptable for lack of a key ring.
var errNoKeyRing = errors.New("event: no calendar key ring")

func mergeParts(ev *Event, parts []caltypes.EventPart, keyPacketB64 string, calKR *crypto.KeyRing, shared bool) {
	for _, part := range parts {
		data, signed, err := partPlaintext(part, keyPacketB64, calKR)
		if err != nil {
			// Lenient: one bad card never kills the event, but record it -
			// write paths must not merge from a half-decrypted event.
			ev.DecryptFailed = true
			continue
		}
		// A non-encrypted card type we don't recognize yields empty data.
		if data == "" {
			continue
		}
		parsed, err := ical.ParseFragment(data)
		if err != nil {
			ev.DecryptFailed = true
			continue
		}
		if shared && signed && parsed.Sequence != nil {
			ev.Sequence = *parsed.Sequence
		}
		mergeParsed(ev, parsed)
	}
}

func mergeParsed(ev *Event, p ical.VEvent) {
	if p.Summary != nil {
		ev.Summary = *p.Summary
	}
	if p.Description != nil {
		ev.Description = *p.Description
	}
	if p.Location != nil {
		ev.Location = *p.Location
	}
	if p.Start != nil {
		ev.Start = p.Start.UTC()
	}
	if p.End != nil {
		ev.End = p.End.UTC()
	}
	if p.Organizer != nil {
		ev.Organizer = &Person{Email: p.Organizer.Email, CN: p.Organizer.CN}
	}
	// Attendees are repeatable and split across cards; append (de-duping by
	// token, preferring the first sighting which carries the richest params).
	for _, a := range p.Attendees {
		if a.Token != "" && slices.ContainsFunc(ev.Attendees, func(x Attendee) bool { return x.Token == a.Token }) {
			continue
		}
		ev.Attendees = append(ev.Attendees, Attendee{
			Email: a.Email, CN: a.CN, Role: a.Role,
			PartStat: a.PartStat, RSVP: a.RSVP, Token: a.Token,
		})
	}
	if p.ConferenceID != "" {
		ev.confID = p.ConferenceID
		ev.confProvider = p.ConferenceProvider
	}
	if p.ConferenceURL != "" {
		ev.confURL = p.ConferenceURL
		ev.confHost = p.ConferenceHost
	}
}

// ListWindow queries, expands and decrypts all occurrences overlapping
// [start, end), deduplicating decryption per event row.
func ListWindow(ctx context.Context, client papi.API, calKR *crypto.KeyRing, calendarID string, start, end int64, tzName string) ([]Listed, error) {
	raws, err := query(ctx, client, calendarID, start, end, tzName)
	if err != nil {
		return nil, err
	}
	occs := recurrence.ExpandOccurrences(raws, start, end)

	cache := make(map[string]*Event)
	out := make([]Listed, 0, len(occs))
	for _, occ := range occs {
		id := occ.Event.ID
		ev, ok := cache[id]
		if !ok {
			// Decrypt errors only on a nil row; expansion never yields one.
			ev, _ = Decrypt(occ.Event, calKR)
			cache[id] = ev
		}
		out = append(out, Listed{Occurrence: occ, Event: ev})
	}
	return out, nil
}
