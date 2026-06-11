package event

import (
	"context"
	"errors"

	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal-go/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal-go/internal/ical"
	"github.com/cheeseandcereal/proton-cal-go/internal/papi"
	"github.com/cheeseandcereal/proton-cal-go/internal/pgp"
	"github.com/cheeseandcereal/proton-cal-go/internal/recurrence"
)

// decryptImpl decrypts a raw event row's cards into an Event.
// Lenient: any part that fails to decrypt or parse is skipped; the event is
// still returned with whatever could be extracted.
func decryptImpl(raw *caltypes.RawEvent, calKR *crypto.KeyRing) (*Event, error) {
	if raw == nil {
		return nil, errors.New("event: nil raw event")
	}
	ev := &Event{
		EventID:       raw.ID,
		UID:           raw.UID,
		CalendarID:    raw.CalendarID,
		StartTime:     raw.StartTime,
		EndTime:       raw.EndTime,
		StartTimezone: raw.StartTimezone,
		EndTimezone:   raw.EndTimezone,
		AllDay:        raw.IsAllDay(),
		Status:        "CONFIRMED",
		RRule:         raw.RRule,
		RecurrenceID:  raw.RecurrenceID,
		Exdates:       append([]int64(nil), raw.Exdates...),
	}
	// Shared parts: summary/description/location (encrypted) and
	// times/recurrence (signed). The signed fragment is kept verbatim for
	// SEQUENCE handling on updates.
	mergeParts(ev, raw.SharedEvents, raw.SharedKeyPacket, calKR, true)
	// Calendar parts: STATUS/TRANSP (signed) and COMMENT (encrypted).
	mergeParts(ev, raw.CalendarEvents, raw.CalendarKeyPacket, calKR, false)
	return ev, nil
}

func mergeParts(ev *Event, parts []caltypes.EventPart, keyPacketB64 string, calKR *crypto.KeyRing, storeRawSigned bool) {
	for _, part := range parts {
		var data string
		switch {
		case part.Type == caltypes.CardClear || part.Type == caltypes.CardSigned:
			data = part.Data
			if data != "" && storeRawSigned {
				ev.RawSharedSigned = data
				ev.Sequence = ical.SequenceFromFragment(data)
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
		mergeParsed(ev, parsed)
	}
}

func mergeParsed(ev *Event, p ical.ParsedEvent) {
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
	if p.StartTS != 0 {
		ev.StartTime = p.StartTS
	}
	if p.EndTS != 0 {
		ev.EndTime = p.EndTS
	}
}

func listWindowImpl(ctx context.Context, client *papi.Client, calKR *crypto.KeyRing, calendarID string, start, end int64, tzName string) ([]Listed, error) {
	raws, err := queryImpl(ctx, client, calendarID, start, end, tzName)
	if err != nil {
		return nil, err
	}
	occs := recurrence.ExpandOccurrences(raws, start, end)

	type cached struct {
		ev  *Event
		err error
	}
	cache := make(map[string]cached)
	out := make([]Listed, 0, len(occs))
	for _, occ := range occs {
		id := occ.Event.ID
		c, ok := cache[id]
		if !ok {
			ev, err := decryptImpl(occ.Event, calKR)
			c = cached{ev: ev, err: err}
			cache[id] = c
		}
		l := Listed{Occurrence: occ, Err: c.err}
		if c.err == nil {
			l.Event = c.ev
		}
		out = append(out, l)
	}
	return out, nil
}
