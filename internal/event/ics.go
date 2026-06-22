package event

import (
	"errors"
	"sort"
	"strings"

	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/ical"
)

// ICSProdID identifies this tool as the producer of exported iCalendar data
// (RFC 5545 PRODID). It is informational; consumers ignore it.
const ICSProdID = "-//proton-cal//proton-cal//EN"

// RowExtras carries the row-level data that Proton stores outside the
// encrypted cards and that must be re-injected into an exported VEVENT: the
// COLOR property and a VALARM per reminder. Callers (calsvc) resolve these to
// their EFFECTIVE values (the event's own when set, otherwise the calendar
// default) so the export mirrors what the Proton clients actually display.
type RowExtras struct {
	Color         string
	Notifications []caltypes.Notification
}

// ExtrasFromRow reads the literal row columns as RowExtras. It is the
// non-resolving fallback (used when no effective values are available); the
// normal path passes calsvc-resolved effective extras instead.
func ExtrasFromRow(raw *caltypes.RawEvent) RowExtras {
	return RowExtras{Color: raw.Color, Notifications: raw.Notifications}
}

// BuildICS reconstructs a standards-complete iCalendar VCALENDAR/VEVENT for a
// raw event row by decrypting its cards and merging them into one VEVENT
// (preserving third-party/unknown properties verbatim), then re-injecting the
// row-level data that Proton stores outside the cards: COLOR (the effective
// per-event color) and a VALARM per effective reminder (exactly as the web
// client does on read).
//
// Decryption is lenient (no signature verification); BuildICS returns an
// error only when no card content could be assembled at all.
func BuildICS(raw *caltypes.RawEvent, calKR *crypto.KeyRing, extras RowExtras) (string, error) {
	body, err := buildVEventBody(raw, calKR, extras)
	if err != nil {
		return "", err
	}
	return ical.MergeVCalendar(ICSProdID, body), nil
}

// BuildSeriesICS reconstructs one VCALENDAR containing every VEVENT of a
// recurring series: the master VEVENT (with its RRULE/EXDATEs) followed by one
// VEVENT per edited occurrence (each carrying its RECURRENCE-ID, read verbatim
// from the row's signed card). Rows are emitted master-first, then exceptions
// ordered by RecurrenceID, for deterministic output.
//
// extrasByID maps an event ID to its resolved (effective) COLOR/VALARM extras;
// a row missing from the map falls back to its literal row columns.
//
// Decryption is lenient: rows whose cards cannot be read are skipped. The
// returned anyDecryptFailed is true when at least one row was skipped (so the
// caller can drive a key-refresh retry); ErrDecryptDegraded is returned only
// when NO row could be assembled at all.
func BuildSeriesICS(rows []*caltypes.RawEvent, calKR *crypto.KeyRing, extrasByID map[string]RowExtras) (ics string, anyDecryptFailed bool, err error) {
	ordered := orderSeriesRows(rows)

	var bodies [][]string
	for _, raw := range ordered {
		if raw == nil {
			continue
		}
		extras, ok := extrasByID[raw.ID]
		if !ok {
			extras = ExtrasFromRow(raw)
		}
		body, berr := buildVEventBody(raw, calKR, extras)
		if berr != nil {
			anyDecryptFailed = true
			continue // lenient: skip rows we cannot decrypt
		}
		bodies = append(bodies, body)
	}

	if len(bodies) == 0 {
		return "", anyDecryptFailed, ErrDecryptDegraded
	}
	return ical.MergeVCalendar(ICSProdID, bodies...), anyDecryptFailed, nil
}

// orderSeriesRows returns the rows master-first (RRULE && RecurrenceID==0),
// then exception rows sorted by RecurrenceID, then any remaining rows. The
// input is not mutated.
func orderSeriesRows(rows []*caltypes.RawEvent) []*caltypes.RawEvent {
	out := make([]*caltypes.RawEvent, len(rows))
	copy(out, rows)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a == nil || b == nil {
			return a != nil // non-nil sorts before nil
		}
		am, bm := a.IsMaster(), b.IsMaster()
		if am != bm {
			return am // masters first
		}
		if a.RecurrenceID != b.RecurrenceID {
			return a.RecurrenceID < b.RecurrenceID
		}
		return a.ID < b.ID
	})
	return out
}

// buildVEventBody decrypts one row's cards, merges them into a single
// BEGIN:VEVENT...END:VEVENT block, and injects the effective COLOR/VALARMs.
// Returns ErrDecryptDegraded when no card could be read.
func buildVEventBody(raw *caltypes.RawEvent, calKR *crypto.KeyRing, extras RowExtras) ([]string, error) {
	if raw == nil {
		return nil, errors.New("event: nil raw event")
	}

	var cards []ical.MergeCard
	add := func(parts []caltypes.EventPart, keyPacket string, sharedSigned bool) {
		for _, part := range parts {
			data, signed, err := partPlaintext(part, keyPacket, calKR)
			if err != nil {
				continue // lenient: skip cards we cannot read
			}
			if strings.TrimSpace(data) == "" {
				continue
			}
			cards = append(cards, ical.MergeCard{
				SharedSigned: sharedSigned && signed,
				Data:         data,
			})
		}
	}

	add(raw.SharedEvents, raw.SharedKeyPacket, true)
	add(raw.CalendarEvents, raw.CalendarKeyPacket, false)
	add(raw.AttendeesEvents, raw.SharedKeyPacket, false)

	if len(cards) == 0 {
		return nil, ErrDecryptDegraded
	}

	body := ical.MergeVEventBody(cards...)
	return injectRowData(body, extras), nil
}

// injectRowData adds the COLOR property and VALARM components for the
// row-level fields that are not stored as iCal properties in the cards. The
// additions are inserted just before END:VEVENT.
func injectRowData(body []string, extras RowExtras) []string {
	var extra []string
	if extras.Color != "" {
		extra = append(extra, "COLOR:"+extras.Color)
	}
	for _, n := range extras.Notifications {
		extra = append(extra, valarmLines(n)...)
	}
	if len(extra) == 0 {
		return body
	}

	// Insert before the trailing END:VEVENT.
	for i := len(body) - 1; i >= 0; i-- {
		if body[i] == "END:VEVENT" {
			out := make([]string, 0, len(body)+len(extra))
			out = append(out, body[:i]...)
			out = append(out, extra...)
			out = append(out, body[i:]...)
			return out
		}
	}
	return body
}

// valarmLines renders one VALARM for a notification. Type 0 = email, anything
// else = display (matching NOTIFICATION_TYPE_API). The Trigger is already an
// iCal duration; a DESCRIPTION is required by RFC 5545 for DISPLAY alarms.
func valarmLines(n caltypes.Notification) []string {
	action := "DISPLAY"
	if n.Type == 0 {
		action = "EMAIL"
	}
	lines := []string{
		"BEGIN:VALARM",
		"ACTION:" + action,
		"TRIGGER:" + n.Trigger,
		"DESCRIPTION:Reminder",
	}
	if action == "EMAIL" {
		lines = append(lines, "SUMMARY:Reminder")
	}
	return append(lines, "END:VALARM")
}
