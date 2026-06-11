package calendar

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/internal/auth"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
	"github.com/cheeseandcereal/proton-cal/internal/pgp"
)

// Access is everything event code needs for one calendar.
type Access struct {
	CalendarID string
	KR         *crypto.KeyRing // unlocked calendar private keyring
	MemberID   string          // our member ID (for sync payloads)
	AddressID  string          // member's address (selects the signing key)
	AddrKR     *crypto.KeyRing // unlocked address keyring for signing
}

// Keychain caches unlocked calendar keys for a session.
type Keychain struct {
	client   *papi.Client
	unlocked *auth.Unlocked

	mu    sync.Mutex
	cache map[string]*Access // calendar ID -> unlocked access
}

// NewKeychain creates a keychain over unlocked address keys.
func NewKeychain(client *papi.Client, unlocked *auth.Unlocked) *Keychain {
	return &Keychain{
		client:   client,
		unlocked: unlocked,
		cache:    make(map[string]*Access),
	}
}

// membersResponse is the wire shape of GET /calendar/v1/{id}/members.
type membersResponse struct {
	Members []apiMember `json:"Members"`
}

// passphraseResponse is the wire shape of GET /calendar/v1/{id}/passphrase.
type passphraseResponse struct {
	Passphrase struct {
		ID                string             `json:"ID"`
		Flags             int                `json:"Flags"`
		MemberPassphrases []memberPassphrase `json:"MemberPassphrases"`
	} `json:"Passphrase"`
}

type memberPassphrase struct {
	MemberID   string `json:"MemberID"`
	Passphrase string `json:"Passphrase"`
	Signature  string `json:"Signature"`
}

// keysResponse is the wire shape of GET /calendar/v1/{id}/keys.
type keysResponse struct {
	Keys []struct {
		ID           string `json:"ID"`
		CalendarID   string `json:"CalendarID"`
		PassphraseID string `json:"PassphraseID"`
		PrivateKey   string `json:"PrivateKey"`
		Flags        int    `json:"Flags"`
	} `json:"Keys"`
}

// Unlock returns the calendar's unlocked private keyring plus the member
// context needed for signing/writing, caching per calendar ID.
//
// The unlock chain is:
// member resolution → passphrase decrypt → calendar key unlock.
func (k *Keychain) Unlock(ctx context.Context, calendarID string) (*Access, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if access, ok := k.cache[calendarID]; ok {
		return access, nil
	}

	memberID, addressID, err := k.resolveMember(ctx, calendarID)
	if err != nil {
		return nil, err
	}

	passphrase, err := k.decryptPassphrase(ctx, calendarID, memberID)
	if err != nil {
		return nil, err
	}

	calKR, err := k.unlockCalendarKeys(ctx, calendarID, passphrase)
	if err != nil {
		return nil, err
	}

	addrKR, err := k.unlocked.PrimaryAddrKR(addressID)
	if err != nil {
		return nil, fmt.Errorf("selecting signing key for calendar %s: %w", calendarID, err)
	}

	access := &Access{
		CalendarID: calendarID,
		KR:         calKR,
		MemberID:   memberID,
		AddressID:  addressID,
		AddrKR:     addrKR,
	}
	k.cache[calendarID] = access
	return access, nil
}

// resolveMember finds OUR member entry on the calendar: the one whose Email
// matches one of our addresses' emails case-insensitively (the members list
// may include other users on shared calendars), falling back to the first
// member. Returns empty strings when the calendar has no members at all
// (lenient by design; passphrase selection then falls
// back to the first member passphrase).
func (k *Keychain) resolveMember(ctx context.Context, calendarID string) (memberID, addressID string, err error) {
	var resp membersResponse
	if err := k.client.Get(ctx, APIPath+"/"+calendarID+"/members", nil, &resp); err != nil {
		return "", "", fmt.Errorf("fetching members for calendar %s: %w", calendarID, err)
	}

	ourEmails := make(map[string]bool, len(k.unlocked.Addresses))
	for _, addr := range k.unlocked.Addresses {
		ourEmails[strings.ToLower(addr.Email)] = true
	}

	for _, m := range resp.Members {
		if ourEmails[strings.ToLower(m.Email)] {
			return m.ID, m.AddressID, nil
		}
	}
	if len(resp.Members) > 0 {
		return resp.Members[0].ID, resp.Members[0].AddressID, nil
	}
	return "", "", nil
}

// decryptPassphrase fetches the calendar passphrase and decrypts our
// member's entry (falling back to the first entry) by trying every unlocked
// address keyring - the passphrase may be encrypted to any of them. The
// detached signature is intentionally not verified (lenient by design;
// its absence or failure is non-fatal).
func (k *Keychain) decryptPassphrase(ctx context.Context, calendarID, memberID string) ([]byte, error) {
	var resp passphraseResponse
	if err := k.client.Get(ctx, APIPath+"/"+calendarID+"/passphrase", nil, &resp); err != nil {
		return nil, fmt.Errorf("fetching passphrase for calendar %s: %w", calendarID, err)
	}

	entries := resp.Passphrase.MemberPassphrases
	var mp *memberPassphrase
	for i := range entries {
		if entries[i].MemberID == memberID {
			mp = &entries[i]
			break
		}
	}
	if mp == nil && len(entries) > 0 {
		mp = &entries[0]
	}
	if mp == nil {
		return nil, fmt.Errorf("no member passphrase found for calendar %s", calendarID)
	}

	// Try address keyrings in API address order for determinism.
	for _, addr := range k.unlocked.Addresses {
		kr, ok := k.unlocked.AddrKRs[addr.ID]
		if !ok {
			continue
		}
		dec, err := pgp.DecryptArmored(mp.Passphrase, kr)
		if err != nil {
			continue
		}
		return dec, nil
	}
	return nil, fmt.Errorf("no address key could decrypt the passphrase for calendar %s", calendarID)
}

// unlockCalendarKeys fetches the calendar's private keys and unlocks each
// with the decrypted passphrase, collecting all that unlock into one
// keyring. It errors when none unlock.
func (k *Keychain) unlockCalendarKeys(ctx context.Context, calendarID string, passphrase []byte) (*crypto.KeyRing, error) {
	var resp keysResponse
	if err := k.client.Get(ctx, APIPath+"/"+calendarID+"/keys", nil, &resp); err != nil {
		return nil, fmt.Errorf("fetching keys for calendar %s: %w", calendarID, err)
	}

	armored := make([]string, 0, len(resp.Keys))
	for _, ck := range resp.Keys {
		armored = append(armored, ck.PrivateKey)
	}
	kr, err := pgp.UnlockKeyRing(armored, passphrase)
	if err != nil {
		return nil, fmt.Errorf("failed to unlock any calendar keys for calendar %s: %w", calendarID, err)
	}
	return kr, nil
}
