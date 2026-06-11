package event

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/icaltime"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
	"github.com/cheeseandcereal/proton-cal/internal/recurrence"
)

// resolveSeries resolves a recurring series from any of its rows: returns
// the master and all same-UID rows. Errors when the event is not recurring.
func resolveSeries(ctx context.Context, client papi.API, calendarID, eventID string) (*caltypes.RawEvent, []*caltypes.RawEvent, error) {
	raw, err := Get(ctx, client, calendarID, eventID)
	if err != nil {
		return nil, nil, err
	}
	related, err := GetByUID(ctx, client, calendarID, raw.UID)
	if err != nil {
		return nil, nil, err
	}
	if raw.RRule != "" {
		return raw, related, nil
	}
	for _, r := range related {
		if r.RRule != "" {
			return r, related, nil
		}
	}
	return nil, nil, fmt.Errorf("event %s is not a recurring event", eventID)
}

// deleteSeriesExceptions deletes all exception rows of a series except
// keepEventID (used when a series-level change invalidates single edits).
// Returns the number of rows deleted.
func deleteSeriesExceptions(ctx context.Context, client papi.API, calendarID, uid, memberID, keepEventID string) (int, error) {
	rows, err := GetByUID(ctx, client, calendarID, uid)
	if err != nil {
		return 0, err
	}
	var ids []string
	for _, r := range rows {
		if r.RecurrenceID != 0 && r.ID != keepEventID {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	if err := deleteRows(ctx, client, calendarID, ids, memberID); err != nil {
		return 0, err
	}
	return len(ids), nil
}

// SmartDelete picks the right delete strategy for the addressed target:
//   - occurrenceTS != 0: delete that single occurrence (EXDATE on the
//     master; an existing exception row for it is deleted too).
//   - occurrenceTS == 0 and the row is an exception: delete just that
//     occurrence (EXDATE + row).
//   - master row: delete the whole series (master + all same-UID rows; the
//     server orphans exceptions otherwise - see RESEARCH.md).
//   - plain event: delete the row.
func SmartDelete(ctx context.Context, client papi.API, access *calendar.Access, eventID string, occurrenceTS int64) (*DeleteResult, error) {
	raw, err := Get(ctx, client, access.CalendarID, eventID)
	if err != nil {
		return nil, err
	}

	// Passing an edited occurrence's own ID deletes just that occurrence.
	if occurrenceTS == 0 && raw.RecurrenceID != 0 {
		occurrenceTS = raw.RecurrenceID
	}

	if occurrenceTS != 0 {
		master, related, err := resolveSeries(ctx, client, access.CalendarID, eventID)
		if err != nil {
			return nil, err
		}
		row, err := recurrence.ResolveOccurrence(master, related, occurrenceTS)
		if err != nil {
			return nil, err
		}
		// EXDATE the original occurrence start on the master.
		exdate := time.Unix(occurrenceTS, 0).UTC()
		if _, err := update(ctx, client, access, master.ID, UpdateOptions{AddExdates: []time.Time{exdate}}); err != nil {
			return nil, err
		}
		rows := 1
		// If the occurrence had been single-edited, delete its exception row too.
		if row != nil {
			if err := deleteRows(ctx, client, access.CalendarID, []string{row.ID}, access.MemberID); err != nil {
				return nil, err
			}
			rows = 2
		}
		return &DeleteResult{Kind: DeletedOccurrence, RowsDeleted: rows}, nil
	}

	if raw.RRule != "" {
		// Series delete: master + ALL same-UID rows in ONE batched call;
		// the server orphans exception rows otherwise (verified live).
		rows, err := GetByUID(ctx, client, access.CalendarID, raw.UID)
		if err != nil {
			return nil, err
		}
		idSet := map[string]struct{}{raw.ID: {}}
		for _, r := range rows {
			if r.ID != "" {
				idSet[r.ID] = struct{}{}
			}
		}
		ids := make([]string, 0, len(idSet))
		for id := range idSet {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if err := deleteRows(ctx, client, access.CalendarID, ids, access.MemberID); err != nil {
			return nil, err
		}
		return &DeleteResult{Kind: DeletedSeries, RowsDeleted: len(ids)}, nil
	}

	if err := deleteRows(ctx, client, access.CalendarID, []string{eventID}, access.MemberID); err != nil {
		return nil, err
	}
	return &DeleteResult{Kind: DeletedEvent, RowsDeleted: 1}, nil
}

// SmartUpdate picks the right update strategy for the addressed target:
//   - occurrenceTS != 0: edit ONE occurrence (update its existing exception
//     row, or create a fresh exception row seeded from the master with
//     SEQUENCE >= the master's). Recurrence options are rejected here.
//   - otherwise: update the event/series; when a significant change hits a
//     master, its now-invalid exception rows are deleted afterwards.
func SmartUpdate(ctx context.Context, client papi.API, access *calendar.Access, eventID string, opts UpdateOptions, occurrenceTS int64) (*UpdateOutcome, error) {
	if occurrenceTS != 0 {
		if opts.RRule != nil || opts.ClearRRule {
			return nil, errors.New("recurrence changes cannot be combined with an occurrence edit (edit the series instead)")
		}
		master, related, err := resolveSeries(ctx, client, access.CalendarID, eventID)
		if err != nil {
			return nil, err
		}
		row, err := recurrence.ResolveOccurrence(master, related, occurrenceTS)
		if err != nil {
			return nil, err
		}
		if row != nil {
			// The occurrence was already single-edited: update its row.
			updated, err := update(ctx, client, access, row.ID, opts)
			if err != nil {
				return nil, err
			}
			return &UpdateOutcome{Updated: updated, EditedOccurrence: true}, nil
		}

		// Create a fresh exception row seeded from the master's fields.
		cur, err := Decrypt(master, access.KR)
		if err != nil {
			return nil, err
		}
		occStart := time.Unix(occurrenceTS, 0).UTC()
		duration := cur.End.Sub(cur.Start)
		start := occStart
		if opts.Start != nil {
			start = *opts.Start
		}
		end := start.Add(duration)
		if opts.End != nil {
			end = *opts.End
		}
		tz := opts.TZName
		if tz == "" {
			tz = icaltime.OrUTC(cur.StartTimezone)
		}
		created, err := Create(ctx, client, access, CreateOptions{
			Summary:      strOr(opts.Summary, cur.Summary),
			Description:  strOr(opts.Description, cur.Description),
			Location:     strOr(opts.Location, cur.Location),
			Start:        start,
			End:          end,
			TZName:       tz,
			AllDay:       cur.AllDay,
			UID:          cur.UID,
			RecurrenceID: &occStart,
			// The server requires single edits to carry a SEQUENCE >= the
			// master's.
			Sequence: cur.Sequence,
		})
		if err != nil {
			return nil, err
		}
		return &UpdateOutcome{Updated: created, EditedOccurrence: true}, nil
	}

	raw, err := Get(ctx, client, access.CalendarID, eventID)
	if err != nil {
		return nil, err
	}
	updated, err := update(ctx, client, access, eventID, opts)
	if err != nil {
		return nil, err
	}
	outcome := &UpdateOutcome{Updated: updated}
	// A series-level time or recurrence change invalidates single edits.
	if raw.IsMaster() && opts.Significant() {
		n, err := deleteSeriesExceptions(ctx, client, access.CalendarID, raw.UID, access.MemberID, eventID)
		if err != nil {
			return nil, fmt.Errorf("event updated, but cleaning up edited occurrences failed: %w", err)
		}
		outcome.RemovedExceptions = n
	}
	return outcome, nil
}

func strOr(p *string, fallback string) string {
	if p != nil {
		return *p
	}
	return fallback
}
