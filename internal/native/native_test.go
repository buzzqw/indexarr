package native

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

func TestRunOnceRunsEveryNativeIndexer(t *testing.T) {
	st, runner := newHarness(t, 2, nil)
	server := newNativeServer(t, 0, nil)

	// The names are chosen so name order differs from id order, proving the
	// result is deterministic by indexer ID.
	firstID := seedNative(t, st, "native-one", "native-one", "Zulu Native", server.URL, true)
	secondID := seedNative(t, st, "native-two", "native-two", "Alpha Native", server.URL, true)

	outcomes := runner.RunOnce(context.Background())
	if len(outcomes) != 2 {
		t.Fatalf("outcomes = %d, want 2", len(outcomes))
	}
	if outcomes[0].IndexerID != firstID || outcomes[1].IndexerID != secondID {
		t.Fatalf("outcome ids = [%d, %d], want [%d, %d]",
			outcomes[0].IndexerID, outcomes[1].IndexerID, firstID, secondID)
	}
	for _, outcome := range outcomes {
		if outcome.Skipped || outcome.Err != nil {
			t.Fatalf("outcome = %+v", outcome)
		}
		if outcome.Stats == nil || outcome.Stats.Seen != 1 || outcome.Stats.New+outcome.Stats.Updated != 1 {
			t.Fatalf("stats = %+v", outcome.Stats)
		}
		if outcome.Duration <= 0 {
			t.Errorf("duration = %v", outcome.Duration)
		}
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
	if invalid.Err == nil {
		t.Fatalf("invalid outcome must report an error: %+v", invalid)
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
	disabledID := seedNative(t, st, "native-disabled", "native-disabled", "Disabled Native", server.URL, false)

	nativeOutcome, err := runner.RunIndexer(context.Background(), nativeID)
	if err != nil {
		t.Fatalf("RunIndexer(native): %v", err)
	}
	if nativeOutcome.Skipped || nativeOutcome.Stats == nil {
		t.Fatalf("native outcome = %+v", nativeOutcome)
	}

	disabledOutcome, err := runner.RunIndexer(context.Background(), disabledID)
	if err != nil {
		t.Fatalf("RunIndexer(disabled): %v", err)
	}
	if !disabledOutcome.Skipped || disabledOutcome.Reason != ReasonDisabled {
		t.Fatalf("disabled outcome = %+v", disabledOutcome)
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
	secondID := seedNative(t, st, "native-two", "native-two", "Native Two", server.URL, true)
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
	for _, id := range []int64{nativeID, secondID, disabledID} {
		if _, ok := got[id]; !ok {
			t.Errorf("OnResult did not receive indexer %d", id)
		}
	}
}

func TestDueIndexersHonoursPerIndexerInterval(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	indexers := []store.Indexer{
		{ID: 1, Enabled: true, IntervalSeconds: 0},   // global, last run 30m ago
		{ID: 2, Enabled: true, IntervalSeconds: 900}, // 15m, last run 20m ago -> due
		{ID: 3, Enabled: true, IntervalSeconds: 900}, // 15m, last run 5m ago -> not due
		{ID: 4, Enabled: false, IntervalSeconds: 0},  // disabled
	}
	states := map[int64]store.RunRetryState{
		1: {LastRun: now.Add(-30 * time.Minute)},
		2: {LastRun: now.Add(-20 * time.Minute)},
		3: {LastRun: now.Add(-5 * time.Minute)},
		4: {LastRun: now.Add(-24 * time.Hour)},
	}
	due := dueIndexers(indexers, states, time.Hour, now)
	if len(due) != 1 || due[0].ID != 2 {
		t.Fatalf("due = %+v, want only indexer 2", due)
	}
}

func TestDueIndexersNeverShortensTheGlobalInterval(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	indexers := []store.Indexer{
		{ID: 1, Enabled: true},                            // global
		{ID: 2, Enabled: true, IntervalSeconds: 6 * 3600}, // longer than global
	}
	states := map[int64]store.RunRetryState{
		1: {LastRun: now.Add(-59 * time.Minute)},
		2: {LastRun: now.Add(-2 * time.Hour)},
	}
	due := dueIndexers(indexers, states, time.Hour, now)
	if len(due) != 0 {
		t.Fatalf("due = %+v, want no indexer before its interval", due)
	}

	// At the global boundary the global indexer becomes due; the long one
	// still waits for its own six hours.
	due = dueIndexers(indexers, states, time.Hour, now.Add(time.Minute))
	if len(due) != 1 || due[0].ID != 1 {
		t.Fatalf("due at the boundary = %+v, want only indexer 1", due)
	}
}

func TestDueIndexersRunsIndexerWithoutHistory(t *testing.T) {
	now := time.Now()
	due := dueIndexers([]store.Indexer{{ID: 9, Enabled: true}}, map[int64]store.RunRetryState{}, time.Hour, now)
	if len(due) != 1 || due[0].ID != 9 {
		t.Fatalf("due = %+v, want the never-run indexer", due)
	}
}

func TestDuePassSkipsRecentlyRunIndexer(t *testing.T) {
	st, runner := newHarness(t, 2, nil)
	tracker := &inflightTracker{}
	server := newNativeServer(t, 0, tracker)
	addNativeDefinition(t, st, "due-native", server.URL)
	recentID := createIndexer(t, st, "due-native", "due-recent", "Due Recent", nativeConfig(server.URL), true)
	createIndexer(t, st, "due-native", "due-pending", "Due Pending", nativeConfig(server.URL), true)
	if _, err := st.RecordIndexerHistory(context.Background(), store.IndexerHistory{
		IndexerID: recentID, Kind: "run", Status: "ok", StartedAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordIndexerHistory: %v", err)
	}

	runner.runDuePass(context.Background())
	if got := tracker.requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want only the indexer without a recent run", got)
	}
}

// TestMaintenanceHonoursActivityRetention covers the 0 = keep forever rule:
// the scheduler must not fall back to a hidden 90-day cleanup when the
// operator explicitly asks to keep the activity history.
func TestMaintenanceHonoursActivityRetention(t *testing.T) {
	ctx := context.Background()
	st, runner := newHarness(t, 1, nil)
	old := time.Now().AddDate(0, 0, -100)
	if _, err := st.RecordIndexerHistory(ctx, store.IndexerHistory{
		Kind: "run", Status: "ok", StartedAt: old,
	}); err != nil {
		t.Fatalf("RecordIndexerHistory: %v", err)
	}

	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.HistoryRetentionDays = 0
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	runner.maintenance(ctx)
	var count int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM indexer_history`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("history rows after retention 0 = %d, want 1 (keep forever)", count)
	}

	settings.HistoryRetentionDays = 1
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	runner.maintenance(ctx)
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM indexer_history`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("history rows after retention 1 = %d, want 0", count)
	}
}

func TestSchedulerSkipsSearchOnlyIndexers(t *testing.T) {
	st, runner := newHarness(t, 2, nil)
	server := newNativeServer(t, 0, nil)

	latestID := seedNative(t, st, "native-latest", "native-latest", "Latest", server.URL, true)
	body := strings.Replace(nativeDefinition("native-search-only", server.URL),
		"capabilities:", "  latest: false\ncapabilities:", 1)
	putDefinition(t, st, store.Definition{ID: "native-search-only", Body: body})
	searchOnlyID := createIndexer(t, st, "native-search-only", "native-search-only",
		"Search only", nativeConfig(server.URL), true)

	outcomes := runner.RunOnce(context.Background())
	if len(outcomes) != 1 {
		t.Fatalf("outcomes = %d, want only the latest indexer", len(outcomes))
	}
	if outcomes[0].IndexerID != latestID || outcomes[0].Skipped {
		t.Fatalf("outcome = %+v, want latest indexer %d ran", outcomes[0], latestID)
	}

	manual, err := runner.RunIndexer(context.Background(), searchOnlyID)
	if err != nil {
		t.Fatalf("RunIndexer: %v", err)
	}
	if !manual.Skipped || manual.Reason != ReasonSearchOnly {
		t.Fatalf("manual outcome = %+v, want skipped search_only", manual)
	}
}

func TestSeederRefreshSkipsLocalOnlyAndLiveExcluded(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	manager := newTestManager(t, st)
	runner, err := New(Config{Store: st, Manager: manager, Logger: discardLogger(), Interval: time.Hour, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.NotFound(w, r)
	}))
	defer server.Close()

	putDefinition(t, st, store.Definition{
		ID: "rss-only", Family: "rss", State: "verificata",
		Body: "schema: 1\nconnector: rss\nmeta: {id: rss-only, name: RSS only, type: public, protocol: torrent, urls: [\"" + server.URL + "/\"]}\n",
	})
	rssID, err := st.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "rss-only", Protocol: "torrent", Slug: "rss-only", Name: "RSS only",
		Enabled: true, ConfigJSON: `{"url":"` + server.URL + `/rss"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	putDefinition(t, st, store.Definition{
		ID: "torrentgalaxy", Family: "torznab", State: "verificata",
		Body: "schema: 1\nmeta: {id: torrentgalaxy, name: TGx, type: public, protocol: torrent, urls: [\"" + server.URL + "/\"]}\n",
	})
	tgxID, err := st.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "torrentgalaxy", Protocol: "torrent", Slug: "tgx", Name: "TGx",
		Enabled: true, ConfigJSON: `{"uploader":"x"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, indexerID := range []int64{rssID, tgxID} {
		rel := store.Release{DedupKey: fmt.Sprintf("skip-%d", i), Title: "Recent Release", SearchTitle: "recent release",
			Category: "5040", PublishedAt: time.Now(), FirstSeenAt: time.Now(), LastSeenAt: time.Now()}
		if _, err := st.UpsertRelease(ctx, &rel); err != nil {
			t.Fatal(err)
		}
		if err := st.RecordReleaseSource(ctx, store.ReleaseSource{ReleaseID: rel.ID, IndexerID: indexerID}); err != nil {
			t.Fatal(err)
		}
	}
	runner.refreshSeeders(ctx, 6*time.Hour)
	if got := requests.Load(); got != 0 {
		t.Fatalf("feed-only/LiveExcluded indexers were queried %d time(s)", got)
	}
	candidates, err := st.SeedersCandidates(ctx, time.Now(), 6*time.Hour, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 0 {
		t.Fatalf("skipped rows were not marked checked: %+v", candidates)
	}
}

func TestTestManyProbesOnlyTheGivenIndexers(t *testing.T) {
	st := newTestStore(t)
	manager := newTestManager(t, st)
	runner, err := New(Config{Store: st, Manager: manager, Logger: discardLogger(), Interval: time.Hour, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	server := newNativeServer(t, 0, nil)
	first := seedNative(t, st, "many-first", "many-first", "Many First", server.URL, true)
	second := seedNative(t, st, "many-second", "many-second", "Many Second", server.URL, true)

	count, err := runner.TestMany(context.Background(), []int64{first})
	if err != nil {
		t.Fatalf("TestMany: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	deadline := time.Now().Add(10 * time.Second)
	for runner.testingAll.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runner.testingAll.Load() {
		t.Fatal("TestMany did not finish in time")
	}
	ctx := context.Background()
	firstHistory, err := st.ListIndexerHistory(ctx, first, 10)
	if err != nil {
		t.Fatal(err)
	}
	foundTest := false
	for _, row := range firstHistory {
		if row.Kind == "test" {
			foundTest = true
			break
		}
	}
	if !foundTest {
		t.Error("the selected indexer was not tested")
	}
	secondHistory, err := st.ListIndexerHistory(ctx, second, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondHistory) != 0 {
		t.Errorf("the second indexer was tested too: %+v", secondHistory)
	}
}

func TestDueIndexersFollowsTheRetrySchedule(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	indexerRow := store.Indexer{ID: 1, Enabled: true}
	for _, tc := range []struct {
		failures int
		wait     time.Duration
		due      bool
	}{
		{1, time.Hour, true},
		{2, 119 * time.Minute, false},
		{7, 47*time.Hour + 59*time.Minute, false},
		{8, 96 * time.Hour, true},
	} {
		state := map[int64]store.RunRetryState{
			1: {LastRun: now.Add(-tc.wait), Failures: tc.failures},
		}
		due := dueIndexers([]store.Indexer{indexerRow}, state, time.Hour, now)
		if tc.due && len(due) != 1 {
			t.Errorf("failures=%d: not due at %s", tc.failures, tc.wait)
		}
		if !tc.due && len(due) != 0 {
			t.Errorf("failures=%d: due before %s", tc.failures, tc.wait)
		}
	}

	// Nine failures mean the 96 hour step already failed: no more retries.
	paused := map[int64]store.RunRetryState{
		1: {LastRun: now.Add(-30 * 24 * time.Hour), Failures: 9},
	}
	if due := dueIndexers([]store.Indexer{indexerRow}, paused, time.Hour, now); len(due) != 0 {
		t.Fatalf("paused source is due: %+v", due)
	}

	// An edit after the failure resumes the normal interval.
	edited := indexerRow
	edited.UpdatedAt = now.Add(-time.Hour)
	state := map[int64]store.RunRetryState{
		1: {LastRun: now.Add(-2 * time.Hour), Failures: 9},
	}
	if due := dueIndexers([]store.Indexer{edited}, state, time.Hour, now); len(due) != 1 {
		t.Fatalf("edited source is not due: %+v", due)
	}
}
