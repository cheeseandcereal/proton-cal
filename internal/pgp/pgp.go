// Package pgp implements the crypto layer for Proton Calendar events.
//
// Proton Calendar events carry iCalendar fragments in four parts; two are
// PGP-signed plaintext and two are PGP-encrypted (and signed over the
// plaintext). Encrypted parts are stored as a base64 "key packet" (a PKESK
// packet encrypting an AES-256 session key to the calendar public key),
// held once per event, plus per-part base64 "data packets" (SEIPD packets).
//
// Updates must reuse the event's existing session keys, because the server
// keeps the original key packets; see EncryptWithSessionKeyAndSign.
//
// This package knows nothing about HTTP or iCalendar; it only handles the
// PGP operations.
package pgp

import (
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/ProtonMail/gopenpgp/v2/crypto"
)

// SignDetached creates an armored detached signature over plaintext bytes,
// signed as binary-mode literal data (sig type 0x00), matching the web client.
func SignDetached(plaintext string, signingKR *crypto.KeyRing) (string, error) {
	sig, err := signingKR.SignDetached(crypto.NewPlainMessage([]byte(plaintext)))
	if err != nil {
		return "", fmt.Errorf("pgp: signing plaintext: %w", err)
	}
	armored, err := sig.GetArmored()
	if err != nil {
		return "", fmt.Errorf("pgp: armoring signature: %w", err)
	}
	return armored, nil
}

// EncryptAndSign encrypts plaintext to the calendar keyring with a fresh
// AES-256 session key and signs the plaintext (not ciphertext) with the address
// keyring. Returns base64 key packet (PKESK), base64 data packet (SEIPD) and
// the armored detached signature.
func EncryptAndSign(plaintext string, recipientKR, signingKR *crypto.KeyRing) (keyPacketB64, dataPacketB64, armoredSig string, err error) {
	// GenerateSessionKey uses the default cipher, which is AES-256
	// (constants.AES256) in gopenpgp v2.
	sk, err := crypto.GenerateSessionKey()
	if err != nil {
		return "", "", "", fmt.Errorf("pgp: generating session key: %w", err)
	}

	keyPacket, err := recipientKR.EncryptSessionKey(sk)
	if err != nil {
		return "", "", "", fmt.Errorf("pgp: encrypting session key: %w", err)
	}

	dataPacketB64, armoredSig, err = EncryptWithSessionKeyAndSign(plaintext, sk, signingKR)
	if err != nil {
		return "", "", "", err
	}

	return base64.StdEncoding.EncodeToString(keyPacket), dataPacketB64, armoredSig, nil
}

// DecryptSessionKey decrypts a base64-encoded key packet (PKESK) with the
// calendar keyring and returns the contained session key.
func DecryptSessionKey(keyPacketB64 string, calKR *crypto.KeyRing) (*crypto.SessionKey, error) {
	keyPacket, err := base64.StdEncoding.DecodeString(keyPacketB64)
	if err != nil {
		return nil, fmt.Errorf("pgp: decoding key packet base64: %w", err)
	}
	sk, err := calKR.DecryptSessionKey(keyPacket)
	if err != nil {
		return nil, fmt.Errorf("pgp: decrypting session key: %w", err)
	}
	return sk, nil
}

// EncryptWithSessionKeyAndSign encrypts plaintext with an existing session key
// (data packet only, no key packet) and signs the plaintext. Used for updates,
// which MUST reuse the original session key since the server keeps the key packet.
func EncryptWithSessionKeyAndSign(plaintext string, sk *crypto.SessionKey, signingKR *crypto.KeyRing) (dataPacketB64, armoredSig string, err error) {
	armoredSig, err = SignDetached(plaintext, signingKR)
	if err != nil {
		return "", "", err
	}

	dataPacket, err := sk.Encrypt(crypto.NewPlainMessage([]byte(plaintext)))
	if err != nil {
		return "", "", fmt.Errorf("pgp: encrypting plaintext with session key: %w", err)
	}

	return base64.StdEncoding.EncodeToString(dataPacket), armoredSig, nil
}

// DecryptPart decrypts one event part (base64 data packet + key packet) with the
// calendar keyring. No signature verification: other members' signatures can't be
// verified with our keys. Empty keyPacketB64 means the data packet is a full message.
func DecryptPart(dataPacketB64, keyPacketB64 string, calKR *crypto.KeyRing) (string, error) {
	dataPacket, err := base64.StdEncoding.DecodeString(dataPacketB64)
	if err != nil {
		return "", fmt.Errorf("pgp: decoding data packet base64: %w", err)
	}

	var msg *crypto.PGPMessage
	if keyPacketB64 == "" {
		msg = crypto.NewPGPMessage(dataPacket)
	} else {
		keyPacket, err := base64.StdEncoding.DecodeString(keyPacketB64)
		if err != nil {
			return "", fmt.Errorf("pgp: decoding key packet base64: %w", err)
		}
		msg = crypto.NewPGPSplitMessage(keyPacket, dataPacket).GetPGPMessage()
	}

	// nil verify keyring and verifyTime 0 disable signature verification.
	plain, err := calKR.Decrypt(msg, nil, 0)
	if err != nil {
		return "", fmt.Errorf("pgp: decrypting event part: %w", err)
	}
	// Use the raw decrypted bytes: PlainMessage.GetString would normalize
	// CRLF to LF, which would corrupt iCalendar payloads.
	return string(plain.GetBinary()), nil
}

// DecryptArmored decrypts an armored PGP message, returning raw plaintext bytes.
// No signature verification (lenient, like all read paths in this package).
func DecryptArmored(armored string, kr *crypto.KeyRing) ([]byte, error) {
	msg, err := crypto.NewPGPMessageFromArmored(armored)
	if err != nil {
		return nil, fmt.Errorf("pgp: parsing armored message: %w", err)
	}
	// nil verify keyring and verifyTime 0 disable signature verification.
	plain, err := kr.Decrypt(msg, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("pgp: decrypting message: %w", err)
	}
	return plain.GetBinary(), nil
}

// UnlockKeyRing unlocks armored private keys with a shared passphrase into one
// keyring. Lenient: keys that fail are skipped; errors only if none unlock.
func UnlockKeyRing(armoredKeys []string, passphrase []byte) (*crypto.KeyRing, error) {
	kr, err := crypto.NewKeyRing(nil)
	if err != nil {
		return nil, fmt.Errorf("pgp: creating keyring: %w", err)
	}
	for _, armored := range armoredKeys {
		locked, err := crypto.NewKeyFromArmored(armored)
		if err != nil {
			continue
		}
		key, err := locked.Unlock(passphrase)
		if err != nil {
			continue
		}
		if err := kr.AddKey(key); err != nil {
			continue
		}
	}
	if kr.CountEntities() == 0 {
		return nil, errors.New("pgp: no key could be unlocked")
	}
	return kr, nil
}
