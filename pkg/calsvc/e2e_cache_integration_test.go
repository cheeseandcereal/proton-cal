//go:build integration

package calsvc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/cheeseandcereal/proton-cal/pkg/calendar"
)

// An obsolete cached default must be refreshed before selecting a live calendar.
func TestE2ECachedDefaultRecovery(t *testing.T) {
	actor, calSel := liveService(t)
	ctx := context.Background()
	target, err := actor.ResolveCalendarInfo(ctx, calSel)
	if err != nil {
		t.Fatalf("resolving test calendar: %v", err)
	}
	originalDefault, err := actor.DefaultCalendarID(ctx)
	if err != nil {
		t.Fatalf("reading original default: %v", err)
	}
	t.Cleanup(func() {
		if err := calendar.SetDefaultCalendarID(context.Background(), actor.api, originalDefault); err != nil {
			t.Errorf("restoring original default: %v", err)
		}
	})
	if err := calendar.SetDefaultCalendarID(ctx, actor.api, target.ID); err != nil {
		t.Fatalf("setting test calendar as default: %v", err)
	}

	api := newCachedAPI(actor.client, newTestCache(t), false)
	api.cache.Put(calendar.UserSettingsPath, json.RawMessage(userSettingsBody("obsolete-calendar")))
	reader := &Service{
		cfg: actor.cfg, client: actor.client, api: api, cacheAPI: api,
		freshAPI: newCachedAPI(actor.client, api.cache, true),
	}
	got, err := reader.GetCalendar(ctx, "")
	if err != nil {
		t.Fatalf("GetCalendar with stale default: %v", err)
	}
	if got.Info.ID != target.ID || !got.IsDefault {
		t.Fatalf("resolved %q (default=%t), want %q marked default", got.Info.ID, got.IsDefault, target.ID)
	}
}
