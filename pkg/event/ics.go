package event

import (
	"errors"
	"sort"
	"strings"

	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/ical"
)

// ICSProdID is the producer of exported iCalendar data (RFC 5545 PRODID); informational.
const ICSProdID = "-//proton-cal//proton-cal//EN"

// RowExtras carries row-level data stored outside the encrypted cards and
// re-injected into an exported VEVENT (COLOR, a VALARM per reminder). Callers
// resolve these to EFFECTIVE values so the export mirrors the Proton clients.
type RowExtras struct {
	Color         string
	Notifications []caltypes.Notification
}

// ExtrasFromRow reads the literal row columns as RowExtras: the non-resolving
// fallback when no effective values are available.
func ExtrasFromRow(raw *caltypes.RawEvent) RowExtras {
	return RowExtras{Color: raw.Color, Notifications: raw.Notifications}
}

// BuildICS reconstructs an iCalendar VCALENDAR/VEVENT from a raw row: decrypts
// and merges its cards (unknown properties verbatim), then injects effective
// COLOR and a VALARM per reminder. Lenient decryption (no signature verify);
// errors only when no card content could be assembled at all.
func BuildICS(raw *caltypes.RawEvent, calKR *crypto.KeyRing, extras RowExtras) (string, error) {
	body, err := buildVEventBody(raw, calKR, extras)
	if err != nil {
		return "", err
	}
	return ical.MergeVCalendar(ICSProdID, body), nil
}

// BuildSeriesICS reconstructs one VCALENDAR with every VEVENT of a series:
// master first (RRULE/EXDATEs), then exceptions ordered by RecurrenceID, for
// deterministic output. extrasByID maps event ID to resolved COLOR/VALARM extras
// (falling back to literal row columns). Lenient: unreadable rows are skipped and
// set anyDecryptFailed; returns ErrDecryptDegraded only when NO row assembled.
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

// orderSeriesRows returns rows master-first, then exceptions by RecurrenceID,
// then the rest. Input is not mutated.
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

// buildVEventBody decrypts one row's cards, merges them into one VEVENT block,
// and injects effective COLOR/VALARMs. Returns ErrDecryptDegraded if no card read.
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

// injectRowData adds COLOR and VALARM components for row-level fields not stored
// in the cards, inserted just before END:VEVENT.
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

// valarmLines renders one VALARM. Type 0 = email, else display (NOTIFICATION_TYPE_API);
// DESCRIPTION is required by RFC 5545 for DISPLAY alarms.
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
