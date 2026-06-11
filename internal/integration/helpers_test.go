//go:build integration

package integration

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/cheeseandcereal/proton-cal/internal/auth"
	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/config"
	"github.com/cheeseandcereal/proton-cal/internal/event"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
)

const (
	// summaryPrefix tags every event the suite creates. The sweep test
	// (TestZSweep) deletes any leftover event whose decrypted summary
	// contains it; nothing without the tag is ever touched.
	summaryPrefix = "proton-cal-test"

	// eventOffsetDays is how far in the future test events live.
	eventOffsetDays = 30

	// Sweep window bounds (days from now); a superset of every offset the
	// tests use so crashed runs always get cleaned up eventually.
	sweepStartDays = 25
	sweepEndDays   = 40

	skipMessage = "integration suite not configured: run `proton-cal login` and create internal/integration/config.toml (see config.example.toml)"
)

// testConfig is the shape of internal/integration/config.toml.
type testConfig struct {
	Calendars []string `toml:"calendars"`
}

// suite is the shared, lazily-initialized plumbing for all tests. It uses
// the REAL user config dir (a previous `proton-cal login` is required) and
// never writes to the session beyond go-proton-api's own token refreshes.
type suite struct {
	tc       testConfig
	cfg      config.Config
	store    *config.SessionStore
	client   *papi.Client
	unlocked *auth.Unlocked
	cals     []calendar.Info
	resolved []calendar.Info // tc.Calendars resolved, same order
	keychain *calendar.Keychain
	tzName   string
}

var (
	setupOnce   sync.Once
	setupSkip   string // non-empty: every test skips with this message
	setupErr    error  // non-nil: every test fails with this error
	sharedSuite *suite
)

// setup returns the shared suite, skipping the calling test when the suite
// is unconfigured (no config.toml / no stored session) and failing it on
// hard setup errors.
func setup(t *testing.T) *suite {
	t.Helper()
	setupOnce.Do(initSuite)
	if setupSkip != "" {
		t.Skip(setupSkip)
	}
	if setupErr != nil {
		t.Fatalf("integration suite setup failed: %v", setupErr)
	}
	return sharedSuite
}

func initSuite() {
	ctx := context.Background()

	// 1. Test config: missing file -> clean skip.
	data, err := os.ReadFile("config.toml")
	if errors.Is(err, os.ErrNotExist) {
		setupSkip = skipMessage
		return
	} else if err != nil {
		setupErr = fmt.Errorf("reading config.toml: %w", err)
		return
	}
	var tc testConfig
	if err := toml.Unmarshal(data, &tc); err != nil {
		setupErr = fmt.Errorf("parsing config.toml: %w", err)
		return
	}
	if len(tc.Calendars) == 0 {
		setupSkip = "internal/integration/config.toml lists no calendars; " + skipMessage
		return
	}

	// 2. Real user config + stored session: missing session -> clean skip.
	cfg, err := config.Load()
	if err != nil {
		setupErr = fmt.Errorf("loading CLI config: %w", err)
		return
	}
	store, err := config.NewSessionStore()
	if err != nil {
		setupErr = fmt.Errorf("opening session store: %w", err)
		return
	}
	sess, err := store.Load()
	if errors.Is(err, config.ErrNoSession) {
		setupSkip = skipMessage
		return
	} else if err != nil {
		setupErr = fmt.Errorf("loading session: %w", err)
		return
	}
	if len(sess.SaltedKeyPass) == 0 {
		setupSkip = "stored session has no key passphrase; " + skipMessage
		return
	}

	// 3. Client + unlocked keys + calendars.
	client, err := papi.FromSession(store, cfg.EffectiveBaseURL())
	if errors.Is(err, config.ErrNoSession) {
		setupSkip = skipMessage
		return
	} else if err != nil {
		setupErr = fmt.Errorf("restoring client from session: %w", err)
		return
	}
	unlocked, err := auth.UnlockKeys(ctx, client, store)
	if err != nil {
		setupErr = fmt.Errorf("unlocking keys (is the saved session still valid? re-run `proton-cal login`): %w", err)
		return
	}
	cals, err := calendar.List(ctx, client)
	if err != nil {
		setupErr = fmt.Errorf("listing calendars: %w", err)
		return
	}
	resolved := make([]calendar.Info, 0, len(tc.Calendars))
	for _, sel := range tc.Calendars {
		info, err := calendar.Resolve(cals, sel, "")
		if err != nil {
			setupErr = fmt.Errorf("resolving configured calendar %q: %w", sel, err)
			return
		}
		resolved = append(resolved, info)
	}

	sharedSuite = &suite{
		tc:       tc,
		cfg:      cfg,
		store:    store,
		client:   client,
		unlocked: unlocked,
		cals:     cals,
		resolved: resolved,
		keychain: calendar.NewKeychain(client, unlocked),
		tzName:   cfg.EffectiveTimezone(),
	}
}

// accessFor unlocks one calendar's keys (cached by the keychain).
func (s *suite) accessFor(t *testing.T, info calendar.Info) *calendar.Access {
	t.Helper()
	access, err := s.keychain.Unlock(context.Background(), info.ID)
	if err != nil {
		t.Fatalf("unlocking calendar %q (%s): %v", info.Name, info.ID, err)
	}
	return access
}

// newAccess returns the client and unlocked access for the FIRST configured
// calendar (one calendar is enough for the write-path tests), skipping when
// the suite is not configured.
func newAccess(t *testing.T) (*papi.Client, *calendar.Access) {
	t.Helper()
	s := setup(t)
	return s.client, s.accessFor(t, s.resolved[0])
}

// uniqueSummary builds a tagged, per-call-unique event summary, e.g.
// "proton-cal-test 1a2b3c4d lifecycle".
func uniqueSummary(label string) string {
	return fmt.Sprintf("%s %s %s", summaryPrefix, randomHex(4), label)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := crand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return hex.EncodeToString(b)
}

// futureWindow returns a [start, start+1h) slot ~30 days out: the next
// half-hour boundary plus a random whole-minute scatter inside a 2-hour
// window, so concurrent runs do not collide. Times are whole minutes, so
// unix timestamps round-trip exactly through the second-precise iCal
// serialization.
func futureWindow() (start, end time.Time) {
	base := time.Now().UTC().AddDate(0, 0, eventOffsetDays).Truncate(30 * time.Minute).Add(30 * time.Minute)
	start = base.Add(time.Duration(mrand.IntN(120)) * time.Minute)
	return start, start.Add(time.Hour)
}

// trackEvent registers a best-effort SmartDelete cleanup for an event the
// test created. Call the returned func once the event is known deleted so
// the cleanup does not double-delete.
func trackEvent(t *testing.T, client *papi.Client, access *calendar.Access, eventID string) (markDeleted func()) {
	t.Helper()
	var done bool
	t.Cleanup(func() {
		if done {
			return
		}
		if _, err := event.SmartDelete(context.Background(), client, access, eventID, 0); err != nil {
			t.Logf("cleanup: could not delete event %s: %v (TestZSweep should catch it)", eventID, err)
		}
	})
	return func() { done = true }
}

// getDecrypted fetches and decrypts one event row.
func getDecrypted(t *testing.T, client *papi.Client, access *calendar.Access, eventID string) *event.Event {
	t.Helper()
	raw, err := event.Get(context.Background(), client, access.CalendarID, eventID)
	if err != nil {
		t.Fatalf("Get(%s): %v", eventID, err)
	}
	ev, err := event.Decrypt(raw, access.KR)
	if err != nil {
		t.Fatalf("Decrypt(%s): %v", eventID, err)
	}
	return ev
}

// listUID expands [start, end) via ListWindow and returns only the
// occurrences backed by rows with the given iCal UID, failing the test if
// Results keep ListWindow's order (sorted by occurrence start).
func listUID(t *testing.T, client *papi.Client, access *calendar.Access, uid string, start, end time.Time, tzName string) []event.Listed {
	t.Helper()
	listed, err := event.ListWindow(context.Background(), client, access.KR, access.CalendarID, start.Unix(), end.Unix(), tzName)
	if err != nil {
		t.Fatalf("ListWindow: %v", err)
	}
	var out []event.Listed
	for _, l := range listed {
		if l.Occurrence.Event == nil || l.Occurrence.Event.UID != uid {
			continue
		}
		out = append(out, l)
	}
	return out
}

// ptr returns a pointer to v (for event.UpdateOptions fields).
func ptr[T any](v T) *T { return &v }
