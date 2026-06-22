package pgp

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ProtonMail/gopenpgp/v2/constants"
	"github.com/ProtonMail/gopenpgp/v2/crypto"
)

// genKeyRing generates a fresh unlocked x25519 key and returns private and
// public keyrings for it.
func genKeyRing(t *testing.T, name, email string) (privKR, pubKR *crypto.KeyRing) {
	t.Helper()

	key, err := crypto.GenerateKey(name, email, "x25519", 0)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	privKR, err = crypto.NewKeyRing(key)
	if err != nil {
		t.Fatalf("NewKeyRing(private): %v", err)
	}

	pubKey, err := key.ToPublic()
	if err != nil {
		t.Fatalf("ToPublic: %v", err)
	}
	pubKR, err = crypto.NewKeyRing(pubKey)
	if err != nil {
		t.Fatalf("NewKeyRing(public): %v", err)
	}

	return privKR, pubKR
}

func TestEncryptAndSignRoundTrip(t *testing.T) {
	calKR, calPubKR := genKeyRing(t, "calendar", "cal@example.com")
	addrKR, addrPubKR := genKeyRing(t, "address", "addr@example.com")

	plaintext := "BEGIN:VCALENDAR\r\nSUMMARY:secret\r\nEND:VCALENDAR"

	keyPacketB64, dataPacketB64, armoredSig, err := EncryptAndSign(plaintext, calPubKR, addrKR)
	if err != nil {
		t.Fatalf("EncryptAndSign: %v", err)
	}
	if keyPacketB64 == "" || dataPacketB64 == "" {
		t.Fatal("EncryptAndSign returned empty key or data packet")
	}
	if !strings.HasPrefix(armoredSig, "-----BEGIN PGP SIGNATURE-----") {
		t.Fatalf("signature is not armored: %q", armoredSig[:min(40, len(armoredSig))])
	}

	got, err := DecryptPart(dataPacketB64, keyPacketB64, calKR)
	if err != nil {
		t.Fatalf("DecryptPart: %v", err)
	}
	if got != plaintext {
		t.Fatalf("DecryptPart = %q, want %q", got, plaintext)
	}

	// The detached signature must verify over the plaintext with the
	// address PUBLIC keyring (DecryptPart itself never verifies).
	sig, err := crypto.NewPGPSignatureFromArmored(armoredSig)
	if err != nil {
		t.Fatalf("NewPGPSignatureFromArmored: %v", err)
	}
	plain := crypto.NewPlainMessage([]byte(plaintext))
	if err := addrPubKR.VerifyDetached(plain, sig, crypto.GetUnixTime()); err != nil {
		t.Fatalf("VerifyDetached: %v", err)
	}
}

func TestSessionKeyReuse(t *testing.T) {
	calKR, calPubKR := genKeyRing(t, "calendar", "cal@example.com")
	addrKR, _ := genKeyRing(t, "address", "addr@example.com")

	original := "the original plaintext"
	replacement := "a different plaintext"

	keyPacketB64, _, _, err := EncryptAndSign(original, calPubKR, addrKR)
	if err != nil {
		t.Fatalf("EncryptAndSign: %v", err)
	}

	sk, err := DecryptSessionKey(keyPacketB64, calKR)
	if err != nil {
		t.Fatalf("DecryptSessionKey: %v", err)
	}

	newDataB64, armoredSig, err := EncryptWithSessionKeyAndSign(replacement, sk, addrKR)
	if err != nil {
		t.Fatalf("EncryptWithSessionKeyAndSign: %v", err)
	}
	if !strings.HasPrefix(armoredSig, "-----BEGIN PGP SIGNATURE-----") {
		t.Fatal("rekey signature is not armored")
	}

	// The critical update-path invariant: the new data packet must decrypt
	// via the ORIGINAL key packet.
	got, err := DecryptPart(newDataB64, keyPacketB64, calKR)
	if err != nil {
		t.Fatalf("DecryptPart(new data, original key packet): %v", err)
	}
	if got != replacement {
		t.Fatalf("DecryptPart = %q, want %q", got, replacement)
	}
}

func TestSessionKeyAlgoIsAES256(t *testing.T) {
	calKR, calPubKR := genKeyRing(t, "calendar", "cal@example.com")
	addrKR, _ := genKeyRing(t, "address", "addr@example.com")

	keyPacketB64, _, _, err := EncryptAndSign("algo check", calPubKR, addrKR)
	if err != nil {
		t.Fatalf("EncryptAndSign: %v", err)
	}

	sk, err := DecryptSessionKey(keyPacketB64, calKR)
	if err != nil {
		t.Fatalf("DecryptSessionKey: %v", err)
	}
	if sk.Algo != constants.AES256 {
		t.Fatalf("session key algo = %q, want %q", sk.Algo, constants.AES256)
	}
	if len(sk.Key) != 32 {
		t.Fatalf("session key length = %d, want 32", len(sk.Key))
	}
}

func TestSignDetached(t *testing.T) {
	addrKR, addrPubKR := genKeyRing(t, "address", "addr@example.com")

	plaintext := "sign me please"

	armoredSig, err := SignDetached(plaintext, addrKR)
	if err != nil {
		t.Fatalf("SignDetached: %v", err)
	}
	if !strings.HasPrefix(armoredSig, "-----BEGIN PGP SIGNATURE-----") {
		t.Fatalf("signature is not armored: %q", armoredSig[:min(40, len(armoredSig))])
	}

	sig, err := crypto.NewPGPSignatureFromArmored(armoredSig)
	if err != nil {
		t.Fatalf("NewPGPSignatureFromArmored: %v", err)
	}

	good := crypto.NewPlainMessage([]byte(plaintext))
	if err := addrPubKR.VerifyDetached(good, sig, crypto.GetUnixTime()); err != nil {
		t.Fatalf("VerifyDetached(valid plaintext): %v", err)
	}

	tampered := crypto.NewPlainMessage([]byte(plaintext + " tampered"))
	if err := addrPubKR.VerifyDetached(tampered, sig, crypto.GetUnixTime()); err == nil {
		t.Fatal("VerifyDetached(tampered plaintext) succeeded, want error")
	}
}

func TestDecryptPartWrongKey(t *testing.T) {
	_, calPubKR := genKeyRing(t, "calendar", "cal@example.com")
	addrKR, _ := genKeyRing(t, "address", "addr@example.com")
	wrongKR, _ := genKeyRing(t, "other", "other@example.com")

	keyPacketB64, dataPacketB64, _, err := EncryptAndSign("secret", calPubKR, addrKR)
	if err != nil {
		t.Fatalf("EncryptAndSign: %v", err)
	}

	if _, err := DecryptPart(dataPacketB64, keyPacketB64, wrongKR); err == nil {
		t.Fatal("DecryptPart with wrong calendar key succeeded, want error")
	}
}

func TestDecryptPartGarbageInput(t *testing.T) {
	calKR, _ := genKeyRing(t, "calendar", "cal@example.com")

	// Invalid base64.
	if _, err := DecryptPart("!!!not-base64!!!", "", calKR); err == nil {
		t.Fatal("DecryptPart with garbage base64 data packet succeeded, want error")
	}
	if _, err := DecryptPart("aGVsbG8=", "!!!not-base64!!!", calKR); err == nil {
		t.Fatal("DecryptPart with garbage base64 key packet succeeded, want error")
	}

	// Valid base64 of non-PGP bytes.
	if _, err := DecryptPart("3q2+776tvu8=", "3q2+776tvu8=", calKR); err == nil {
		t.Fatal("DecryptPart with non-PGP bytes succeeded, want error")
	}
}

func TestDecryptSessionKeyGarbageInput(t *testing.T) {
	calKR, _ := genKeyRing(t, "calendar", "cal@example.com")

	if _, err := DecryptSessionKey("!!!not-base64!!!", calKR); err == nil {
		t.Fatal("DecryptSessionKey with garbage base64 succeeded, want error")
	}
	if _, err := DecryptSessionKey("3q2+776tvu8=", calKR); err == nil {
		t.Fatal("DecryptSessionKey with non-PGP bytes succeeded, want error")
	}
}

func TestDecryptPartMultiKeyKeyring(t *testing.T) {
	addrKR, _ := genKeyRing(t, "address", "addr@example.com")

	key1, err := crypto.GenerateKey("cal one", "cal1@example.com", "x25519", 0)
	if err != nil {
		t.Fatalf("GenerateKey(1): %v", err)
	}
	key2, err := crypto.GenerateKey("cal two", "cal2@example.com", "x25519", 0)
	if err != nil {
		t.Fatalf("GenerateKey(2): %v", err)
	}

	// Encrypt to the SECOND key only.
	pub2, err := key2.ToPublic()
	if err != nil {
		t.Fatalf("ToPublic: %v", err)
	}
	pub2KR, err := crypto.NewKeyRing(pub2)
	if err != nil {
		t.Fatalf("NewKeyRing(pub2): %v", err)
	}

	plaintext := "for the second key"
	keyPacketB64, dataPacketB64, _, err := EncryptAndSign(plaintext, pub2KR, addrKR)
	if err != nil {
		t.Fatalf("EncryptAndSign: %v", err)
	}

	// Calendar keyring holds BOTH private keys; key 2 is not first.
	calKR, err := crypto.NewKeyRing(key1)
	if err != nil {
		t.Fatalf("NewKeyRing(key1): %v", err)
	}
	if err := calKR.AddKey(key2); err != nil {
		t.Fatalf("AddKey(key2): %v", err)
	}

	got, err := DecryptPart(dataPacketB64, keyPacketB64, calKR)
	if err != nil {
		t.Fatalf("DecryptPart with multi-key keyring: %v", err)
	}
	if got != plaintext {
		t.Fatalf("DecryptPart = %q, want %q", got, plaintext)
	}

	sk, err := DecryptSessionKey(keyPacketB64, calKR)
	if err != nil {
		t.Fatalf("DecryptSessionKey with multi-key keyring: %v", err)
	}
	if sk.Algo != constants.AES256 {
		t.Fatalf("session key algo = %q, want %q", sk.Algo, constants.AES256)
	}
}

func TestDecryptPartEmptyKeyPacketFullMessage(t *testing.T) {
	calKR, calPubKR := genKeyRing(t, "calendar", "cal@example.com")

	plaintext := "a full pgp message, not split"
	msg, err := calPubKR.Encrypt(crypto.NewPlainMessage([]byte(plaintext)), nil)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	fullB64 := base64.StdEncoding.EncodeToString(msg.GetBinary())

	got, err := DecryptPart(fullB64, "", calKR)
	if err != nil {
		t.Fatalf("DecryptPart with empty key packet: %v", err)
	}
	if got != plaintext {
		t.Fatalf("DecryptPart = %q, want %q", got, plaintext)
	}
}
