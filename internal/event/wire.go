package event

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
)

const pageSize = 100

// eventBody is the Event object of a sync payload. Field presence is
// significant (see RESEARCH.md): the content/
// attendee arrays must serialize as [] (never null) and Notifications/Color
// as explicit null.
type eventBody struct {
	Permissions           int                  `json:"Permissions"`
	SharedKeyPacket       string               `json:"SharedKeyPacket,omitempty"`
	CalendarKeyPacket     string               `json:"CalendarKeyPacket,omitempty"`
	SharedEventContent    []caltypes.EventPart `json:"SharedEventContent"`
	CalendarEventContent  []caltypes.EventPart `json:"CalendarEventContent"`
	AttendeesEventContent []caltypes.EventPart `json:"AttendeesEventContent"`
	Attendees             []struct{}           `json:"Attendees"`
	Notifications         json.RawMessage      `json:"Notifications"`
	Color                 json.RawMessage      `json:"Color"`
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

// firstError returns the failure carried by the response: the first
// per-event reply's error when one is present, the top-level code
// otherwise, nil on success.
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

// firstEvent interprets the first per-event reply: (echoed event, nil) on
// success (the event may be nil - updates do not always echo it), or the
// error from firstError.
func (r *syncResp) firstEvent() (*caltypes.RawEvent, error) {
	if err := r.firstError(); err != nil {
		return nil, err
	}
	if len(r.Responses) == 0 {
		return nil, nil
	}
	return r.Responses[0].Response.Event, nil
}

func putSync(ctx context.Context, client *papi.Client, calendarID string, payload syncReq) (*syncResp, error) {
	var resp syncResp
	if err := client.Put(ctx, calendar.APIPath+"/"+calendarID+"/events/sync", payload, &resp); err != nil {
		return nil, fmt.Errorf("calendar %s: sync: %w", calendarID, err)
	}
	return &resp, nil
}

type eventsResponse struct {
	Events []*caltypes.RawEvent `json:"Events"`
	Total  int                  `json:"Total"`
}

// query fetches raw events for a calendar with full pagination and
// client-side window filtering (the server ignores Start/End params).
// Recurring masters always survive the filter (they are expanded later).
// Sorted by StartTime.
func query(ctx context.Context, client *papi.Client, calendarID string, start, end int64, tzName string) ([]*caltypes.RawEvent, error) {
	var all []*caltypes.RawEvent
	for page := 0; ; page++ {
		q := url.Values{}
		q.Set("Start", strconv.FormatInt(start, 10))
		q.Set("End", strconv.FormatInt(end, 10))
		if tzName != "" {
			q.Set("Timezone", tzName)
		}
		q.Set("Page", strconv.Itoa(page))
		q.Set("PageSize", strconv.Itoa(pageSize))

		var resp eventsResponse
		if err := client.Get(ctx, calendar.APIPath+"/"+calendarID+"/events", q, &resp); err != nil {
			return nil, fmt.Errorf("calendar %s: listing events: %w", calendarID, err)
		}
		all = append(all, resp.Events...)
		if len(resp.Events) == 0 || len(all) >= resp.Total {
			break
		}
	}

	// Client-side filter: keep rows overlapping [start, end). Recurring
	// masters always survive (their times describe only the first
	// occurrence; expansion does the precise filtering).
	filtered := all[:0]
	for _, ev := range all {
		if ev.RRule != "" || (ev.StartTime < end && ev.EndTime > start) {
			filtered = append(filtered, ev)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].StartTime < filtered[j].StartTime })
	return filtered, nil
}

// Get fetches a single raw event.
func Get(ctx context.Context, client *papi.Client, calendarID, eventID string) (*caltypes.RawEvent, error) {
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

// GetByUID fetches all raw rows sharing an iCal UID (master + exceptions);
// the UID query param filters server-side (verified live).
func GetByUID(ctx context.Context, client *papi.Client, calendarID, uid string) ([]*caltypes.RawEvent, error) {
	q := url.Values{}
	q.Set("UID", uid)
	q.Set("Page", "0")
	q.Set("PageSize", strconv.Itoa(pageSize))
	var resp eventsResponse
	if err := client.Get(ctx, calendar.APIPath+"/"+calendarID+"/events", q, &resp); err != nil {
		return nil, fmt.Errorf("calendar %s: fetching events by UID: %w", calendarID, err)
	}
	return resp.Events, nil
}

// deleteRows deletes raw event rows by ID in a single sync call.
func deleteRows(ctx context.Context, client *papi.Client, calendarID string, eventIDs []string, memberID string) error {
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
