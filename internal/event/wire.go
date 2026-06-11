package event

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"

	"github.com/cheeseandcereal/proton-cal-go/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal-go/internal/papi"
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

// firstEvent interprets the first per-event reply: (echoed event, nil) on
// success (the event may be nil - updates do not always echo it), or an
// error carrying the per-event code and message.
func (r *syncResp) firstEvent() (*caltypes.RawEvent, error) {
	if len(r.Responses) == 0 {
		// Deletes return only a top-level code.
		if r.Code == 1000 || r.Code == 1001 {
			return nil, nil
		}
		return nil, fmt.Errorf("sync failed: code %d", r.Code)
	}
	resp := r.Responses[0].Response
	if resp.Code != 1000 {
		if resp.Error != "" {
			return nil, fmt.Errorf("sync failed: code %d: %s", resp.Code, resp.Error)
		}
		return nil, fmt.Errorf("sync failed: code %d", resp.Code)
	}
	return resp.Event, nil
}

func putSync(ctx context.Context, client *papi.Client, calendarID string, payload syncReq) (*syncResp, error) {
	var resp syncResp
	if err := client.Put(ctx, "/calendar/v1/"+calendarID+"/events/sync", payload, &resp); err != nil {
		return nil, fmt.Errorf("calendar %s: sync: %w", calendarID, err)
	}
	return &resp, nil
}

type eventsResponse struct {
	Events []*caltypes.RawEvent `json:"Events"`
	Total  int                  `json:"Total"`
}

// Query fetches raw events for a calendar with full pagination and
// client-side window filtering (the server ignores Start/End params).
func queryImpl(ctx context.Context, client *papi.Client, calendarID string, start, end int64, tzName string) ([]*caltypes.RawEvent, error) {
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
		if err := client.Get(ctx, "/calendar/v1/"+calendarID+"/events", q, &resp); err != nil {
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

func getImpl(ctx context.Context, client *papi.Client, calendarID, eventID string) (*caltypes.RawEvent, error) {
	var raw json.RawMessage
	if err := client.Get(ctx, "/calendar/v1/"+calendarID+"/events/"+eventID, nil, &raw); err != nil {
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

func getByUIDImpl(ctx context.Context, client *papi.Client, calendarID, uid string) ([]*caltypes.RawEvent, error) {
	q := url.Values{}
	q.Set("UID", uid)
	q.Set("Page", "0")
	q.Set("PageSize", strconv.Itoa(pageSize))
	var resp eventsResponse
	if err := client.Get(ctx, "/calendar/v1/"+calendarID+"/events", q, &resp); err != nil {
		return nil, fmt.Errorf("calendar %s: fetching events by UID: %w", calendarID, err)
	}
	return resp.Events, nil
}

func deleteImpl(ctx context.Context, client *papi.Client, calendarID string, eventIDs []string, memberID string) error {
	reqs := make([]syncEventReq, 0, len(eventIDs))
	for _, id := range eventIDs {
		reqs = append(reqs, syncEventReq{ID: id})
	}
	resp, err := putSync(ctx, client, calendarID, syncReq{MemberID: memberID, Events: reqs})
	if err != nil {
		return err
	}
	if resp.Code != 1000 && resp.Code != 1001 {
		_, err := resp.firstEvent()
		if err != nil {
			return fmt.Errorf("deleting events: %w", err)
		}
		return fmt.Errorf("deleting events: code %d", resp.Code)
	}
	return nil
}
