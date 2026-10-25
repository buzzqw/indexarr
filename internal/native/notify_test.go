package native

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/notify"
	"github.com/buzzqw/indexarr/internal/store"
)

func notifierSink(t *testing.T, st *store.Store) (*notify.Dispatcher, chan notify.Event) {
	t.Helper()
	events := make(chan notify.Event, 10)
	dispatcher := notify.NewDispatcher(st, discardLogger())
	dispatcher.Deliver = func(_ context.Context, _ notify.Config, event notify.Event) error {
		events <- event
		return nil
	}
	return dispatcher, events
}

func TestIndexerStateNotifiesOnChangeOnly(t *testing.T) {
	st := newTestStore(t)
	manager := newTestManager(t, st)
	dispatcher, events := notifierSink(t, st)
	runner := &Runner{
		store: st, manager: manager, logger: discardLogger(), notifier: dispatcher,
	}
	settings, _ := st.LoadSettings(context.Background())
	settings.NotifyFailures = true
	settings.NotifyWebhookURL = "http://hooks.invalid/indexarr"
	settings.NotifyCooldownMinutes = 30
	if err := st.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	putDefinition(t, st, store.Definition{ID: "state-notify"})
	id := createIndexer(t, st, "state-notify", "state-notify", "State Notify", "{}", true)
	row, err := st.GetIndexer(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	next := func(outcome RunOutcome) notify.Event {
		t.Helper()
		runner.afterRun(context.Background(), row, outcome)
		select {
		case event := <-events:
			return event
		case <-time.After(2 * time.Second):
			t.Fatal("no notification")
			return notify.Event{}
		}
	}
	none := func(step string) {
		t.Helper()
		select {
		case event := <-events:
			t.Fatalf("%s sent an unexpected notification: %#v", step, event)
		case <-time.After(150 * time.Millisecond):
		}
	}

	// ok -> error: the first failure is a change.
	outcome := next(RunOutcome{Err: fmt.Errorf("connection refused")})
	if outcome.Kind != notify.KindIndexerState || outcome.Current != notify.StateError {
		t.Fatalf("first failure event = %#v", outcome)
	}
	if outcome.Source != "State Notify" || outcome.Path != "/indexers" {
		t.Errorf("event source/path = %q/%q", outcome.Source, outcome.Path)
	}
	// The same error again is not a new event.
	runner.afterRun(context.Background(), row, RunOutcome{Err: fmt.Errorf("connection refused")})
	none("repeated error")

	// error -> blocked is a change.
	outcome = next(RunOutcome{Err: fmt.Errorf("rss: %w", engine.ErrChallenge)})
	if outcome.Previous != notify.StateError || outcome.Current != notify.StateBlocked {
		t.Errorf("blocked event = %q -> %q", outcome.Previous, outcome.Current)
	}

	// blocked -> ok is the recovery.
	outcome = next(RunOutcome{Stats: &indexer.RunStats{}})
	if outcome.Previous != notify.StateBlocked || outcome.Current != notify.StateOK {
		t.Errorf("recovery event = %q -> %q", outcome.Previous, outcome.Current)
	}

	// A successful run while already healthy says nothing.
	runner.afterRun(context.Background(), row, RunOutcome{Stats: &indexer.RunStats{}})
	none("repeated success")
}

func TestNewReleasesAreGroupedPerPass(t *testing.T) {
	st := newTestStore(t)
	dispatcher, events := notifierSink(t, st)
	runner := &Runner{store: st, logger: discardLogger(), notifier: dispatcher}
	settings, _ := st.LoadSettings(context.Background())
	settings.NotifyNewReleases = true
	settings.NotifyWebhookURL = "http://hooks.invalid/indexarr"
	if err := st.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	outcomes := []RunOutcome{
		{Name: "Site A", Stats: &indexer.RunStats{New: 2, Items: []engine.Item{{Title: "A1"}, {Title: "A2"}}}},
		{Name: "Site B", Stats: &indexer.RunStats{New: 1, Items: []engine.Item{{Title: "B1"}}}},
		{Name: "Site C", Stats: &indexer.RunStats{New: 0}},
	}
	runner.notifyNewReleases(context.Background(), outcomes)
	select {
	case event := <-events:
		if event.Kind != notify.KindNewReleases || event.Count != 3 || len(event.Titles) != 3 {
			t.Errorf("grouped event = %#v", event)
		}
		if event.Source != "Site A, Site B" || event.Path != "/releases" {
			t.Errorf("grouped source/path = %q/%q", event.Source, event.Path)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no grouped notification")
	}
	select {
	case event := <-events:
		t.Fatalf("a second notification was sent for the same pass: %#v", event)
	case <-time.After(150 * time.Millisecond):
	}

	// A pass with no new release sends nothing.
	runner.notifyNewReleases(context.Background(), []RunOutcome{{Name: "Site A"}})
	select {
	case event := <-events:
		t.Fatalf("empty pass notified: %#v", event)
	case <-time.After(150 * time.Millisecond):
	}
}
