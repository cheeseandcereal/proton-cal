package event

import (
	"errors"
	"strings"

	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/ical"
)

// ICSProdID identifies this tool as the producer of exported iCalendar data
// (RFC 5545 PRODID). It is informational; consumers ignore it.
const ICSProdID = "-//proton-cal//proton-cal//EN"

// BuildICS reconstructs a standards-complete iCalendar VCALENDAR/VEVENT for a
// raw event row by decrypting its cards and merging them into one VEVENT
// (preserving third-party/unknown properties verbatim), then re-injecting the
// row-level data that Proton stores outside the cards: COLOR (the per-event
// color) and a VALARM per reminder (synthesized from the plaintext
// Notifications, exactly as the web client does on read).
//
// Decryption is lenient (no signature verification); BuildICS returns an
// error only when no card content could be assembled at all.
func BuildICS(raw *caltypes.RawEvent, calKR *crypto.KeyRing) (string, error) {
	if raw == nil {
		return "", errors.New("event: nil raw event")
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
		return "", ErrDecryptDegraded
	}

	merged := ical.MergeFragments(ICSProdID, cards...)
	return injectRowData(merged, raw), nil
}

// injectRowData adds the COLOR property and VALARM components for the
// row-level fields that are not stored as iCal properties in the cards. The
// additions are inserted just before END:VEVENT.
func injectRowData(merged string, raw *caltypes.RawEvent) string {
	var extra []string
	if raw.Color != "" {
		extra = append(extra, "COLOR:"+raw.Color)
	}
	for _, n := range raw.Notifications {
		extra = append(extra, valarmLines(n)...)
	}
	if len(extra) == 0 {
		return merged
	}

	const marker = "\r\nEND:VEVENT"
	i := strings.LastIndex(merged, marker)
	if i < 0 {
		return merged
	}
	return merged[:i] + "\r\n" + strings.Join(extra, "\r\n") + merged[i:]
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
