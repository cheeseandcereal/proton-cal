package event

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/cheeseandcereal/proton-cal/internal/calendar"
	"github.com/cheeseandcereal/proton-cal/internal/caltypes"
	"github.com/cheeseandcereal/proton-cal/internal/papi"
	"github.com/cheeseandcereal/proton-cal/internal/recurrence"
)

func resolveSeriesImpl(ctx context.Context, client *papi.Client, calendarID, eventID string) (*caltypes.RawEvent, []*caltypes.RawEvent, error) {
	raw, err := getImpl(ctx, client, calendarID, eventID)
	if err != nil {
		return nil, nil, err
	}
	related, err := getByUIDImpl(ctx, client, calendarID, raw.UID)
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

func deleteSeriesExceptionsImpl(ctx context.Context, client *papi.Client, calendarID, uid, memberID, keepEventID string) (int, error) {
	rows, err := getByUIDImpl(ctx, client, calendarID, uid)
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
	if err := deleteImpl(ctx, client, calendarID, ids, memberID); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func smartDeleteImpl(ctx context.Context, client *papi.Client, access *calendar.Access, eventID string, occurrenceTS int64) (*DeleteResult, error) {
	raw, err := getImpl(ctx, client, access.CalendarID, eventID)
	if err != nil {
		return nil, err
	}

	// Passing an edited occurrence's own ID deletes just that occurrence.
	if occurrenceTS == 0 && raw.RecurrenceID != 0 {
		occurrenceTS = raw.RecurrenceID
	}

	if occurrenceTS != 0 {
		master, related, err := resolveSeriesImpl(ctx, client, access.CalendarID, eventID)
		if err != nil {
			return nil, err
		}
		kind, row, err := recurrence.ResolveOccurrence(master, related, occurrenceTS)
		if err != nil {
			return nil, err
		}
		// EXDATE the original occurrence start on the master.
		exdate := time.Unix(occurrenceTS, 0).UTC()
		if _, err := updateImpl(ctx, client, access, master.ID, UpdateOptions{AddExdates: []time.Time{exdate}}); err != nil {
			return nil, err
		}
		rows := 1
		// If the occurrence had been single-edited, delete its exception row too.
		if kind == recurrence.KindException && row != nil {
			if err := deleteImpl(ctx, client, access.CalendarID, []string{row.ID}, access.MemberID); err != nil {
				return nil, err
			}
			rows = 2
		}
		return &DeleteResult{Kind: "occurrence", RowsDeleted: rows}, nil
	}

	if raw.RRule != "" {
		// Series delete: master + ALL same-UID rows in ONE batched call;
		// the server orphans exception rows otherwise (verified live).
		rows, err := getByUIDImpl(ctx, client, access.CalendarID, raw.UID)
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
		if err := deleteImpl(ctx, client, access.CalendarID, ids, access.MemberID); err != nil {
			return nil, err
		}
		return &DeleteResult{Kind: "series", RowsDeleted: len(ids)}, nil
	}

	if err := deleteImpl(ctx, client, access.CalendarID, []string{eventID}, access.MemberID); err != nil {
		return nil, err
	}
	return &DeleteResult{Kind: "event", RowsDeleted: 1}, nil
}

func smartUpdateImpl(ctx context.Context, client *papi.Client, access *calendar.Access, eventID string, opts UpdateOptions, occurrenceTS int64) (*UpdateOutcome, error) {
	if occurrenceTS != 0 {
		if opts.RRule != nil || opts.ClearRRule {
			return nil, errors.New("recurrence changes cannot be combined with an occurrence edit (edit the series instead)")
		}
		master, related, err := resolveSeriesImpl(ctx, client, access.CalendarID, eventID)
		if err != nil {
			return nil, err
		}
		kind, row, err := recurrence.ResolveOccurrence(master, related, occurrenceTS)
		if err != nil {
			return nil, err
		}
		if kind == recurrence.KindException && row != nil {
			// The occurrence was already single-edited: update its row.
			updated, err := updateImpl(ctx, client, access, row.ID, opts)
			if err != nil {
				return nil, err
			}
			return &UpdateOutcome{Updated: updated, EditedOccurrence: true}, nil
		}

		// Create a fresh exception row seeded from the master's fields.
		cur, err := decryptImpl(master, access.KR)
		if err != nil {
			return nil, err
		}
		occStart := time.Unix(occurrenceTS, 0).UTC()
		duration := time.Duration(cur.EndTime-cur.StartTime) * time.Second
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
			tz = cur.StartTimezone
		}
		if tz == "" {
			tz = "UTC"
		}
		created, err := createImpl(ctx, client, access, CreateOptions{
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

	raw, err := getImpl(ctx, client, access.CalendarID, eventID)
	if err != nil {
		return nil, err
	}
	updated, err := updateImpl(ctx, client, access, eventID, opts)
	if err != nil {
		return nil, err
	}
	outcome := &UpdateOutcome{Updated: updated}
	// A series-level time or recurrence change invalidates single edits.
	if raw.IsMaster() && opts.Significant() {
		n, err := deleteSeriesExceptionsImpl(ctx, client, access.CalendarID, raw.UID, access.MemberID, eventID)
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
