package auth

import (
	"context"
	"errors"
	"fmt"

	proton "github.com/ProtonMail/go-proton-api"
	"github.com/ProtonMail/gopenpgp/v2/crypto"

	"github.com/cheeseandcereal/proton-cal/internal/config"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
)

// Unlocked holds unlocked key material for a session.
type Unlocked struct {
	// User is the Proton user the session belongs to.
	User proton.User
	// Addresses are the user's addresses, in API order.
	Addresses []proton.Address
	// UserKR is the unlocked user keyring.
	UserKR *crypto.KeyRing
	// AddrKRs maps address ID to its unlocked keyring. Addresses whose keys
	// could not be unlocked are absent.
	AddrKRs map[string]*crypto.KeyRing
}

// UnlockKeys restores key material using the salted key passphrase stored at
// login: it fetches the user and addresses via the typed client and unlocks
// the user/address keyrings with proton.Unlock. A session without a stored
// salted key passphrase yields an error directing the user to run
// `proton-cal login`.
func UnlockKeys(ctx context.Context, client *papi.Client, store *config.SessionStore) (*Unlocked, error) {
	sess, err := store.Load()
	if err != nil {
		return nil, fmt.Errorf("loading session: %w", err)
	}
	if len(sess.SaltedKeyPass) == 0 {
		return nil, errors.New("session has no stored key passphrase; run `proton-cal login` to refresh it")
	}

	pc := client.Proton()

	user, err := pc.GetUser(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching user: %w", err)
	}
	addrs, err := pc.GetAddresses(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching addresses: %w", err)
	}

	userKR, addrKRs, err := proton.Unlock(user, addrs, sess.SaltedKeyPass, nil)
	if err != nil {
		return nil, fmt.Errorf("unlocking keys: %w", err)
	}

	return &Unlocked{
		User:      user,
		Addresses: addrs,
		UserKR:    userKR,
		AddrKRs:   addrKRs,
	}, nil
}

// PrimaryAddrKR returns the unlocked keyring for addressID, falling back to
// any unlocked address keyring when that address is unknown or its keys
// could not be unlocked. It returns an error when no address keyrings are
// unlocked at all.
func (u *Unlocked) PrimaryAddrKR(addressID string) (*crypto.KeyRing, error) {
	if kr, ok := u.AddrKRs[addressID]; ok {
		return kr, nil
	}
	// Deterministic fallback: first unlocked keyring in address order.
	for _, addr := range u.Addresses {
		if kr, ok := u.AddrKRs[addr.ID]; ok {
			return kr, nil
		}
	}
	// Last resort: any unlocked keyring (map order).
	for _, kr := range u.AddrKRs {
		return kr, nil
	}
	return nil, errors.New("no unlocked address keyrings available")
}
