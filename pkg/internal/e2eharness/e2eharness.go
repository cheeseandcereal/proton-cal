//go:build integration

// Package e2eharness holds the shared bootstrap for the live integration tests
// in the calsvc, cli and mcpserver packages. They all need the same thing: read
// pkg/integration/config.toml, skip cleanly when the suite is unconfigured, and
// otherwise build one authenticated, cache-bypassing calsvc.Service.
//
// Every event the live tests create carries the SummaryPrefix tag so the
// pkg/integration sweep can clean up a crashed run.
package e2eharness

import (
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/cheeseandcereal/proton-cal/pkg/calsvc"
	"github.com/cheeseandcereal/proton-cal/pkg/config"
)

// SummaryPrefix tags every event the live tests create.
const SummaryPrefix = "proton-cal-test"

// configPath is relative to each test package directory (all the e2e packages
// sit one level below pkg/, alongside pkg/integration).
const configPath = "../integration/config.toml"

type tomlConfig struct {
	Calendars []string `toml:"calendars"`
}

var (
	once sync.Once
	svc  *calsvc.Service
	cal  string // first configured calendar selector
	skip string
)

// LiveService returns a real, authenticated cache-bypassing Service and the
// first configured test calendar selector, skipping the test when the suite is
// not configured (no config.toml, no calendars, or no saved session). The
// service is built once and shared across all callers.
func LiveService(t *testing.T) (*calsvc.Service, string) {
	t.Helper()
	once.Do(func() {
		data, err := os.ReadFile(configPath)
		if errors.Is(err, os.ErrNotExist) {
			skip = "e2e not configured: see pkg/integration/README.md"
			return
		} else if err != nil {
			t.Fatalf("reading config.toml: %v", err)
		}
		var tc tomlConfig
		if err := toml.Unmarshal(data, &tc); err != nil {
			t.Fatalf("parsing config.toml: %v", err)
		}
		if len(tc.Calendars) == 0 {
			skip = "pkg/integration/config.toml lists no calendars"
			return
		}
		s, err := calsvc.New(true) // no cache: always hit the live API
		if errors.Is(err, config.ErrNoSession) {
			skip = "no saved session; run `proton-cal login`"
			return
		} else if err != nil {
			t.Fatalf("calsvc.New: %v", err)
		}
		svc = s
		cal = tc.Calendars[0]
	})
	if skip != "" {
		t.Skip(skip)
	}
	if svc == nil {
		t.Fatal("e2e service not initialised")
	}
	return svc, cal
}

// Summary returns a unique, sweep-recognisable event summary for label.
func Summary(label string) string {
	b := make([]byte, 4)
	if _, err := crand.Read(b); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%s %s %s", SummaryPrefix, hex.EncodeToString(b), label)
}

// FutureDate returns a YYYY-MM-DD date ~30 days out.
func FutureDate() string {
	return time.Now().UTC().AddDate(0, 0, 30).Format("2006-01-02")
}

// FutureSlot returns start/end "YYYY-MM-DD HH:MM" ~30 days out at a fixed wall
// time. Whole minutes keep the round trip exact.
func FutureSlot() (start, end string) {
	d := FutureDate()
	return d + " 09:00", d + " 09:30"
}
