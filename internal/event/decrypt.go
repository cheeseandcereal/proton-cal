package event

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/ical"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
	"github.com/cheeseandcereal/proton-cal/internal/pgp"
	"github.com/cheeseandcereal/proton-cal/internal/recurrence"
)

// Decrypt decrypts a raw event row's cards into an Event. Lenient:
// signature verification is skipped and any part that fails to decrypt or
// parse is skipped; the event is still returned with whatever could be
// extracted (failures set Event.DecryptFailed). It errors only on a nil
// raw event.
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
		EventID:       raw.ID,
		UID:           raw.UID,
		CalendarID:    raw.CalendarID,
		Start:         time.Unix(raw.StartTime, 0).UTC(),
		End:           time.Unix(raw.EndTime, 0).UTC(),
		StartTimezone: raw.StartTimezone,
		EndTimezone:   raw.EndTimezone,
		AllDay:        raw.IsAllDay(),
		Status:        "CONFIRMED",
		Transp:        "OPAQUE",
		RRule:         raw.RRule,
		RecurrenceID:  recurrenceID,
		Exdates:       exdates,
		Color:         raw.Color,
		IsOrganizer:   raw.IsOrganizer != 0,
		MoreAttendees: raw.AttendeesInfo.MoreAttendees != 0,
		Notifications: raw.Notifications,
	}
	// Shared parts: summary/description/location + conference URL (encrypted)
	// and times/recurrence + organizer + conference ID (signed). The signed
	// fragment is kept verbatim for SEQUENCE handling on updates.
	mergeParts(ev, raw.SharedEvents, raw.SharedKeyPacket, calKR, true)
	// Calendar parts: STATUS/TRANSP (signed) and COMMENT (encrypted).
	mergeParts(ev, raw.CalendarEvents, raw.CalendarKeyPacket, calKR, false)
	// Attendee identities live in the attendees card, encrypted with the
	// shared session key (same key packet as the shared encrypted card).
	mergeParts(ev, raw.AttendeesEvents, raw.SharedKeyPacket, calKR, true)

	enrichAttendeeStatus(ev, raw.Attendees)
	assembleConference(ev)
	// Proton embeds the conference join block into DESCRIPTION for client
	// portability; surface a clean description (the conference is exposed as
	// a structured field). The raw cards used by BuildICS are untouched, so
	// the ICS export keeps the embedded block.
	if ev.Conference != nil {
		ev.Description = ical.StripConferenceBlock(ev.Description)
	}
	return ev, nil
}

// enrichAttendeeStatus joins the live RSVP status from the plaintext row
// onto the decrypted attendees by their anonymized token. Attendees with no
// matching row token get Status -1.
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

// assembleConference promotes the conference fields gathered across the
// shared cards into a Conference, parsing the password from the URL's
// "#pwd-" fragment. No-op when the event carries no conference data.
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

func mergeParts(ev *Event, parts []caltypes.EventPart, keyPacketB64 string, calKR *crypto.KeyRing, shared bool) {
	for _, part := range parts {
		var data string
		signed := false
		switch {
		case part.Type == caltypes.CardClear || part.Type == caltypes.CardSigned:
			data = part.Data
			signed = true
			if data != "" && shared {
				ev.RawSharedSigned = data
			}
		case part.IsEncrypted():
			if calKR == nil {
				ev.DecryptFailed = true
				continue
			}
			plain, err := pgp.DecryptPart(part.Data, keyPacketB64, calKR)
			if err != nil {
				// Lenient: one bad card never kills the event, but the
				// degradation is recorded - write paths must not merge
				// from a half-decrypted event.
				ev.DecryptFailed = true
				continue
			}
			data = plain
		default:
			continue
		}
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
	if p.Status != nil && *p.Status != "" {
		ev.Status = *p.Status
	}
	if p.Transp != nil && *p.Transp != "" {
		ev.Transp = *p.Transp
	}
	if p.Comment != nil {
		ev.Comment = *p.Comment
	}
	if p.Created != nil {
		ev.Created = p.Created.UTC()
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
		if a.Token != "" && attendeeSeen(ev.Attendees, a.Token) {
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

// attendeeSeen reports whether an attendee with the given non-empty token is
// already present.
func attendeeSeen(attendees []Attendee, token string) bool {
	for _, a := range attendees {
		if a.Token == token {
			return true
		}
	}
	return false
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
