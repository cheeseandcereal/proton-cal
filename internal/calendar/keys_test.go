package calendar

import (
	"context"
	"strings"
	"testing"

	proton "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/internal/auth"
)

const (
	testCalendarID = "cal1"
	testMemberID   = "mem1"
	testAddressID  = "addr1"
)

// calFixtures holds in-test generated PGP material mirroring the real
// calendar key hierarchy.
type calFixtures struct {
	addrKR *crypto.KeyRing // unlocked "address" private keyring

	calPassphrase    []byte // passphrase locking the calendar key
	calPrivateArmor  string // calendar private key, locked with calPassphrase
	calPublicKR      *crypto.KeyRing
	encPassphrase    string // calPassphrase encrypted to addrKR, armored
	decoyEncrypted   string // calPassphrase encrypted to an unrelated key
	wrongPassKeyArm  string // calendar key locked with a DIFFERENT passphrase
	unlockedAccounts *auth.Unlocked
}

func newCalFixtures(t *testing.T) *calFixtures {
	t.Helper()

	addrKey, err := crypto.GenerateKey("addr", "me@example.com", "x25519", 0)
	if err != nil {
		t.Fatalf("generating address key: %v", err)
	}
	addrKR, err := crypto.NewKeyRing(addrKey)
	if err != nil {
		t.Fatalf("address keyring: %v", err)
	}

	calKey, err := crypto.GenerateKey("calendar", "calendar@proton.me", "x25519", 0)
	if err != nil {
		t.Fatalf("generating calendar key: %v", err)
	}
	passphrase := []byte("test-calendar-passphrase")
	lockedCal, err := calKey.Lock(passphrase)
	if err != nil {
		t.Fatalf("locking calendar key: %v", err)
	}
	calArmor, err := lockedCal.Armor()
	if err != nil {
		t.Fatalf("armoring calendar key: %v", err)
	}

	pubArmor, err := calKey.GetArmoredPublicKey()
	if err != nil {
		t.Fatalf("calendar public key: %v", err)
	}
	pubKey, err := crypto.NewKeyFromArmored(pubArmor)
	if err != nil {
		t.Fatalf("parsing calendar public key: %v", err)
	}
	pubKR, err := crypto.NewKeyRing(pubKey)
	if err != nil {
		t.Fatalf("calendar public keyring: %v", err)
	}

	encMsg, err := addrKR.Encrypt(crypto.NewPlainMessage(passphrase), nil)
	if err != nil {
		t.Fatalf("encrypting passphrase: %v", err)
	}
	encArmor, err := encMsg.GetArmored()
	if err != nil {
		t.Fatalf("armoring encrypted passphrase: %v", err)
	}

	// A passphrase copy encrypted to a key we do NOT hold.
	otherKey, err := crypto.GenerateKey("other", "other@example.com", "x25519", 0)
	if err != nil {
		t.Fatalf("generating unrelated key: %v", err)
	}
	otherKR, err := crypto.NewKeyRing(otherKey)
	if err != nil {
		t.Fatalf("unrelated keyring: %v", err)
	}
	decoyMsg, err := otherKR.Encrypt(crypto.NewPlainMessage(passphrase), nil)
	if err != nil {
		t.Fatalf("encrypting decoy passphrase: %v", err)
	}
	decoyArmor, err := decoyMsg.GetArmored()
	if err != nil {
		t.Fatalf("armoring decoy passphrase: %v", err)
	}

	// Calendar key locked with a different passphrase (for failure tests).
	wrongLocked, err := calKey.Lock([]byte("a-completely-different-passphrase"))
	if err != nil {
		t.Fatalf("locking calendar key with wrong passphrase: %v", err)
	}
	wrongArmor, err := wrongLocked.Armor()
	if err != nil {
		t.Fatalf("armoring wrong-passphrase key: %v", err)
	}

	return &calFixtures{
		addrKR:          addrKR,
		calPassphrase:   passphrase,
		calPrivateArmor: calArmor,
		calPublicKR:     pubKR,
		encPassphrase:   encArmor,
		decoyEncrypted:  decoyArmor,
		wrongPassKeyArm: wrongArmor,
		unlockedAccounts: &auth.Unlocked{
			Addresses: []proton.Address{{ID: testAddressID, Email: "Me@Example.com"}},
			AddrKRs:   map[string]*crypto.KeyRing{testAddressID: addrKR},
		},
	}
}

// bootstrap is a tweakable bootstrap body. Zero value is the happy path,
// seeding seams: a mixed-case other member (case-insensitive match), a foreign
// passphrase before ours, and a malformed key (skipped) before the real one.
func (f *calFixtures) bootstrap() map[string]any {
	return map[string]any{
		"Members": []map[string]any{
			{"ID": "other-member", "AddressID": "other-addr", "CalendarID": testCalendarID,
				"Email": "someone-else@example.com"},
			{"ID": testMemberID, "AddressID": testAddressID, "CalendarID": testCalendarID,
				"Email": "ME@EXAMPLE.COM", "Permissions": 112},
		},
		"Passphrase": map[string]any{
			"ID":    "pp1",
			"Flags": 0,
			"MemberPassphrases": []map[string]any{
				{"MemberID": "other-member", "Passphrase": f.decoyEncrypted, "Signature": ""},
				{"MemberID": testMemberID, "Passphrase": f.encPassphrase, "Signature": ""},
			},
		},
		"Keys": []map[string]any{
			{"ID": "k0", "CalendarID": testCalendarID, "PassphraseID": "pp1",
				"PrivateKey": "not an armored key", "Flags": 0},
			{"ID": "k1", "CalendarID": testCalendarID, "PassphraseID": "pp1",
				"PrivateKey": f.calPrivateArmor, "Flags": 3},
		},
		"CalendarSettings": map[string]any{
			"DefaultEventDuration":        30,
			"DefaultPartDayNotifications": []map[string]any{{"Type": 1, "Trigger": "-PT15M"}},
			"DefaultFullDayNotifications": []map[string]any{{"Type": 1, "Trigger": "-PT16H"}},
		},
	}
}

// serveBootstrap registers the single bootstrap endpoint with the given body.
func (f *calFixtures) serveBootstrap(mux *countingMux, body map[string]any) {
	mux.handleJSON(BootstrapPath(testCalendarID), body)
}

func TestKeychainUnlock(t *testing.T) {
	f := newCalFixtures(t)
	mux := newCountingMux()
	f.serveBootstrap(mux, f.bootstrap())
	client := newTestClient(t, mux)

	kc := NewKeychain(client, f.unlockedAccounts)
	access, err := kc.Unlock(context.Background(), Info{ID: testCalendarID})
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	if access.CalendarID != testCalendarID {
		t.Errorf("CalendarID = %q, want %q", access.CalendarID, testCalendarID)
	}
	if access.MemberID != testMemberID {
		t.Errorf("MemberID = %q, want %q", access.MemberID, testMemberID)
	}
	if access.AddressID != testAddressID {
		t.Errorf("AddressID = %q, want %q", access.AddressID, testAddressID)
	}
	if access.AddrKR != f.addrKR {
		t.Error("AddrKR is not the unlocked address keyring")
	}
	// Settings come back on the same bootstrap call.
	if access.Settings.DefaultEventDuration != 30 ||
		len(access.Settings.DefaultPartDayNotifications) != 1 ||
		access.Settings.DefaultPartDayNotifications[0].Trigger != "-PT15M" {
		t.Errorf("Settings = %+v, want part-day -PT15M / duration 30", access.Settings)
	}

	// The returned keyring must decrypt data encrypted to the calendar key.
	enc, err := f.calPublicKR.Encrypt(crypto.NewPlainMessageFromString("hello calendar"), nil)
	if err != nil {
		t.Fatalf("encrypting probe message: %v", err)
	}
	dec, err := access.KR.Decrypt(enc, nil, 0)
	if err != nil {
		t.Fatalf("decrypting probe message with unlocked calendar keyring: %v", err)
	}
	if got := dec.GetString(); got != "hello calendar" {
		t.Errorf("decrypted probe = %q, want %q", got, "hello calendar")
	}

	// Second Unlock must come from the cache: same access, no new API hit.
	path := BootstrapPath(testCalendarID)
	if got := mux.hitCount(path); got != 1 {
		t.Errorf("bootstrap hit %d times before second Unlock, want 1", got)
	}
	again, err := kc.Unlock(context.Background(), Info{ID: testCalendarID})
	if err != nil {
		t.Fatalf("second Unlock: %v", err)
	}
	if again != access {
		t.Error("second Unlock did not return the cached access")
	}
	if got := mux.hitCount(path); got != 1 {
		t.Errorf("bootstrap hit %d times after cached Unlock, want 1", got)
	}
}

func TestKeychainUnlockWithResolvedIdentity(t *testing.T) {
	f := newCalFixtures(t)
	mux := newCountingMux()
	f.serveBootstrap(mux, f.bootstrap())
	client := newTestClient(t, mux)

	// A List-resolved Info already carries our member identity: Unlock must
	// use it directly rather than re-resolving from the bootstrap members.
	info := Info{ID: testCalendarID, MemberID: testMemberID, AddressID: testAddressID}
	access, err := NewKeychain(client, f.unlockedAccounts).Unlock(context.Background(), info)
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if access.MemberID != testMemberID || access.AddressID != testAddressID {
		t.Errorf("identity = (%q, %q), want (%q, %q)", access.MemberID, access.AddressID, testMemberID, testAddressID)
	}
}

func TestKeychainUnlockMemberFallback(t *testing.T) {
	f := newCalFixtures(t)
	mux := newCountingMux()
	// No member email matches ours: fall back to the first member.
	body := f.bootstrap()
	body["Members"] = []map[string]any{
		{"ID": "other-member", "AddressID": "other-addr", "CalendarID": testCalendarID,
			"Email": "someone-else@example.com"},
	}
	body["Passphrase"] = map[string]any{
		"ID": "pp1",
		"MemberPassphrases": []map[string]any{
			{"MemberID": "other-member", "Passphrase": f.encPassphrase, "Signature": ""},
		},
	}
	f.serveBootstrap(mux, body)
	client := newTestClient(t, mux)

	access, err := NewKeychain(client, f.unlockedAccounts).Unlock(context.Background(), Info{ID: testCalendarID})
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if access.MemberID != "other-member" {
		t.Errorf("MemberID = %q, want fallback %q", access.MemberID, "other-member")
	}
	if access.AddressID != "other-addr" {
		t.Errorf("AddressID = %q, want fallback %q", access.AddressID, "other-addr")
	}
	// other-addr has no unlocked keyring: PrimaryAddrKR falls back to ours.
	if access.AddrKR != f.addrKR {
		t.Error("AddrKR did not fall back to the unlocked address keyring")
	}
}

func TestKeychainUnlockNoDecryptableKey(t *testing.T) {
	f := newCalFixtures(t)
	mux := newCountingMux()
	// Our member's passphrase is encrypted to a key we do not hold.
	body := f.bootstrap()
	body["Passphrase"] = map[string]any{
		"ID": "pp1",
		"MemberPassphrases": []map[string]any{
			{"MemberID": testMemberID, "Passphrase": f.decoyEncrypted, "Signature": ""},
		},
	}
	f.serveBootstrap(mux, body)
	client := newTestClient(t, mux)

	_, err := NewKeychain(client, f.unlockedAccounts).Unlock(context.Background(), Info{ID: testCalendarID})
	if err == nil {
		t.Fatal("Unlock: expected error when no address key can decrypt the passphrase")
	}
	if !strings.Contains(err.Error(), "no address key could decrypt") {
		t.Errorf("error = %q, want mention of undecryptable passphrase", err)
	}
	if !strings.Contains(err.Error(), testCalendarID) {
		t.Errorf("error = %q, want calendar ID %q", err, testCalendarID)
	}
}

func TestKeychainUnlockNoKeyUnlocks(t *testing.T) {
	f := newCalFixtures(t)
	mux := newCountingMux()
	// The served calendar key is locked with a different passphrase.
	body := f.bootstrap()
	body["Keys"] = []map[string]any{
		{"ID": "k1", "CalendarID": testCalendarID, "PassphraseID": "pp1",
			"PrivateKey": f.wrongPassKeyArm, "Flags": 3},
	}
	f.serveBootstrap(mux, body)
	client := newTestClient(t, mux)

	_, err := NewKeychain(client, f.unlockedAccounts).Unlock(context.Background(), Info{ID: testCalendarID})
	if err == nil {
		t.Fatal("Unlock: expected error when no calendar key unlocks")
	}
	if !strings.Contains(err.Error(), "failed to unlock any calendar keys") {
		t.Errorf("error = %q, want mention of failed key unlock", err)
	}
	if !strings.Contains(err.Error(), testCalendarID) {
		t.Errorf("error = %q, want calendar ID %q", err, testCalendarID)
	}
}

func TestKeychainUnlockNoMemberPassphrase(t *testing.T) {
	f := newCalFixtures(t)
	mux := newCountingMux()
	body := f.bootstrap()
	body["Passphrase"] = map[string]any{"ID": "pp1", "MemberPassphrases": []map[string]any{}}
	f.serveBootstrap(mux, body)
	client := newTestClient(t, mux)

	_, err := NewKeychain(client, f.unlockedAccounts).Unlock(context.Background(), Info{ID: testCalendarID})
	if err == nil {
		t.Fatal("Unlock: expected error when no member passphrases exist")
	}
	if !strings.Contains(err.Error(), "no member passphrase") {
		t.Errorf("error = %q, want mention of missing member passphrase", err)
	}
}
