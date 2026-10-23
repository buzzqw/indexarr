package native

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

const testHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// testResultsHTML is one extractable row with an infohash, so a run never has
// to download a .torrent payload.
const testResultsHTML = `<html><body>
<table class="results"><tbody>
<tr>
  <td class="name"><a href="/details/1">Native Result</a></td>
  <td><a class="download" href="/download/1.torrent">DL</a></td>
  <td class="size">1.5 GB</td>
  <td class="seeders">123</td>
  <td class="leechers">4</td>
  <td class="hash">` + testHash + `</td>
  <td class="date">02-01-2026 15:04</td>
</tr>
</tbody></table>
</body></html>`

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return st
}

func newTestManager(t *testing.T, st *store.Store) *indexer.Manager {
	t.Helper()
	key := make([]byte, 32)
	key[0] = 1
	v, err := vault.New(key)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	manager, err := indexer.New(indexer.Config{
		Store:     st,
		Vault:     v,
		Transport: engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
		Logger:    discardLogger(),
	})
	if err != nil {
		t.Fatalf("indexer.New: %v", err)
	}
	return manager
}

func newHarness(t *testing.T, workers int, onResult func(RunOutcome)) (*store.Store, *Runner) {
	t.Helper()
	st := newTestStore(t)
	manager := newTestManager(t, st)
	runner, err := New(Config{
		Store:    st,
		Manager:  manager,
		Logger:   discardLogger(),
		Interval: 10 * time.Millisecond, // clamped to the 60s floor
		Workers:  workers,
		OnResult: onResult,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return st, runner
}

func putDefinition(t *testing.T, st *store.Store, d store.Definition) {
	t.Helper()
	if d.SchemaVersion == 0 {
		d.SchemaVersion = definition.CurrentSchema
	}
	if d.State == "" {
		d.State = "verificata"
	}
	if d.Origin == "" {
		d.Origin = "local"
	}
	if d.Author == "" {
		d.Author = "test"
	}
	if d.Hash == "" {
		d.Hash = definition.Fingerprint([]byte(d.Body))
	}
	if d.UpdatedAt.IsZero() {
		d.UpdatedAt = time.Now()
	}
	if err := st.UpsertDefinition(context.Background(), d); err != nil {
		t.Fatalf("UpsertDefinition(%s): %v", d.ID, err)
	}
}

func createIndexer(t *testing.T, st *store.Store, definitionID, slug, name, configJSON string, enabled bool) int64 {
	t.Helper()
	id, err := st.CreateIndexer(context.Background(), store.Indexer{
		DefinitionID: definitionID,
		Protocol:     "torrent",
		Slug:         slug,
		Name:         name,
		Enabled:      enabled,
		Priority:     25,
		ConfigJSON:   configJSON,
	})
	if err != nil {
		t.Fatalf("CreateIndexer(%s): %v", slug, err)
	}
	return id
}

func nativeConfig(baseURL string) string {
	return fmt.Sprintf(`{"url":%q,"query":"test"}`, baseURL)
}

// nativeDefinition renders a real HTML definition bound to a test server.
func nativeDefinition(id, baseURL string) string {
	return fmt.Sprintf(`schema: 1
meta:
  id: %s
  name: Native %s
  type: public
  protocol: torrent
  urls: ["%s/"]
capabilities:
  search: [q]
search:
  request:
    get: /search
    params:
      q: "{{ query.keywords }}"
  response:
    type: html
    rows: "table.results tr"
    fields:
      title: { selector: "td.name a", filters: [trim] }
      details: { selector: "td.name a", attr: href, type: url }
      download: { selector: "a.download", attr: href, type: url }
      size: { selector: "td.size", type: size }
      seeders: { selector: "td.seeders", type: int }
      leechers: { selector: "td.leechers", type: int }
      infohash: { selector: "td.hash" }
      date: { selector: "td.date", type: date, layouts: ["02-01-2006 15:04"] }
    required: [title, download, size]
`, id, id, baseURL)
}

func addNativeDefinition(t *testing.T, st *store.Store, id, baseURL string) {
	t.Helper()
	putDefinition(t, st, store.Definition{ID: id, Body: nativeDefinition(id, baseURL)})
}

func seedNative(t *testing.T, st *store.Store, definitionID, slug, name, baseURL string, enabled bool) int64 {
	t.Helper()
	addNativeDefinition(t, st, definitionID, baseURL)
	return createIndexer(t, st, definitionID, slug, name, nativeConfig(baseURL), enabled)
}

func seedLegacy(t *testing.T, st *store.Store, definitionID, slug, name string) int64 {
	t.Helper()
	body := fmt.Sprintf(`schema: 1
meta:
  id: %s
  name: %s
  type: private
  protocol: torrent
  urls: ["https://legacy.example/"]
connector: torznab
`, definitionID, name)
	putDefinition(t, st, store.Definition{ID: definitionID, Family: "torznab", Body: body})
	return createIndexer(t, st, definitionID, slug, name, `{}`, true)
}

type inflightTracker struct {
	current  atomic.Int64
	max      atomic.Int64
	requests atomic.Int64
}

func (t *inflightTracker) enter() {
	t.requests.Add(1)
	n := t.current.Add(1)
	for {
		old := t.max.Load()
		if n <= old || t.max.CompareAndSwap(old, n) {
			break
		}
	}
}

func (t *inflightTracker) leave() { t.current.Add(-1) }

func newNativeServer(t *testing.T, delay time.Duration, tracker *inflightTracker) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tracker != nil {
			tracker.enter()
			defer tracker.leave()
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, testResultsHTML)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRunOnceRunsNativeAndSkipsLegacy(t *testing.T) {
	st, runner := newHarness(t, 2, nil)
	server := newNativeServer(t, 0, nil)

	// The names are chosen so name order differs from id order, proving the
	// result is deterministic by indexer ID.
	nativeID := seedNative(t, st, "native-one", "native-one", "Zulu Native", server.URL, true)
	legacyID := seedLegacy(t, st, "legacy-torznab", "legacy-torznab", "Alpha Legacy")

	outcomes := runner.RunOnce(context.Background())
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %d, want 2", len(outcomes))
	}
	if outcomes[0].IndexerID != nativeID || outcomes[1].IndexerID != legacyID {
		t.Fatalf("outcome ids = [%d, %d], want [%d, %d]",
			outcomes[0].IndexerID, outcomes[1].IndexerID, nativeID, legacyID)
	}

	native := outcomes[0]
	if native.Skipped || native.Err != nil {
		t.Fatalf("native outcome = %+v", native)
	}
	if native.Slug != "native-one" || native.Name != "Zulu Native" {
		t.Fatalf("native identity = %+v", native)
	}
	if native.Stats == nil || native.Stats.Seen != 1 || native.Stats.New != 1 {
		t.Fatalf("native stats = %+v", native.Stats)
	}
	if native.Duration <= 0 {
		t.Errorf("native duration = %v", native.Duration)
	}

	legacy := outcomes[1]
	if !legacy.Skipped || legacy.Reason != ReasonLegacyBridge {
		t.Fatalf("legacy outcome = %+v", legacy)
	}
	if legacy.Stats != nil || legacy.Err != nil {
		t.Fatalf("legacy outcome must have nil stats and error: %+v", legacy)
	}
	if legacy.Slug != "legacy-torznab" || legacy.Name != "Alpha Legacy" {
		t.Fatalf("legacy identity = %+v", legacy)
	}
}

func TestRunOnceSkipsDisabled(t *testing.T) {
	st, runner := newHarness(t, 2, nil)
	tracker := &inflightTracker{}
	server := newNativeServer(t, 0, tracker)
	id := seedNative(t, st, "native-disabled", "native-disabled", "Disabled Native", server.URL, false)

	outcomes := runner.RunOnce(context.Background())
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %d, want 1", len(outcomes))
	}
	outcome := outcomes[0]
	if outcome.IndexerID != id || !outcome.Skipped || outcome.Reason != ReasonDisabled {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.Stats != nil || outcome.Err != nil {
		t.Fatalf("skipped outcome must be clean: %+v", outcome)
	}
	if got := tracker.requests.Load(); got != 0 {
		t.Fatalf("disabled indexer made %d requests", got)
	}
}

func TestRunOnceInvalidDefinitionIsSkipped(t *testing.T) {
	st, runner := newHarness(t, 2, nil)
	server := newNativeServer(t, 0, nil)

	putDefinition(t, st, store.Definition{
		ID:   "broken",
		Body: "schema: 1\nmeta:\n  id: broken\n  name: Broken\n",
	})
	invalidID := createIndexer(t, st, "broken", "broken", "Broken Definition", `{}`, true)
	nativeID := seedNative(t, st, "native-ok", "native-ok", "Native OK", server.URL, true)

	outcomes := runner.RunOnce(context.Background())
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %d, want 2", len(outcomes))
	}
	if outcomes[0].IndexerID != invalidID || outcomes[1].IndexerID != nativeID {
		t.Fatalf("outcome ids = [%d, %d]", outcomes[0].IndexerID, outcomes[1].IndexerID)
	}
	invalid := outcomes[0]
	if !invalid.Skipped || invalid.Reason != ReasonInvalidDefinition {
		t.Fatalf("invalid outcome = %+v", invalid)
	}
	if invalid.Stats != nil || invalid.Err != nil {
		t.Fatalf("invalid outcome must be a clean skip: %+v", invalid)
	}
	if native := outcomes[1]; native.Skipped || native.Err != nil || native.Stats == nil {
		t.Fatalf("native outcome did not run after the invalid one: %+v", native)
	}
}

func TestRunOnceConcurrencyBound(t *testing.T) {
	st, runner := newHarness(t, 1, nil)
	tracker := &inflightTracker{}
	server := newNativeServer(t, 60*time.Millisecond, tracker)
	addNativeDefinition(t, st, "slow-native", server.URL)
	createIndexer(t, st, "slow-native", "slow-one", "Slow One", nativeConfig(server.URL), true)
	createIndexer(t, st, "slow-native", "slow-two", "Slow Two", nativeConfig(server.URL), true)

	outcomes := runner.RunOnce(context.Background())
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %d, want 2", len(outcomes))
	}
	for _, outcome := range outcomes {
		if outcome.Skipped || outcome.Err != nil || outcome.Stats == nil {
			t.Fatalf("outcome = %+v", outcome)
		}
	}
	if got := tracker.requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	if got := tracker.max.Load(); got != 1 {
		t.Fatalf("max in-flight = %d, want 1 with Workers=1", got)
	}
}

func TestRunIndexer(t *testing.T) {
	st, runner := newHarness(t, 2, nil)
	server := newNativeServer(t, 0, nil)
	nativeID := seedNative(t, st, "native-one", "native-one", "Native One", server.URL, true)
	legacyID := seedLegacy(t, st, "legacy-torznab", "legacy-torznab", "Legacy Torznab")

	nativeOutcome, err := runner.RunIndexer(context.Background(), nativeID)
	if err != nil {
		t.Fatalf("RunIndexer(native): %v", err)
	}
	if nativeOutcome.Skipped || nativeOutcome.Stats == nil {
		t.Fatalf("native outcome = %+v", nativeOutcome)
	}

	legacyOutcome, err := runner.RunIndexer(context.Background(), legacyID)
	if err != nil {
		t.Fatalf("RunIndexer(legacy): %v", err)
	}
	if !legacyOutcome.Skipped || legacyOutcome.Reason != ReasonLegacyBridge {
		t.Fatalf("legacy outcome = %+v", legacyOutcome)
	}

	if _, err := runner.RunIndexer(context.Background(), 999999); err == nil {
		t.Fatal("RunIndexer(unknown) returned no error")
	}
}

func TestStartRunsOnceAndStopsOnCancel(t *testing.T) {
	results := make(chan RunOutcome, 16)
	st, runner := newHarness(t, 2, func(outcome RunOutcome) {
		select {
		case results <- outcome:
		default:
		}
	})
	server := newNativeServer(t, 0, nil)
	seedNative(t, st, "native-one", "native-one", "Native One", server.URL, true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner.Start(ctx)

	select {
	case outcome := <-results:
		if outcome.Skipped || outcome.Err != nil || outcome.Stats == nil {
			t.Fatalf("first outcome = %+v", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not run the first pass")
	}

	cancel()
	done := make(chan struct{})
	go func() {
		runner.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after cancellation")
	}
}

func TestOnResultReceivesOneOutcomePerIndexer(t *testing.T) {
	var mu sync.Mutex
	got := make(map[int64]RunOutcome)
	st, runner := newHarness(t, 2, func(outcome RunOutcome) {
		mu.Lock()
		got[outcome.IndexerID] = outcome
		mu.Unlock()
	})
	server := newNativeServer(t, 0, nil)
	nativeID := seedNative(t, st, "native-one", "native-one", "Native One", server.URL, true)
	legacyID := seedLegacy(t, st, "legacy-torznab", "legacy-torznab", "Legacy Torznab")
	disabledID := seedNative(t, st, "native-disabled", "native-disabled", "Disabled Native", server.URL, false)

	outcomes := runner.RunOnce(context.Background())
	if len(outcomes) != 3 {
		t.Fatalf("outcomes = %d, want 3", len(outcomes))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != len(outcomes) {
		t.Fatalf("OnResult calls = %d, want %d", len(got), len(outcomes))
	}
	for _, id := range []int64{nativeID, legacyID, disabledID} {
		if _, ok := got[id]; !ok {
			t.Errorf("OnResult did not receive indexer %d", id)
		}
	}
}
