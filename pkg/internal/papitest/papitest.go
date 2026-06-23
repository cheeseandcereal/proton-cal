// Package papitest provides shared test doubles for the papi.API surface so
// the CLI, MCP server and service tests don't each reimplement the same fake.
package papitest

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// Fake is a papi.API that serves canned GET bodies keyed by path; writes are
// no-ops. A missing path returns "{}". It satisfies papi.API structurally.
type Fake struct {
	Bodies map[string]string // GET path -> JSON response
}

// Get unmarshals the canned body for path into out (or "{}" if none).
func (f Fake) Get(_ context.Context, path string, _ url.Values, out any) error {
	body := f.Bodies[path]
	if body == "" {
		body = `{}`
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal([]byte(body), out)
}

// Put is a no-op; the fake serves reads only.
func (Fake) Put(context.Context, string, any, any) error { return nil }

// Post is a no-op; the fake serves reads only.
func (Fake) Post(context.Context, string, any, any) error { return nil }

// Delete is a no-op; the fake serves reads only.
func (Fake) Delete(context.Context, string, any) error { return nil }

// Ptr returns a pointer to v, for building optional/pointer test fixtures.
func Ptr[T any](v T) *T { return &v }

// CalSpec describes one calendar for CalListBody. Type 0 is a normal (owned)
// calendar; 1 subscribed; 2 holidays.
type CalSpec struct {
	ID   string
	Name string
	Type int
}

// CalListBody builds a GET /calendar/v1 response body for the given calendars,
// each with a single member carrying the name and a fixed color.
func CalListBody(cals ...CalSpec) string {
	var b strings.Builder
	b.WriteString(`{"Calendars":[`)
	for i, c := range cals {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"ID":"` + c.ID + `","Type":` + strconv.Itoa(c.Type) +
			`,"Members":[{"ID":"m-` + c.ID + `","Name":"` + c.Name + `","Color":"#112233"}]}`)
	}
	b.WriteString(`]}`)
	return b.String()
}
