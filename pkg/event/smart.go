package event

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
	"github.com/cheeseandcereal/proton-cal/pkg/caltypes"
	"github.com/cheeseandcereal/proton-cal/pkg/icaltime"
	"github.com/cheeseandcereal/proton-cal/pkg/papi"
	"github.com/cheeseandcereal/proton-cal/pkg/recurrence"
)

// resolveSeries resolves a series from any of its rows: returns the master and
// all same-UID rows. Errors when the event is not recurring.
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

// deleteSeriesExceptions deletes all exception rows of a series except keepEventID
// (used when a series-level change invalidates single edits); returns the count.
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

// SmartDelete picks the delete strategy: occurrenceTS != 0 (or an exception row)
// EXDATEs the occurrence and deletes its row; a master deletes the whole series
// (master + all same-UID rows in one batch, else the server orphans exceptions -
// see docs/api.md); a plain event deletes its row.
func SmartDelete(ctx context.Context, client papi.API, access *calendar.Access, eventID string, occurrenceTS int64) (*DeleteResult, error) {
	raw, err := Get(ctx, client, access.CalendarID, eventID)
	if err != nil {
		return nil, err
	}

	// Passing an edited occurrence's own ID deletes just that occurrence.
	if occurrenceTS == 0 && raw.RecurrenceID != 0 {
		occurrenceTS = raw.RecurrenceID
	}

	switch {
	case occurrenceTS != 0:
		return deleteOccurrence(ctx, client, access, eventID, occurrenceTS)
	case raw.RRule != "":
		return deleteSeries(ctx, client, access, raw)
	default:
		if err := deleteRows(ctx, client, access.CalendarID, []string{eventID}, access.MemberID); err != nil {
			return nil, err
		}
		return &DeleteResult{Kind: DeletedEvent, RowsDeleted: 1}, nil
	}
}

// deleteOccurrence removes one occurrence: EXDATE on the master, plus deleting
// its exception row if single-edited.
func deleteOccurrence(ctx context.Context, client papi.API, access *calendar.Access, eventID string, occurrenceTS int64) (*DeleteResult, error) {
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

// deleteSeries deletes a series: master plus ALL same-UID rows in ONE batched
// call; the server orphans exception rows otherwise (verified live).
func deleteSeries(ctx context.Context, client papi.API, access *calendar.Access, master *caltypes.RawEvent) (*DeleteResult, error) {
	rows, err := GetByUID(ctx, client, access.CalendarID, master.UID)
	if err != nil {
		return nil, err
	}
	idSet := map[string]struct{}{master.ID: {}}
	for _, r := range rows {
		if r.ID != "" {
			idSet[r.ID] = struct{}{}
		}
	}
	ids := slices.Sorted(maps.Keys(idSet))
	if err := deleteRows(ctx, client, access.CalendarID, ids, access.MemberID); err != nil {
		return nil, err
	}
	return &DeleteResult{Kind: DeletedSeries, RowsDeleted: len(ids)}, nil
}

// SmartUpdate picks the update strategy: occurrenceTS != 0 edits ONE occurrence
// (update its exception row, or create one seeded from the master with SEQUENCE
// >= master's; recurrence options rejected). Otherwise update the event/series;
// a significant change on a master then deletes its now-invalid exception rows.
func SmartUpdate(ctx context.Context, client papi.API, access *calendar.Access, eventID string, opts UpdateOptions, occurrenceTS int64) (*UpdateOutcome, error) {
	if occurrenceTS != 0 {
		return updateOccurrence(ctx, client, access, eventID, opts, occurrenceTS)
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

// updateOccurrence edits one occurrence: update its existing exception row, or
// create a fresh one seeded from the master.
func updateOccurrence(ctx context.Context, client papi.API, access *calendar.Access, eventID string, opts UpdateOptions, occurrenceTS int64) (*UpdateOutcome, error) {
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

	cur, err := Decrypt(master, access.KR)
	if err != nil {
		return nil, err
	}
	if cur.DecryptFailed {
		return nil, fmt.Errorf("editing occurrence of event %s: %w", master.ID, ErrDecryptDegraded)
	}
	created, err := Create(ctx, client, access, seedExceptionRow(cur, opts, occurrenceTS))
	if err != nil {
		return nil, err
	}
	return &UpdateOutcome{Updated: created, EditedOccurrence: true}, nil
}

// seedExceptionRow builds CreateOptions for a fresh exception row from the
// decrypted master and requested changes (pure, no I/O). Unspecified times keep
// the occurrence's slot (original start, master duration); text inherits master's.
func seedExceptionRow(master *Event, opts UpdateOptions, occurrenceTS int64) CreateOptions {
	occStart := time.Unix(occurrenceTS, 0).UTC()
	start := occStart
	if opts.Start != nil {
		start = *opts.Start
	}
	end := start.Add(master.End.Sub(master.Start))
	if opts.End != nil {
		end = *opts.End
	}
	tz := opts.TZName
	if tz == "" {
		tz = icaltime.OrUTC(master.StartTimezone)
	}
	// Seed reminders/color from the master so a fresh single-edit (a CREATE)
	// keeps the series' effective values instead of reverting to inherit. opts wins.
	reminders, remindersSet := master.Notifications, master.NotificationsSet
	if opts.Reminders != nil {
		if opts.Reminders.Inherit {
			reminders, remindersSet = nil, false
		} else {
			reminders, remindersSet = opts.Reminders.List, true
		}
	}
	color := master.Color
	if opts.Color != nil {
		color = opts.Color.Value
	}

	return CreateOptions{
		Summary:      strOr(opts.Summary, master.Summary),
		Description:  strOr(opts.Description, master.Description),
		Location:     strOr(opts.Location, master.Location),
		Start:        start,
		End:          end,
		TZName:       tz,
		AllDay:       master.AllDay,
		Reminders:    reminders,
		RemindersSet: remindersSet,
		Color:        color,
		UID:          master.UID,
		RecurrenceID: &occStart,
		// The server requires single edits to carry a SEQUENCE >= the
		// master's.
		Sequence: master.Sequence,
	}
}

func strOr(p *string, fallback string) string {
	if p != nil {
		return *p
	}
	return fallback
}
