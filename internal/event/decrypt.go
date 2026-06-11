package event

import (
	"context"
	"errors"
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
// extracted. It errors only on a nil raw event.
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
	}
	// Shared parts: summary/description/location (encrypted) and
	// times/recurrence (signed). The signed fragment is kept verbatim for
	// SEQUENCE handling on updates.
	mergeParts(ev, raw.SharedEvents, raw.SharedKeyPacket, calKR, true)
	// Calendar parts: STATUS/TRANSP (signed) and COMMENT (encrypted).
	mergeParts(ev, raw.CalendarEvents, raw.CalendarKeyPacket, calKR, false)
	return ev, nil
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
				continue
			}
			plain, err := pgp.DecryptPart(part.Data, keyPacketB64, calKR)
			if err != nil {
				continue // lenient: one bad card never kills the event
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
