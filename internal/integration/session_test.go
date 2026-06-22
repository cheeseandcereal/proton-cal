//go:build integration

package integration

import "testing"

// TestSession asserts the stored session works live: setup ran auth.UnlockKeys
// (fetching user and addresses), so a populated Unlocked proves the round-trip.
func TestSession(t *testing.T) {
	s := setup(t)

	if s.unlocked.User.ID == "" {
		t.Error("unlocked.User.ID is empty; GetUser did not return a user")
	}
	if len(s.unlocked.Addresses) == 0 {
		t.Error("no addresses on the account")
	}
	if len(s.unlocked.AddrKRs) < 1 {
		t.Errorf("expected at least 1 unlocked address keyring, got %d", len(s.unlocked.AddrKRs))
	}
}
