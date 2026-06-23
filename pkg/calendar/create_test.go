package calendar

import (
	"context"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	proton "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/pkg/auth"
	"github.com/cheeseandcereal/proton-cal/pkg/papi"
	"github.com/cheeseandcereal/proton-cal/pkg/pgp"
)

// recordedPost is one captured POST call.
type recordedPost struct {
	path string
	body any
}

// createAPI is a fake papi.API for Create: it records every POST, fills the
// create response with a fixed calendar ID, and can fail a chosen step.
type createAPI struct {
	posts      []recordedPost
	calID      string
	failOnPath string // when a POST hits this path, return failErr
	failErr    error
}

func (c *createAPI) Get(context.Context, string, url.Values, any) error { return nil }
func (c *createAPI) Put(context.Context, string, any, any) error        { return nil }
func (c *createAPI) Delete(context.Context, string, any) error          { return nil }

func (c *createAPI) Post(_ context.Context, path string, body, out any) error {
	c.posts = append(c.posts, recordedPost{path: path, body: body})
	if c.failOnPath != "" && path == c.failOnPath {
		return c.failErr
	}
	// The metadata POST (exact APIPath) returns the created calendar.
	if path == APIPath {
		if resp, ok := out.(*createResponse); ok {
			resp.Calendar = apiCalendar{
				ID:   c.calID,
				Type: 0,
				Members: []apiMember{{
					ID:        "mem-new",
					AddressID: testAddressID,
					Name:      "from-server",
				}},
			}
		}
	}
	return nil
}

// newCreateInput builds a CreateInput with a freshly generated address keyring.
func newCreateInput(t *testing.T) CreateInput {
	t.Helper()
	addrKey, err := crypto.GenerateKey("addr", "me@example.com", "x25519", 0)
	if err != nil {
		t.Fatalf("generating address key: %v", err)
	}
	addrKR, err := crypto.NewKeyRing(addrKey)
	if err != nil {
		t.Fatalf("address keyring: %v", err)
	}
	return CreateInput{
		Name:        "My New Calendar",
		Description: "desc",
		ColorHex:    "#F78400",
		AddressID:   testAddressID,
		AddrKR:      addrKR,
	}
}

func TestCreateTwoStepAndCryptoRoundTrip(t *testing.T) {
	api := &createAPI{calID: "newcal"}
	in := newCreateInput(t)

	info, err := Create(context.Background(), api, in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.ID != "newcal" {
		t.Errorf("info.ID = %q, want %q", info.ID, "newcal")
	}
	// Metadata comes from the server response (member entry), via newInfo.
	if info.Name != "from-server" {
		t.Errorf("info.Name = %q, want %q", info.Name, "from-server")
	}

	if len(api.posts) != 2 {
		t.Fatalf("got %d POSTs, want 2 (metadata + keys)", len(api.posts))
	}

	// Step 1: metadata body.
	if api.posts[0].path != APIPath {
		t.Errorf("step 1 path = %q, want %q", api.posts[0].path, APIPath)
	}
	meta := api.posts[0].body.(map[string]any)
	if meta["Name"] != in.Name || meta["Description"] != in.Description ||
		meta["Color"] != in.ColorHex || meta["AddressID"] != in.AddressID {
		t.Errorf("metadata body = %+v", meta)
	}
	if meta["Display"] != calendarDisplayVisible {
		t.Errorf("Display = %v, want %d", meta["Display"], calendarDisplayVisible)
	}

	// Step 2: setup-keys body.
	if api.posts[1].path != APIPath+"/newcal/keys" {
		t.Errorf("step 2 path = %q, want %q", api.posts[1].path, APIPath+"/newcal/keys")
	}
	setup := api.posts[1].body.(map[string]any)
	if setup["AddressID"] != in.AddressID {
		t.Errorf("setup AddressID = %v", setup["AddressID"])
	}
	priv, _ := setup["PrivateKey"].(string)
	if !strings.Contains(priv, "PGP PRIVATE KEY BLOCK") {
		t.Errorf("PrivateKey is not an armored private key: %q", priv)
	}
	sig, _ := setup["Signature"].(string)
	if !strings.Contains(sig, "PGP SIGNATURE") {
		t.Errorf("Signature is not armored: %q", sig)
	}
	pass, ok := setup["Passphrase"].(map[string]any)
	if !ok {
		t.Fatalf("Passphrase block type %T", setup["Passphrase"])
	}
	keyPacketB64, _ := pass["KeyPacket"].(string)
	dataPacketB64, _ := pass["DataPacket"].(string)
	if keyPacketB64 == "" || dataPacketB64 == "" {
		t.Fatalf("empty key/data packet: %+v", pass)
	}

	// Crypto round-trip: the generated private key must unlock with the
	// passphrase recovered from the split message, proving consistency.
	sk, err := pgp.DecryptSessionKey(keyPacketB64, in.AddrKR)
	if err != nil {
		t.Fatalf("DecryptSessionKey: %v", err)
	}
	dataPacket, err := base64.StdEncoding.DecodeString(dataPacketB64)
	if err != nil {
		t.Fatalf("decoding data packet: %v", err)
	}
	plain, err := sk.Decrypt(dataPacket)
	if err != nil {
		t.Fatalf("decrypting passphrase data packet: %v", err)
	}
	passphrase := plain.GetBinary()

	locked, err := crypto.NewKeyFromArmored(priv)
	if err != nil {
		t.Fatalf("parsing armored private key: %v", err)
	}
	if _, err := locked.Unlock(passphrase); err != nil {
		t.Fatalf("calendar key did not unlock with the recovered passphrase: %v", err)
	}
}

func TestCreateBadColorMapsFriendlyError(t *testing.T) {
	api := &createAPI{
		calID:      "newcal",
		failOnPath: APIPath,
		failErr:    &papi.Error{Status: 400, Code: codeColorNotAllowed, Message: "Color is not in allowed color list"},
	}
	_, err := Create(context.Background(), api, newCreateInput(t))
	if err == nil {
		t.Fatal("want error for bad color")
	}
	if !strings.Contains(err.Error(), "Proton palette color") {
		t.Errorf("error = %q, want palette-color hint", err)
	}
	// Only the metadata POST should have been attempted.
	if len(api.posts) != 1 {
		t.Errorf("got %d POSTs, want 1 (failed metadata)", len(api.posts))
	}
}

func TestCreateKeySetupFailureReportsKeylessCalendar(t *testing.T) {
	api := &createAPI{
		calID:      "newcal",
		failOnPath: APIPath + "/newcal/keys",
		failErr:    &papi.Error{Status: 500, Message: "boom"},
	}
	_, err := Create(context.Background(), api, newCreateInput(t))
	if err == nil {
		t.Fatal("want error when key setup fails")
	}
	if !strings.Contains(err.Error(), "newcal") {
		t.Errorf("error = %q, want the keyless calendar ID", err)
	}
	if !strings.Contains(err.Error(), ErrKeylessCalendar.Error()) {
		t.Errorf("error = %q, want ErrKeylessCalendar wrapping", err)
	}
}

func TestSigningAddressSelectsActiveUnlocked(t *testing.T) {
	addrKey, err := crypto.GenerateKey("addr", "me@example.com", "x25519", 0)
	if err != nil {
		t.Fatalf("generating address key: %v", err)
	}
	addrKR, err := crypto.NewKeyRing(addrKey)
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}

	unlocked := &auth.Unlocked{
		Addresses: []proton.Address{
			// Disabled address first: must be skipped even though it has a key.
			{ID: "disabled", Status: proton.AddressStatusDisabled, Send: true, Receive: true},
			// Active but no unlocked keyring: skipped.
			{ID: "nokey", Status: proton.AddressStatusEnabled, Send: true, Receive: true},
			// The one we want.
			{ID: "active", Status: proton.AddressStatusEnabled, Send: true, Receive: true},
		},
		AddrKRs: map[string]*crypto.KeyRing{
			"disabled": addrKR,
			"active":   addrKR,
		},
	}
	kc := NewKeychain(nil, unlocked)
	id, kr, err := kc.SigningAddress()
	if err != nil {
		t.Fatalf("SigningAddress: %v", err)
	}
	if id != "active" {
		t.Errorf("addressID = %q, want %q", id, "active")
	}
	if kr != addrKR {
		t.Error("keyring is not the active address's keyring")
	}
}

func TestSigningAddressNoneAvailable(t *testing.T) {
	unlocked := &auth.Unlocked{
		Addresses: []proton.Address{
			{ID: "disabled", Status: proton.AddressStatusDisabled, Send: true, Receive: true},
		},
		AddrKRs: map[string]*crypto.KeyRing{},
	}
	kc := NewKeychain(nil, unlocked)
	if _, _, err := kc.SigningAddress(); err == nil {
		t.Fatal("want error when no active address has a usable key")
	}
}
