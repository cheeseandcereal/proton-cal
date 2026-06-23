package calendar

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	proton "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/pkg/auth"
	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/papi"
	"github.com/cheeseandcereal/proton-cal/pkg/pgp"
)

// Settings holds a calendar's default reminder/duration settings (the
// CalendarSettings from the v2 bootstrap), applied to events carrying none.
type Settings struct {
	DefaultEventDuration        int                     `json:"DefaultEventDuration"`
	DefaultPartDayNotifications []caltypes.Notification `json:"DefaultPartDayNotifications"`
	DefaultFullDayNotifications []caltypes.Notification `json:"DefaultFullDayNotifications"`
	MakesUserBusy               int                     `json:"MakesUserBusy"`
}

// DefaultNotifications returns the default reminders to apply to an event
// with no reminders of its own, choosing the full-day or part-day set.
func (s Settings) DefaultNotifications(allDay bool) []caltypes.Notification {
	if allDay {
		return s.DefaultFullDayNotifications
	}
	return s.DefaultPartDayNotifications
}

// DefaultDuration returns the default event duration and whether it is usable;
// a non-positive value (the API's "unset" state) reports ok=false.
func (s Settings) DefaultDuration() (time.Duration, bool) {
	if s.DefaultEventDuration <= 0 {
		return 0, false
	}
	return time.Duration(s.DefaultEventDuration) * time.Minute, true
}

// Access is everything event code needs for one calendar.
type Access struct {
	CalendarID string
	KR         *crypto.KeyRing // unlocked calendar private keyring
	MemberID   string          // our member ID (for sync payloads)
	AddressID  string          // member's address (selects the signing key)
	AddrKR     *crypto.KeyRing // unlocked address keyring for signing
	Settings   Settings        // calendar default reminders/duration
}

// Keychain caches unlocked calendar keys for a session.
type Keychain struct {
	client   papi.API
	unlocked *auth.Unlocked

	mu    sync.Mutex
	cache map[string]*Access // calendar ID -> unlocked access
}

// NewKeychain creates a keychain over unlocked address keys.
func NewKeychain(client papi.API, unlocked *auth.Unlocked) *Keychain {
	return &Keychain{
		client:   client,
		unlocked: unlocked,
		cache:    make(map[string]*Access),
	}
}

// BootstrapPath returns GET /calendar/v2/{id}/bootstrap, which returns keys,
// passphrase, members and settings in one call.
func BootstrapPath(calendarID string) string {
	return "/calendar/v2/" + calendarID + "/bootstrap"
}

// bootstrapResponse is the wire shape of GET /calendar/v2/{id}/bootstrap.
type bootstrapResponse struct {
	Keys       []calendarKey   `json:"Keys"`
	Passphrase passphraseBlock `json:"Passphrase"`
	Members    []apiMember     `json:"Members"`
	Settings   Settings        `json:"CalendarSettings"`
}

type passphraseBlock struct {
	ID                string             `json:"ID"`
	Flags             int                `json:"Flags"`
	MemberPassphrases []memberPassphrase `json:"MemberPassphrases"`
}

type memberPassphrase struct {
	MemberID   string `json:"MemberID"`
	Passphrase string `json:"Passphrase"`
	Signature  string `json:"Signature"`
}

type calendarKey struct {
	ID           string `json:"ID"`
	CalendarID   string `json:"CalendarID"`
	PassphraseID string `json:"PassphraseID"`
	PrivateKey   string `json:"PrivateKey"`
	Flags        int    `json:"Flags"`
}

// FetchSettings reads a calendar's default settings from the bootstrap
// endpoint WITHOUT unlocking keys, for ops needing settings but not key
// material (avoiding the cost and unlock failure modes of a full Unlock).
func FetchSettings(ctx context.Context, client papi.API, calendarID string) (Settings, error) {
	var boot bootstrapResponse
	if err := client.Get(ctx, BootstrapPath(calendarID), nil, &boot); err != nil {
		return Settings{}, fmt.Errorf("fetching settings for calendar %s: %w", calendarID, err)
	}
	return boot.Settings, nil
}

// Unlock returns the calendar's unlocked keyring plus member context and
// settings, caching per calendar ID. One bootstrap GET feeds the pure-CPU
// unlock chain (member resolution, passphrase decrypt, key unlock); work runs
// outside the cache lock, so same-calendar unlocks may both run (idempotent).
func (k *Keychain) Unlock(ctx context.Context, info Info) (*Access, error) {
	calendarID := info.ID

	k.mu.Lock()
	access, ok := k.cache[calendarID]
	k.mu.Unlock()
	if ok {
		return access, nil
	}

	var boot bootstrapResponse
	if err := k.client.Get(ctx, BootstrapPath(calendarID), nil, &boot); err != nil {
		return nil, fmt.Errorf("fetching bootstrap for calendar %s: %w", calendarID, err)
	}

	memberID, addressID := info.MemberID, info.AddressID
	if memberID == "" {
		memberID, addressID = k.resolveMember(boot.Members)
	}

	passphrase, err := k.decryptPassphrase(calendarID, memberID, boot.Passphrase)
	if err != nil {
		return nil, err
	}

	calKR, err := unlockCalendarKeys(calendarID, boot.Keys, passphrase)
	if err != nil {
		return nil, err
	}

	addrKR, err := k.unlocked.PrimaryAddrKR(addressID)
	if err != nil {
		return nil, fmt.Errorf("selecting signing key for calendar %s: %w", calendarID, err)
	}

	access = &Access{
		CalendarID: calendarID,
		KR:         calKR,
		MemberID:   memberID,
		AddressID:  addressID,
		AddrKR:     addrKR,
		Settings:   boot.Settings,
	}
	k.mu.Lock()
	k.cache[calendarID] = access
	k.mu.Unlock()
	return access, nil
}

// SigningAddress selects the address whose key signs and receives a new
// calendar's passphrase: the first ACTIVE address (enabled, sending and
// receiving) with an unlocked keyring, mirroring the web client's
// getActiveAddresses[0]. It returns that address's ID and keyring. Unlike a
// calendar unlock there is no member to resolve yet, so this is the create
// path's way to reach a usable signing/encryption keyring.
func (k *Keychain) SigningAddress() (addressID string, addrKR *crypto.KeyRing, err error) {
	for _, addr := range k.unlocked.Addresses {
		if addr.Status != proton.AddressStatusEnabled || !bool(addr.Send) || !bool(addr.Receive) {
			continue
		}
		kr, ok := k.unlocked.AddrKRs[addr.ID]
		if !ok {
			continue
		}
		return addr.ID, kr, nil
	}
	return "", nil, errors.New("no active address with a usable key was found to create a calendar")
}

// Invalidate drops the cached Access so the next Unlock re-fetches the
// bootstrap, avoiding stale settings after a same-session write.
func (k *Keychain) Invalidate(calendarID string) {
	k.mu.Lock()
	delete(k.cache, calendarID)
	k.mu.Unlock()
}

// resolveMember finds OUR member entry by case-insensitive email match (the
// list may include other users on shared calendars), falling back to the first
// member, or empty strings when there are none.
func (k *Keychain) resolveMember(members []apiMember) (memberID, addressID string) {
	ourEmails := make(map[string]bool, len(k.unlocked.Addresses))
	for _, addr := range k.unlocked.Addresses {
		ourEmails[strings.ToLower(addr.Email)] = true
	}
	for _, m := range members {
		if ourEmails[strings.ToLower(m.Email)] {
			return m.ID, m.AddressID
		}
	}
	if len(members) > 0 {
		return members[0].ID, members[0].AddressID
	}
	return "", ""
}

// decryptPassphrase decrypts our member's passphrase entry (else the first) by
// trying every unlocked address keyring, since any may be the recipient. The
// detached signature is intentionally not verified (lenient by design).
func (k *Keychain) decryptPassphrase(calendarID, memberID string, pass passphraseBlock) ([]byte, error) {
	entries := pass.MemberPassphrases
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

// unlockCalendarKeys unlocks the calendar private keys with the passphrase
// into one keyring, erroring when none unlock.
func unlockCalendarKeys(calendarID string, keys []calendarKey, passphrase []byte) (*crypto.KeyRing, error) {
	armored := make([]string, 0, len(keys))
	for _, ck := range keys {
		armored = append(armored, ck.PrivateKey)
	}
	kr, err := pgp.UnlockKeyRing(armored, passphrase)
	if err != nil {
		return nil, fmt.Errorf("failed to unlock any calendar keys for calendar %s: %w", calendarID, err)
	}
	return kr, nil
}
