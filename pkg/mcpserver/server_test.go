package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cheeseandcereal/proton-cal/pkg/config"
	"github.com/cheeseandcereal/proton-cal/pkg/internal/papitest"
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
	want := []string{"create_calendar", "create_event", "delete_calendar", "delete_event", "get_calendar", "get_event", "list_calendars", "list_events", "update_calendar", "update_event"}
	if len(names) != len(want) {
		t.Fatalf("tools = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tools = %v, want %v", names, want)
		}
	}
}

// list_events exposes a multi-calendar input: a "calendars" array and an
// "all_calendars" flag, replacing the old single "calendar" string.
func TestListEventsToolMultiCalendarSchema(t *testing.T) {
	cs := connectTestClient(t, failingServer(errors.New("unused")))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var schema any
	for _, tool := range res.Tools {
		if tool.Name == "list_events" {
			schema = tool.InputSchema
		}
	}
	if schema == nil {
		t.Fatal("list_events tool not found")
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc.Properties["calendars"]; !ok {
		t.Errorf("list_events must expose a calendars property; schema=%s", raw)
	}
	if _, ok := doc.Properties["all_calendars"]; !ok {
		t.Errorf("list_events must expose an all_calendars property; schema=%s", raw)
	}
	if _, ok := doc.Properties["calendar"]; ok {
		t.Errorf("list_events must not expose the old single calendar property; schema=%s", raw)
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

// TestListToolsAdvertiseObjectOutputSchema verifies the MCP spec fix: tools
// that return structured content advertise an object outputSchema, and the
// list tools wrap their results under a named field (events/calendars) rather
// than the bare array that violated the spec (issue #1).
func TestListToolsAdvertiseObjectOutputSchema(t *testing.T) {
	cs := connectTestClient(t, failingServer(errors.New("unused")))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// outputSchema, when present, must be a JSON object schema. get_event is the
	// one structured tool intentionally without a schema (polymorphic detail vs.
	// raw ICS), so it is allowed to omit it.
	noSchema := map[string]bool{"get_event": true}
	want := map[string]string{ // tool -> the property its array result is wrapped under
		"list_events":    "events",
		"list_calendars": "calendars",
	}
	for _, tool := range res.Tools {
		if tool.OutputSchema == nil {
			if noSchema[tool.Name] {
				continue
			}
			t.Errorf("%s: missing outputSchema", tool.Name)
			continue
		}
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatalf("%s: marshal outputSchema: %v", tool.Name, err)
		}
		var doc struct {
			Type       string                     `json:"type"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", tool.Name, err)
		}
		if doc.Type != "object" {
			t.Errorf("%s: outputSchema type = %q, want object; schema=%s", tool.Name, doc.Type, raw)
		}
		if key, ok := want[tool.Name]; ok {
			if _, found := doc.Properties[key]; !found {
				t.Errorf("%s: outputSchema must wrap its array under %q; schema=%s", tool.Name, key, raw)
			}
		}
	}
}

// TestToolAnnotations checks the read-only and destructive hints advertised in
// tools/list match each tool's behavior.
func TestToolAnnotations(t *testing.T) {
	cs := connectTestClient(t, failingServer(errors.New("unused")))
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]*mcp.ToolAnnotations)
	for _, tool := range res.Tools {
		got[tool.Name] = tool.Annotations
	}

	readOnly := []string{"list_calendars", "list_events", "get_calendar", "get_event"}
	for _, name := range readOnly {
		a := got[name]
		if a == nil || !a.ReadOnlyHint {
			t.Errorf("%s: want readOnlyHint=true, got %+v", name, a)
		}
	}
	// Deletes are destructive (DestructiveHint defaults to true when unset).
	for _, name := range []string{"delete_event", "delete_calendar"} {
		a := got[name]
		if a == nil || a.ReadOnlyHint {
			t.Errorf("%s: want a non-read-only destructive tool, got %+v", name, a)
		}
		if a != nil && a.DestructiveHint != nil && !*a.DestructiveHint {
			t.Errorf("%s: destructiveHint must not be false", name)
		}
	}
	// Creates/updates mutate but are non-destructive.
	for _, name := range []string{"create_event", "update_event", "create_calendar", "update_calendar"} {
		a := got[name]
		if a == nil || a.ReadOnlyHint {
			t.Errorf("%s: want non-read-only, got %+v", name, a)
		}
		if a == nil || a.DestructiveHint == nil || *a.DestructiveHint {
			t.Errorf("%s: want destructiveHint=false, got %+v", name, a)
		}
	}
}

// TestListCalendarsStructuredContentIsObject drives list_calendars over the
// client transport and asserts structuredContent is a JSON object wrapping the
// calendars array, not a bare array (the spec violation from issue #1).
func TestListCalendarsStructuredContentIsObject(t *testing.T) {
	cs := connectTestClient(t, apiStubServer(config.Config{Timezone: "UTC"}, map[string]string{
		"/calendar/v1": papitest.CalListBody(papitest.CalSpec{ID: "id-work", Name: "Work"}),
	}))
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_calendars"})
	if err != nil {
		t.Fatalf("CallTool(list_calendars): %v", err)
	}
	if res.IsError {
		t.Fatalf("list_calendars errored: %s", textResult2(res))
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structuredContent: %v", err)
	}
	// Must decode as an object (not an array). A bare array fails this.
	var obj struct {
		Calendars []json.RawMessage `json:"calendars"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("structuredContent is not an object: %v; raw=%s", err, raw)
	}
	if len(obj.Calendars) != 1 {
		t.Errorf("calendars = %d, want 1; raw=%s", len(obj.Calendars), raw)
	}
}

// textResult2 concatenates the text content blocks of a tool result.
func textResult2(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
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

	// create_calendar validates the name and color before any network use.
	text, isErr = callText(t, cs2, "create_calendar", map[string]any{"name": ""})
	if !isErr || !strings.Contains(text, "name is required") {
		t.Errorf("create_calendar empty name: isErr=%v text=%q", isErr, text)
	}
	text, isErr = callText(t, cs2, "create_calendar", map[string]any{"name": "Work", "color": "default"})
	if !isErr || !strings.Contains(text, "no inheritable default color") {
		t.Errorf("create_calendar default color: isErr=%v text=%q", isErr, text)
	}

	// delete_calendar confirm=false dry-runs and refuses, naming the target.
	// Needs an API-backed server so resolution succeeds offline.
	cs3 := connectTestClient(t, apiStubServer(config.Config{Timezone: "UTC"}, map[string]string{
		"/calendar/v1": papitest.CalListBody(papitest.CalSpec{ID: "id-work", Name: "Work"}),
	}))
	text, isErr = callText(t, cs3, "delete_calendar", map[string]any{"calendar": "Work", "confirm": false})
	if !isErr || !strings.Contains(text, "confirm=true") || !strings.Contains(text, "id-work") {
		t.Errorf("delete_calendar without confirm: isErr=%v text=%q", isErr, text)
	}
}
