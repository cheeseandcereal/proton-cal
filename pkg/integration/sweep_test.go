//go:build integration

package integration

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cheeseandcereal/proton-cal/pkg/event"
)

// TestZSweep runs last (Z prefix keeps it last in declaration order). It scans
// every calendar over now+25d..now+40d and deletes any leftover row whose summary
// carries the suite tag, cleaning up crashed runs. Per-row failures are non-fatal.
func TestZSweep(t *testing.T) {
	ctx := context.Background()
	s := setup(t)

	now := time.Now().UTC()
	winStart := now.AddDate(0, 0, sweepStartDays).Unix()
	winEnd := now.AddDate(0, 0, sweepEndDays).Unix()

	totalSwept := 0
	for _, cal := range s.resolved {
		access := s.accessFor(t, cal)
		listed, err := event.ListWindow(ctx, s.client, access.KR, cal.ID, winStart, winEnd, s.tzName)
		if err != nil {
			t.Errorf("sweep: ListWindow on calendar %q: %v", cal.Name, err)
			continue
		}

		// Deduplicate occurrences to their backing rows; only rows whose
		// decrypted summary carries the suite tag are ever touched.
		rows := make(map[string]*event.Event)
		for _, l := range listed {
			if l.Event == nil {
				continue
			}
			if !strings.Contains(l.Event.Summary, summaryPrefix) {
				continue
			}
			rows[l.Event.EventID] = l.Event
		}

		// Masters first: a series delete also removes its exception rows,
		// so same-UID leftovers can then be skipped.
		var masters, others []*event.Event
		for _, ev := range rows {
			if ev.IsRecurring() && ev.RecurrenceID.IsZero() {
				masters = append(masters, ev)
			} else {
				others = append(others, ev)
			}
		}
		sort.Slice(masters, func(i, j int) bool { return masters[i].EventID < masters[j].EventID })
		sort.Slice(others, func(i, j int) bool { return others[i].EventID < others[j].EventID })

		seriesDeleted := make(map[string]bool)
		for _, ev := range append(masters, others...) {
			if seriesDeleted[ev.UID] {
				continue
			}
			res, err := event.SmartDelete(ctx, s.client, access, ev.EventID, 0)
			if err != nil {
				t.Logf("sweep: could not delete event %s (%q) in calendar %q: %v",
					ev.EventID, ev.Summary, cal.Name, err)
				continue
			}
			if res.Kind == "series" {
				seriesDeleted[ev.UID] = true
			}
			totalSwept += res.RowsDeleted
			t.Logf("sweep: deleted leftover %s row(s) for event %s (%q) in calendar %q (%d row(s))",
				res.Kind, ev.EventID, ev.Summary, cal.Name, res.RowsDeleted)
		}
	}
	t.Logf("sweep: deleted %d leftover row(s) total", totalSwept)
}
