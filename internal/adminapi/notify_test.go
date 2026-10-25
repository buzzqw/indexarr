package adminapi

import (
	"context"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/notify"
)

// TestCatalogSyncNotifies covers the catalog-updated event on the admin API
// path, not only at start-up.
func TestCatalogSyncNotifies(t *testing.T) {
	e := newEnv(t, true, false)
	events := make(chan notify.Event, 4)
	dispatcher := notify.NewDispatcher(e.store, e.server.logger)
	dispatcher.Deliver = func(_ context.Context, _ notify.Config, event notify.Event) error {
		events <- event
		return nil
	}
	e.server.notifier = dispatcher
	settings, _ := e.store.LoadSettings(context.Background())
	settings.NotifyCatalogUpdated = true
	settings.NotifyWebhookURL = "http://hooks.invalid/indexarr"
	if err := e.store.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}

	res := e.do("POST", "/api/v2/catalog/sync", "")
	requireStatus(t, res, 200)
	select {
	case event := <-events:
		if event.Kind != notify.KindCatalogUpdated || event.Count <= 0 {
			t.Errorf("catalog event = %#v", event)
		}
		if event.Path != "/settings?section=advanced" {
			t.Errorf("catalog path = %q", event.Path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("catalog sync sent no notification")
	}
}
