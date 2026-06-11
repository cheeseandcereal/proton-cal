// Package mcpserver implements the proton-cal MCP (Model Context Protocol)
// stdio server: it exposes Proton Calendar tools (list calendars, list /
// create / update / delete events) over stdin/stdout JSON-RPC so AI tools
// (Claude Code, opencode, ...) can use the calendar natively.
//
// Every tool takes an optional `calendar` argument (ID or name) instead of
// hardcoding the first calendar; the default comes from the configured
// default calendar, else the first calendar. Time-taking tools take an
// optional `tz` override, defaulting to the configured timezone.
//
// stdout belongs to the MCP transport; all logging goes to stderr. The
// server never prompts: a missing session yields tool errors directing the
// user to run `proton-cal login`.
package mcpserver

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cheeseandcereal/proton-cal/internal/calsvc"
)

// serverVersion is the MCP implementation version advertised to clients.
const serverVersion = "1.0.0"

// server holds the lazily initialised service, guarded by a mutex so
// concurrent tool calls bootstrap exactly once. The bootstrap func is a
// field so tests can stub it.
type server struct {
	mu        sync.Mutex
	svc       *calsvc.Service
	bootstrap func() (*calsvc.Service, error)
}

func newServer() *server {
	return &server{bootstrap: func() (*calsvc.Service, error) { return calsvc.New(false) }}
}

// service returns the cached service, bootstrapping it on first use.
func (s *server) service() (*calsvc.Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.svc != nil {
		return s.svc, nil
	}
	svc, err := s.bootstrap()
	if err != nil {
		return nil, err
	}
	s.svc = svc
	return svc, nil
}

// Run starts the stdio MCP server (blocks until the client disconnects or
// ctx is cancelled). A client closing stdin (the normal MCP shutdown) is a
// clean exit, not an error.
func Run(ctx context.Context) error {
	s := newServer()
	srv := mcp.NewServer(&mcp.Implementation{Name: "proton-calendar", Version: serverVersion}, nil)
	s.register(srv)
	err := srv.Run(ctx, &mcp.StdioTransport{})
	if errors.Is(err, io.EOF) || errors.Is(err, mcp.ErrConnectionClosed) {
		return nil
	}
	return err
}
