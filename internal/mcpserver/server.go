// Package mcpserver implements the proton-cal MCP (Model Context Protocol)
// stdio server: it exposes Proton Calendar tools (list calendars, list /
// create / update / delete events) over stdin/stdout JSON-RPC so AI tools
// (Claude Code, opencode, ...) can use the calendar natively.
//
// Every tool takes an optional `calendar` argument (ID or name) instead of
// hardcoding the first calendar; the default comes from the configured
// default calendar, else the first calendar.
//
// stdout belongs to the MCP transport; all logging goes to stderr. The
// server never prompts: a missing session yields tool errors directing the
// user to run `proton-cal login`.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cheeseandcereal/proton-cal/internal/auth"
	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/config"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
)

// session bundles the lazily initialised authenticated state shared by all
// tool calls.
type session struct {
	cfg      config.Config
	client   *papi.Client
	cals     []calendar.Info
	keychain *calendar.Keychain
}

// server holds the lazily initialised session, guarded by a mutex so
// concurrent tool calls bootstrap exactly once. The bootstrap func is a
// field so tests can stub it.
type server struct {
	mu        sync.Mutex
	sess      *session
	bootstrap func(ctx context.Context) (*session, error)
}

func newServer() *server {
	return &server{bootstrap: bootstrapSession}
}

// bootstrapSession restores an authenticated, key-unlocked session from the
// saved login state. It never prompts: any missing piece becomes an error
// telling the user to run `proton-cal login` first.
func bootstrapSession(ctx context.Context) (*session, error) {
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
	unlocked, err := auth.UnlockKeys(ctx, client, store)
	if err != nil {
		return nil, fmt.Errorf("unlocking keys: %w", err)
	}
	cals, err := calendar.List(ctx, client)
	if err != nil {
		return nil, err
	}
	return &session{
		cfg:      cfg,
		client:   client,
		cals:     cals,
		keychain: calendar.NewKeychain(client, unlocked),
	}, nil
}

// session returns the cached session, bootstrapping it on first use.
func (s *server) session(ctx context.Context) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sess != nil {
		return s.sess, nil
	}
	sess, err := s.bootstrap(ctx)
	if err != nil {
		return nil, err
	}
	s.sess = sess
	return sess, nil
}

// resolveCalendar resolves a calendar selector (ID or name; "" = the
// configured default calendar, else the first calendar) against the cached
// calendar list.
func (sess *session) resolveCalendar(selector string) (calendar.Info, error) {
	return calendar.Resolve(sess.cals, selector, sess.cfg.DefaultCalendar)
}

// access resolves the calendar selector and unlocks that calendar's keys.
func (sess *session) access(ctx context.Context, selector string) (calendar.Info, *calendar.Access, error) {
	info, err := sess.resolveCalendar(selector)
	if err != nil {
		return calendar.Info{}, nil, err
	}
	acc, err := sess.keychain.Unlock(ctx, info.ID)
	if err != nil {
		return calendar.Info{}, nil, fmt.Errorf("unlocking calendar keys: %w", err)
	}
	return info, acc, nil
}

// Run starts the stdio MCP server (blocks until the client disconnects or
// ctx is cancelled). A client closing stdin (the normal MCP shutdown) is a
// clean exit, not an error.
func Run(ctx context.Context) error {
	s := newServer()
	srv := mcp.NewServer(&mcp.Implementation{Name: "proton-calendar", Version: "1.0.0"}, nil)
	s.register(srv)
	err := srv.Run(ctx, &mcp.StdioTransport{})
	if errors.Is(err, io.EOF) || errors.Is(err, mcp.ErrConnectionClosed) {
		return nil
	}
	return err
}
