package calendar

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/pkg/papi"
	"github.com/cheeseandcereal/proton-cal/pkg/pgp"
)

// codeColorNotAllowed is returned by POST /calendar/v1 (HTTP 400) when the
// requested color is not a Proton accent-palette value. Note this differs from
// the member-update path's code 2011 (see UpdateMember); both mean "bad color".
const codeColorNotAllowed = 2001

// calendarDisplayVisible is the Display value for a normally visible calendar
// (the web client's CALENDAR_DISPLAY.VISIBLE); 0 would hide it.
const calendarDisplayVisible = 1

// CreateInput describes a new owned calendar. ColorHex must already be a
// resolved Proton palette hex (callers validate via calcolor). AddrKR is the
// member address's keyring: it both signs the calendar passphrase and is the
// recipient its session key is encrypted to (see Create).
type CreateInput struct {
	Name        string
	Description string
	ColorHex    string
	AddressID   string
	AddrKR      *crypto.KeyRing
}

// createResponse is the wire shape of POST /calendar/v1. The calendar comes
// back fully formed but WITHOUT keys (Keys: [], Passphrase: null) until the
// setup-keys call below; reuse apiCalendar/newInfo to resolve display metadata.
type createResponse struct {
	Calendar apiCalendar `json:"Calendar"`
}

// ErrKeylessCalendar wraps a setup-keys failure that left a created-but-keyless
// calendar behind. The calendar exists (its ID is in the message) and can be
// completed later by re-running create; deletion needs the elevated scope.
var ErrKeylessCalendar = errors.New("calendar created but key setup failed")

// Create makes a new owned calendar in two steps, mirroring the web client:
// POST /calendar/v1 (metadata) then POST /calendar/v1/{id}/keys (key material),
// both with the normal session scope. It generates a fresh calendar key locked
// with a random passphrase, encrypts that passphrase to the member address key
// (split key packet + data packet) and detached-signs it, exactly as the unlock
// path expects to read back.
//
// If the second step fails, the first has already created a keyless calendar;
// the returned error wraps ErrKeylessCalendar and names the calendar ID. It is
// recoverable by re-running create-keys.
func Create(ctx context.Context, client papi.API, in CreateInput) (Info, error) {
	createBody := map[string]any{
		"Name":        in.Name,
		"Description": in.Description,
		"Color":       in.ColorHex,
		"Display":     calendarDisplayVisible,
		"AddressID":   in.AddressID,
	}
	var resp createResponse
	if err := client.Post(ctx, APIPath, createBody, &resp); err != nil {
		if papi.IsCode(err, codeColorNotAllowed) {
			return Info{}, fmt.Errorf("calendar color must be a Proton palette color: %w", err)
		}
		return Info{}, fmt.Errorf("creating calendar: %w", err)
	}
	info := newInfo(resp.Calendar)
	if info.ID == "" {
		return Info{}, errors.New("creating calendar: server returned no calendar ID")
	}

	setupBody, err := buildSetupKeysBody(in.AddressID, in.AddrKR)
	if err != nil {
		return Info{}, fmt.Errorf("%w (calendar %s): %w", ErrKeylessCalendar, info.ID, err)
	}
	if err := client.Post(ctx, APIPath+"/"+info.ID+"/keys", setupBody, nil); err != nil {
		return Info{}, fmt.Errorf("%w (calendar %s; re-run create to complete it): %w", ErrKeylessCalendar, info.ID, err)
	}
	return info, nil
}

// buildSetupKeysBody generates the calendar key material for POST
// /calendar/v1/{id}/keys: a random passphrase, a new calendar private key
// locked with it (armored), and the passphrase encrypted+signed to the address
// key as a split KeyPacket (PKESK) + DataPacket (SEIPD) plus an armored
// detached signature over the passphrase plaintext.
func buildSetupKeysBody(addressID string, addrKR *crypto.KeyRing) (map[string]any, error) {
	passphrase, err := generatePassphrase()
	if err != nil {
		return nil, err
	}

	privArmored, err := generateLockedCalendarKey(passphrase)
	if err != nil {
		return nil, err
	}

	// EncryptAndSign uses a fresh session key, encrypts the passphrase under it
	// (DataPacket), encrypts that session key to the address key (KeyPacket) and
	// detached-signs the passphrase plaintext with the address key.
	keyPacketB64, dataPacketB64, sigArmored, err := pgp.EncryptAndSign(passphrase, addrKR, addrKR)
	if err != nil {
		return nil, fmt.Errorf("encrypting calendar passphrase: %w", err)
	}

	return map[string]any{
		"AddressID":  addressID,
		"Signature":  sigArmored,
		"PrivateKey": privArmored,
		"Passphrase": map[string]any{
			"DataPacket": dataPacketB64,
			"KeyPacket":  keyPacketB64,
		},
	}, nil
}

// generatePassphrase returns a random calendar key passphrase: 32 CSPRNG bytes
// encoded as standard base64 (the text is the passphrase), matching the web
// client's generatePassphrase.
func generatePassphrase() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating calendar passphrase: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// generateLockedCalendarKey generates a new calendar private key (legacy
// Ed25519 + Curve25519, the "x25519" keyType, user ID name "Calendar key" with
// no email to avoid confusion on export), locks it with the passphrase and
// returns it armored. Mirrors the web client's generateCalendarKey.
func generateLockedCalendarKey(passphrase string) (string, error) {
	key, err := crypto.GenerateKey("Calendar key", "", "x25519", 0)
	if err != nil {
		return "", fmt.Errorf("generating calendar key: %w", err)
	}
	locked, err := key.Lock([]byte(passphrase))
	if err != nil {
		return "", fmt.Errorf("locking calendar key: %w", err)
	}
	armored, err := locked.Armor()
	if err != nil {
		return "", fmt.Errorf("armoring calendar key: %w", err)
	}
	return armored, nil
}
