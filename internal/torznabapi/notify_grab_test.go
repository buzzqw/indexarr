package torznabapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/notify"
)

// TestGrabSuccessNotifications checks the trigger wiring: a payload or magnet
// delivered by the Torznab endpoint reaches the dispatcher with the release
// data the custom-script environment needs.
func TestGrabSuccessNotifications(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.NotifyWebhookURL = "http://example.invalid/hook"
	settings.NotifyGrabSuccess = true
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	events := make(chan notify.Event, 1)
	dispatcher := notify.NewDispatcher(st, testLogger())
	dispatcher.Deliver = func(_ context.Context, _ notify.Config, event notify.Event) error {
		events <- event
		return nil
	}
	api.SetNotifier(dispatcher)
	// The production server installs the grab guard/record pair, which is also
	// what resolves the indexer behind the release. Install a no-op recorder so
	// the event carries its source.
	api.SetGrabGuard(nil, func(context.Context, int64, int64, string) error { return nil })

	hash := "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	id := insertRelease(t, st, "Notified Show 1080p", "5040", hash, "", nil)
	linkReleaseIndexer(t, st, "notify", id)

	recorder := do(t, api, "/dl/"+itoa(id)+".torrent?apikey=secret")
	if recorder.Code != http.StatusFound {
		t.Fatalf("download status = %d: %s", recorder.Code, recorder.Body.String())
	}
	select {
	case event := <-events:
		if event.Kind != notify.KindGrabSuccess {
			t.Errorf("event kind = %q", event.Kind)
		}
		if event.Title != "Notified Show 1080p" || event.Size != 1234 {
			t.Errorf("event = %#v", event)
		}
		if len(event.Categories) != 1 || event.Categories[0] != "5040" {
			t.Errorf("categories = %v", event.Categories)
		}
		if event.Source != "Guard Indexer notify" {
			t.Errorf("source = %q", event.Source)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no grab-success notification was dispatched")
	}
}
