package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/cheeseandcereal/proton-cal/internal/auth"
	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/config"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
)

// app bundles the dependencies shared by authenticated commands: the loaded
// config, the session store and an API client restored from the saved
// session.
type app struct {
	cfg    config.Config
	store  *config.SessionStore
	client *papi.Client
}

// newApp loads the config and restores an authenticated API client from the
// persisted session. A missing session yields a friendly "run login" error.
func newApp() (*app, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	store, err := config.NewSessionStore()
	if err != nil {
		return nil, fmt.Errorf("opening session store: %w", err)
	}
	client, err := papi.FromSession(store, cfg.EffectiveBaseURL())
	if errors.Is(err, config.ErrNoSession) {
		return nil, errors.New("not logged in; run `proton-cal login` first")
	} else if err != nil {
		return nil, fmt.Errorf("restoring session: %w", err)
	}
	return &app{cfg: cfg, store: store, client: client}, nil
}

// resolveAccess unlocks the account keys, resolves the calendar selector
// (explicit flag, else the configured default calendar, else the first
// calendar) and unlocks that calendar's keys.
func (a *app) resolveAccess(ctx context.Context, calSelector string) (calendar.Info, *calendar.Access, error) {
	unlocked, err := auth.UnlockKeys(ctx, a.client)
	if err != nil {
		return calendar.Info{}, nil, fmt.Errorf("unlocking keys: %w", err)
	}
	cals, err := calendar.List(ctx, a.client)
	if err != nil {
		return calendar.Info{}, nil, err
	}
	info, err := calendar.Resolve(cals, calSelector, a.cfg.DefaultCalendar)
	if err != nil {
		return calendar.Info{}, nil, err
	}
	if calSelector == "" {
		fmt.Fprintf(os.Stderr, "Using calendar: %s\n", info.Name)
	}
	access, err := calendar.NewKeychain(a.client, unlocked).Unlock(ctx, info)
	if err != nil {
		return calendar.Info{}, nil, fmt.Errorf("unlocking calendar keys: %w", err)
	}
	return info, access, nil
}

// effectiveTZ resolves a timezone: the explicit --tz flag value when given,
// else the configured / system timezone.
func (a *app) effectiveTZ(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return a.cfg.EffectiveTimezone()
}

// humanOut returns the stream for human-readable output: stderr when --json
// is active (stdout is reserved for the JSON document), stdout otherwise.
func humanOut() io.Writer {
	if jsonOutput {
		return os.Stderr
	}
	return os.Stdout
}

// printJSON writes v as indented JSON to stdout.
func printJSON(v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}
