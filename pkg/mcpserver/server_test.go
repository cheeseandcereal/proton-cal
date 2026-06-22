package mcpserver

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cheeseandcereal/proton-cal/pkg/config"
)

// connectTestClient wires the given server to an MCP client over the
// in-memory transport pair and returns the live client session.
func connectTestClient(t *testing.T, s *server) *mcp.ClientSession {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "proton-calendar", Version: "test"}, nil)
	s.register(srv)

	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	srvSess, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srvSess.Wait() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestServerExposesAllTools(t *testing.T) {
	cs := connectTestClient(t, failingServer(errors.New("unused")))

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := []string{"create_event", "delete_calendar", "delete_event", "get_calendar", "get_event", "list_calendars", "list_events", "update_calendar", "update_event"}
	if len(names) != len(want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tools = %v, want %v", names, want)
		}
	}
}

// callText calls a tool and returns its concatenated text content.
func callText(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

func TestToolErrorsAreToolResults(t *testing.T) {
	// A handler error must surface as an IsError tool RESULT (visible to
	// the model), never crash the server or become a protocol error.
	cs := connectTestClient(t, failingServer(errors.New("not logged in; run `proton-cal login` first")))

	text, isErr := callText(t, cs, "list_calendars", nil)
	if !isErr {
		t.Fatal("want IsError result")
	}
	if !strings.Contains(text, "proton-cal login") {
		t.Errorf("error text %q does not direct the user to login", text)
	}

	// Validation errors too - and the server keeps serving afterwards.
	cs2 := connectTestClient(t, stubServer(config.Config{Timezone: "UTC"}))
	text, isErr = callText(t, cs2, "update_event", map[string]any{
		"event_id":  "abc",
		"no_repeat": true,
		"repeat":    "daily",
	})
	if !isErr || !strings.Contains(text, "no-repeat cannot be combined") {
		t.Errorf("isErr=%v text=%q", isErr, text)
	}
	text, isErr = callText(t, cs2, "create_event", map[string]any{
		"summary": "X",
		"start":   "nope",
	})
	if !isErr || !strings.Contains(text, "invalid datetime") {
		t.Errorf("isErr=%v text=%q", isErr, text)
	}

	// delete_calendar confirm=false dry-runs and refuses, naming the target.
	// Needs an API-backed server so resolution succeeds offline.
	cs3 := connectTestClient(t, apiStubServer(config.Config{Timezone: "UTC"}, map[string]string{
		"/calendar/v1": `{"Calendars":[{"ID":"id-work","Type":0,"Members":[{"ID":"m1","Name":"Work","Color":"#112233"}]}]}`,
	}))
	text, isErr = callText(t, cs3, "delete_calendar", map[string]any{"calendar": "Work", "confirm": false})
	if !isErr || !strings.Contains(text, "confirm=true") || !strings.Contains(text, "id-work") {
		t.Errorf("delete_calendar without confirm: isErr=%v text=%q", isErr, text)
	}
}
