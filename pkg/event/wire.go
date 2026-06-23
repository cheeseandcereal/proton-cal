package event

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"

	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/papi"
	"golang.org/x/sync/errgroup"
)

const pageSize = 100

// eventBody is the Event object of a sync payload. Field presence is significant
// (see docs/api.md): content arrays must serialize as [] (never null).
// Notifications/Color/Attendees mirror formatData: null/null/[] on create; on
// update they carry existing values so the sync doesn't clear reminders/RSVP rows.
type eventBody struct {
	Permissions           int                  `json:"Permissions"`
	SharedKeyPacket       string               `json:"SharedKeyPacket,omitempty"`
	CalendarKeyPacket     string               `json:"CalendarKeyPacket,omitempty"`
	SharedEventContent    []caltypes.EventPart `json:"SharedEventContent"`
	CalendarEventContent  []caltypes.EventPart `json:"CalendarEventContent"`
	AttendeesEventContent []caltypes.EventPart `json:"AttendeesEventContent"`
	Attendees             json.RawMessage      `json:"Attendees"`
	Notifications         json.RawMessage      `json:"Notifications"`
	Color                 json.RawMessage      `json:"Color"`
}

// attendeeClear is one entry of the clear Attendees array on an update body
// (token + live RSVP status); Comment preserved verbatim as raw JSON (null when absent).
type attendeeClear struct {
	Token   string          `json:"Token"`
	Status  int             `json:"Status"`
	Comment json.RawMessage `json:"Comment"`
}

// syncEventReq is one entry of the sync Events array: create (Overwrite +
// Event), update (ID + Event) or delete (ID only).
type syncEventReq struct {
	ID        string     `json:"ID,omitempty"`
	Overwrite *int       `json:"Overwrite,omitempty"`
	Event     *eventBody `json:"Event,omitempty"`
}

// syncReq is the PUT /calendar/v1/{calID}/events/sync payload. IsImport is
// present (0) only on creates.
type syncReq struct {
	MemberID string         `json:"MemberID"`
	IsImport *int           `json:"IsImport,omitempty"`
	Events   []syncEventReq `json:"Events"`
}

// syncResp is the sync endpoint response.
type syncResp struct {
	Code      int `json:"Code"`
	Responses []struct {
		Index    int `json:"Index"`
		Response struct {
			Code  int                `json:"Code"`
			Error string             `json:"Error"`
			Event *caltypes.RawEvent `json:"Event"`
		} `json:"Response"`
	} `json:"Responses"`
}

// firstError returns the response's failure: the first per-event reply's error,
// else the top-level code, nil on success.
func (r *syncResp) firstError() error {
	if len(r.Responses) == 0 {
		// Deletes return only a top-level code.
		if r.Code == papi.CodeSuccess || r.Code == papi.CodeSuccessMulti {
			return nil
		}
		return fmt.Errorf("sync failed: code %d", r.Code)
	}
	resp := r.Responses[0].Response
	if resp.Code != papi.CodeSuccess {
		if resp.Error != "" {
			return fmt.Errorf("sync failed: code %d: %s", resp.Code, resp.Error)
		}
		return fmt.Errorf("sync failed: code %d", resp.Code)
	}
	return nil
}

// firstEvent interprets the first per-event reply: (echoed event, nil) on success
// (event may be nil - updates don't always echo it), or the error from firstError.
func (r *syncResp) firstEvent() (*caltypes.RawEvent, error) {
	if err := r.firstError(); err != nil {
		return nil, err
	}
	if len(r.Responses) == 0 {
		return nil, nil
	}
	return r.Responses[0].Response.Event, nil
}

func putSync(ctx context.Context, client papi.API, calendarID string, payload syncReq) (*syncResp, error) {
	var resp syncResp
	if err := client.Put(ctx, calendar.APIPath+"/"+calendarID+"/events/sync", payload, &resp); err != nil {
		return nil, fmt.Errorf("calendar %s: sync: %w", calendarID, err)
	}
	return &resp, nil
}

type eventsResponse struct {
	Events []*caltypes.RawEvent `json:"Events"`
	// More is the page cursor on Type-scoped queries (1 = another page follows).
	More int `json:"More"`
}

// Windowed-query constants. /events honours Start/End only with a Type
// supplied (see docs/api.md); one paginated stream per Type, all four in parallel.
const (
	// queryTypePartDayInside selects timed events starting inside the window.
	queryTypePartDayInside = 0
	// queryTypePartDayBefore selects timed events starting before the window
	// but extending/recurring into it.
	queryTypePartDayBefore = 1
	// queryTypeFullDayInside selects all-day events starting inside the window.
	queryTypeFullDayInside = 2
	// queryTypeFullDayBefore selects all-day events starting before the window but
	// extending/recurring into it; surfaces recurring masters predating the window.
	queryTypeFullDayBefore = 3

	// maxWindowSeconds is the largest Start..End span the server accepts (93 days);
	// wider spans are rejected with code 2000 "Time window is too big".
	maxWindowSeconds = 93 * 86400

	// windowPadSeconds widens each window end by a day: the server buckets by
	// timezone-local start/end, so slack avoids missing all-day/boundary rows.
	windowPadSeconds = 86400
)

// queryTypes is the full set the server partitions events across; all must be
// queried to see every row overlapping the window.
var queryTypes = [...]int{
	queryTypePartDayInside,
	queryTypePartDayBefore,
	queryTypeFullDayInside,
	queryTypeFullDayBefore,
}

// maxConcurrentChunks bounds the parallel per-chunk-per-Type page streams
// (all to one host over HTTP/2; modest to stay clear of rate limits).
const maxConcurrentChunks = 6

// query fetches the raw rows a calendar exposes for [start, end): one paginated
// stream per Type concurrently, splitting over-wide windows into maxWindowSeconds
// chunks with windowPadSeconds slack. Rows are deduped by ID and sorted by
// StartTime; recurring masters always survive (expansion happens later). See docs/api.md.
func query(ctx context.Context, client papi.API, calendarID string, start, end int64, tzName string) ([]*caltypes.RawEvent, error) {
	if end <= start {
		return nil, nil
	}
	qStart := start - windowPadSeconds
	if qStart < 0 {
		qStart = 0
	}
	qEnd := end + windowPadSeconds

	// One unit of work = (chunk, Type). Each is an independent More-cursor
	// page stream.
	type unit struct {
		start, end int64
		typ        int
	}
	var units []unit
	for s := qStart; s < qEnd; s += maxWindowSeconds {
		e := min(s+maxWindowSeconds, qEnd)
		for _, typ := range queryTypes {
			units = append(units, unit{start: s, end: e, typ: typ})
		}
	}

	results := make([][]*caltypes.RawEvent, len(units))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrentChunks)
	for i, u := range units {
		g.Go(func() error {
			rows, err := queryStream(gctx, client, calendarID, u.start, u.end, tzName, u.typ)
			if err != nil {
				return err
			}
			results[i] = rows
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Deduplicate by ID across chunks/Types, preserving the first sighting.
	seen := make(map[string]struct{})
	out := make([]*caltypes.RawEvent, 0)
	for _, rows := range results {
		for _, ev := range rows {
			if _, dup := seen[ev.ID]; dup {
				continue
			}
			seen[ev.ID] = struct{}{}
			out = append(out, ev)
		}
	}
	slices.SortStableFunc(out, func(a, b *caltypes.RawEvent) int { return cmp.Compare(a.StartTime, b.StartTime) })
	return out, nil
}

// queryStream pages one (window, Type) slice via the More cursor and returns
// every row across its pages.
func queryStream(ctx context.Context, client papi.API, calendarID string, start, end int64, tzName string, typ int) ([]*caltypes.RawEvent, error) {
	var all []*caltypes.RawEvent
	for page := 0; ; page++ {
		q := url.Values{}
		q.Set("Start", strconv.FormatInt(start, 10))
		q.Set("End", strconv.FormatInt(end, 10))
		if tzName != "" {
			q.Set("Timezone", tzName)
		}
		q.Set("Type", strconv.Itoa(typ))
		q.Set("Page", strconv.Itoa(page))
		q.Set("PageSize", strconv.Itoa(pageSize))

		var resp eventsResponse
		if err := client.Get(ctx, calendar.APIPath+"/"+calendarID+"/events", q, &resp); err != nil {
			return nil, fmt.Errorf("calendar %s: listing events: %w", calendarID, err)
		}
		all = append(all, resp.Events...)
		if resp.More != 1 {
			return all, nil
		}
	}
}

// Get fetches a single raw event.
func Get(ctx context.Context, client papi.API, calendarID, eventID string) (*caltypes.RawEvent, error) {
	var raw json.RawMessage
	if err := client.Get(ctx, calendar.APIPath+"/"+calendarID+"/events/"+eventID, nil, &raw); err != nil {
		return nil, fmt.Errorf("calendar %s: fetching event %s: %w", calendarID, eventID, err)
	}
	// Standard shape is {"Event": {...}}; tolerate a bare event object.
	var envelope struct {
		Event *caltypes.RawEvent `json:"Event"`
	}
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Event != nil && envelope.Event.ID != "" {
		return envelope.Event, nil
	}
	var bare caltypes.RawEvent
	if err := json.Unmarshal(raw, &bare); err != nil || bare.ID == "" {
		return nil, fmt.Errorf("calendar %s: event %s: unrecognized response shape", calendarID, eventID)
	}
	return &bare, nil
}

// GetByUID fetches all raw rows sharing an iCal UID (master + exceptions),
// filtered server-side and paginated on the More cursor so multi-page series
// are returned in full.
func GetByUID(ctx context.Context, client papi.API, calendarID, uid string) ([]*caltypes.RawEvent, error) {
	var all []*caltypes.RawEvent
	for page := 0; ; page++ {
		q := url.Values{}
		q.Set("UID", uid)
		q.Set("Page", strconv.Itoa(page))
		q.Set("PageSize", strconv.Itoa(pageSize))
		var resp eventsResponse
		if err := client.Get(ctx, calendar.APIPath+"/"+calendarID+"/events", q, &resp); err != nil {
			return nil, fmt.Errorf("calendar %s: fetching events by UID: %w", calendarID, err)
		}
		all = append(all, resp.Events...)
		if resp.More != 1 {
			return all, nil
		}
	}
}

// deleteRows deletes raw event rows by ID in a single sync call.
func deleteRows(ctx context.Context, client papi.API, calendarID string, eventIDs []string, memberID string) error {
	reqs := make([]syncEventReq, 0, len(eventIDs))
	for _, id := range eventIDs {
		reqs = append(reqs, syncEventReq{ID: id})
	}
	resp, err := putSync(ctx, client, calendarID, syncReq{MemberID: memberID, Events: reqs})
	if err != nil {
		return err
	}
	if resp.Code == papi.CodeSuccess || resp.Code == papi.CodeSuccessMulti {
		return nil
	}
	if err := resp.firstError(); err != nil {
		return fmt.Errorf("deleting events: %w", err)
	}
	return fmt.Errorf("deleting events: code %d", resp.Code)
}
