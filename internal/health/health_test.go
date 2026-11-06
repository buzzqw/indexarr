package health

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/notify"
	"github.com/buzzqw/indexarr/internal/store"
)

func clean(seen int64) Outcome {
	return Outcome{
		Kind: "run", Status: "ok", HasSeen: true, Seen: seen,
		FieldTotal: seen, FieldSize: seen, FieldSeeders: seen,
		FieldCategory: seen, FieldDate: seen, FieldHash: seen,
	}
}

func emptyOutcome() Outcome {
	return Outcome{Kind: "run", Status: "ok", HasSeen: true}
}

func failed() Outcome {
	return Outcome{Kind: "run", Status: "error"}
}

func lowCompleteness() Outcome {
	return Outcome{
		Kind: "run", Status: "ok", HasSeen: true, Seen: 10,
		FieldTotal: 10, FieldSize: 10, FieldSeeders: 1,
		FieldCategory: 10, FieldDate: 10, FieldHash: 10,
	}
}

// TestCheckGroupsKeepTheFiltersOfOneSiteTogether locks the order of a health
// pass: the filters of one definition are checked one after the other, because
// probing them at the same time makes the site answer with a rate limit.
func TestCheckGroupsKeepTheFiltersOfOneSiteTogether(t *testing.T) {
	rows := []store.Indexer{
		{ID: 1, DefinitionID: "torrentgalaxy", Enabled: true},
		{ID: 2, DefinitionID: "extto", Enabled: true},
		{ID: 3, DefinitionID: "torrentgalaxy", Enabled: true},
		{ID: 4, DefinitionID: "extto", Enabled: false},
		{ID: 5, DefinitionID: "", Enabled: true},
		{ID: 6, DefinitionID: "", Enabled: true},
	}
	groups := checkGroups(rows)
	want := [][]int64{{1, 3}, {2}, {5}, {6}}
	if len(groups) != len(want) {
		t.Fatalf("groups = %d, want %d: %#v", len(groups), len(want), groups)
	}
	for i, group := range groups {
		for j, target := range group {
			if target.indexer.ID != want[i][j] {
				t.Errorf("group %d[%d] = %d, want %d", i, j, target.indexer.ID, want[i][j])
			}
		}
	}
}

func TestNextStateTransitions(t *testing.T) {
	thresholds := DefaultThresholds()
	cases := []struct {
		name    string
		current string
		recent  []Outcome
		want    string
	}{
		{"draft never promoted", CatalogDraft, []Outcome{clean(5), clean(5), clean(5)}, CatalogDraft},
		{"draft never degraded", CatalogDraft, []Outcome{failed(), failed(), failed()}, CatalogDraft},
		{"unknown state untouched", "custom", []Outcome{failed(), failed(), failed()}, "custom"},
		{"no data stays verified", CatalogVerified, nil, CatalogVerified},
		{"no data stays stable", CatalogStable, nil, CatalogStable},
		{"two failures are not enough", CatalogVerified, []Outcome{failed(), failed()}, CatalogVerified},
		{"exactly three failures degrade", CatalogVerified, []Outcome{failed(), failed(), failed()}, CatalogDegraded},
		{"stable degrades too", CatalogStable, []Outcome{failed(), failed(), failed()}, CatalogDegraded},
		{"two empty answers are not enough", CatalogVerified, []Outcome{emptyOutcome(), emptyOutcome()}, CatalogVerified},
		{"exactly three empty answers degrade", CatalogVerified, []Outcome{emptyOutcome(), emptyOutcome(), emptyOutcome()}, CatalogDegraded},
		{"canceled is not a failure", CatalogVerified, []Outcome{
			{Kind: "run", Status: "canceled"}, {Kind: "run", Status: "canceled"}, {Kind: "run", Status: "canceled"},
		}, CatalogVerified},
		{"completeness exactly at threshold is fine", CatalogVerified, []Outcome{{
			Kind: "run", Status: "ok", HasSeen: true, Seen: 10,
			FieldTotal: 10, FieldSize: 10, FieldSeeders: 5,
			FieldCategory: 10, FieldDate: 10, FieldHash: 10,
		}}, CatalogVerified},
		{"completeness below threshold is neutral", CatalogVerified, []Outcome{lowCompleteness()}, CatalogVerified},
		{"a clean outcome recovers a degraded site", CatalogDegraded, []Outcome{clean(5)}, CatalogVerified},
		{"an empty outcome does not recover", CatalogDegraded, []Outcome{emptyOutcome()}, CatalogDegraded},
		{"a low-completeness outcome does not keep a site degraded", CatalogDegraded, []Outcome{lowCompleteness()}, CatalogVerified},
		{"three clean outcomes make it stable", CatalogVerified, []Outcome{clean(5), clean(5), clean(5)}, CatalogStable},
		{"two clean outcomes are not enough for stable", CatalogVerified, []Outcome{clean(5), clean(5)}, CatalogVerified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NextState(tc.current, tc.recent, thresholds); got != tc.want {
				t.Errorf("NextState(%q, %d outcomes) = %q, want %q",
					tc.current, len(tc.recent), got, tc.want)
			}
		})
	}
}

func TestAssessUsesOneModel(t *testing.T) {
	thresholds := DefaultThresholds()

	if got := Assess(Input{Enabled: false, LastError: "boom"}, thresholds); got.State != StateDisabled {
		t.Errorf("disabled site = %q", got.State)
	}
	if got := Assess(Input{Enabled: true}, thresholds); got.State != StateTesting || got.Completeness != -1 {
		t.Errorf("no data = %#v", got)
	}
	if got := Assess(Input{Enabled: true, LastError: "HTTP 429"}, thresholds); got.State != StateProblem {
		t.Errorf("current error = %q, want problem", got.State)
	}
	if got := Assess(Input{Enabled: true, Circuit: "open"}, thresholds); got.State != StateProblem {
		t.Errorf("open circuit = %q, want problem", got.State)
	}
	if got := Assess(Input{Enabled: true, Outcomes: []Outcome{failed()}}, thresholds); got.State != StateProblem {
		t.Errorf("latest failure = %q, want problem", got.State)
	}
	if got := Assess(Input{Enabled: true, Outcomes: []Outcome{clean(4), failed()}}, thresholds); got.State != StateOK {
		t.Errorf("recovered site = %q, want ok", got.State)
	}
	// A stored error older than the newest clean execution is no longer the
	// current state: a site that answers now must not stay red because a run
	// failed before the last check.
	recovered := Assess(Input{Enabled: true, LastError: "request timed out", Outcomes: []Outcome{clean(4)}}, thresholds)
	if recovered.State != StateOK || len(recovered.Reasons) != 0 {
		t.Errorf("stale error = %q %#v, want ok", recovered.State, recovered.Reasons)
	}
	// The last row being a failure, or saying nothing (canceled), keeps the
	// stored error actionable.
	if got := Assess(Input{Enabled: true, LastError: "boom", Outcomes: []Outcome{failed()}}, thresholds); got.State != StateProblem {
		t.Errorf("failure after error = %q, want problem", got.State)
	}
	if got := Assess(Input{Enabled: true, LastError: "boom", Outcomes: []Outcome{{Kind: "run", Status: "canceled"}}}, thresholds); got.State != StateProblem {
		t.Errorf("canceled after error = %q, want problem", got.State)
	}

	degraded := Assess(Input{Enabled: true, Outcomes: []Outcome{emptyOutcome(), emptyOutcome(), emptyOutcome()}}, thresholds)
	if degraded.State != StateDegraded || degraded.ZeroResults != 3 || len(degraded.Reasons) == 0 {
		t.Errorf("repeated zero = %#v", degraded)
	}
	low := Assess(Input{Enabled: true, Outcomes: []Outcome{lowCompleteness()}}, thresholds)
	if low.State != StateOK || low.Completeness != 10 || len(low.Reasons) != 0 {
		t.Errorf("low completeness must be neutral = %#v", low)
	}
	searchZeros := []Outcome{
		{Kind: "search", Status: "ok", HasSeen: true},
		{Kind: "search", Status: "ok", HasSeen: true},
		{Kind: "search", Status: "ok", HasSeen: true},
	}
	if got := Assess(Input{Enabled: true, Outcomes: searchZeros}, thresholds); got.State != StateOK || got.ZeroResults != 0 {
		t.Errorf("search zeros degraded a site = %#v", got)
	}
	if got := Assess(Input{Enabled: true, FeedOnly: true, Outcomes: []Outcome{emptyOutcome(), emptyOutcome(), emptyOutcome()}}, thresholds); got.State != StateOK {
		t.Errorf("feed-only zero degraded a site = %#v", got)
	}
}

func TestOutcomesFromHistoryIgnoresTestsAndGrabs(t *testing.T) {
	rows := []store.IndexerHistory{
		{Kind: "grab", Status: "error"},
		{Kind: "test", Status: "error"},
		{Kind: "run", Status: "ok", Metrics: &store.RunMetrics{
			Seen: 4, FieldTotal: 4, FieldSize: 4, FieldSeeders: 4,
			FieldCategory: 4, FieldDate: 4, FieldHash: 4,
		}},
	}
	outcomes := OutcomesFromHistory(rows)
	if len(outcomes) != 1 || outcomes[0].Kind != "run" || !outcomes[0].HasSeen {
		t.Fatalf("outcomes = %#v", outcomes)
	}
	if got := NextState(CatalogVerified, outcomes, DefaultThresholds()); got != CatalogVerified {
		t.Errorf("state = %q", got)
	}
}

func TestCheckerTransitionsTheDefinitionAndKeepsIt(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "family", "testdata", "rss_feed.xml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var failing atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failing.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write(body)
	}))
	defer server.Close()
	failing.Store(true)

	st, err := store.Open(filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	definitionBody := "schema: 1\nconnector: rss\nmeta:\n  id: health-test\n  name: Health Test\n  type: public\n  protocol: torrent\n  urls:\n    - " +
		server.URL + "/rss\nhealth:\n  query: \"\"\n  min_rows: 1\n"
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID: "health-test", SchemaVersion: 1, Family: "rss", State: CatalogVerified,
		Origin: "local", Author: "test", Body: definitionBody, Hash: hash,
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	id, err := st.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "health-test", Protocol: "torrent", Slug: "health-test",
		Name: "Health Test", Enabled: true, ConfigJSON: `{"url":"` + server.URL + `/rss"}`,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	manager, err := indexer.New(indexer.Config{
		Store:     st,
		Transport: engine.NewTransport(engine.TransportConfig{AllowPrivate: true, Cooldown: 10 * time.Millisecond}),
	})
	if err != nil {
		t.Fatalf("indexer.New: %v", err)
	}
	var events []notify.Event
	checker, err := New(Config{
		Store: st, Manager: manager,
		Notify: func(_ context.Context, _ store.Settings, event notify.Event) {
			events = append(events, event)
		},
	})
	if err != nil {
		t.Fatalf("health.New: %v", err)
	}

	for i := 0; i < 3; i++ {
		results, err := checker.RunOnce(ctx)
		if err != nil {
			t.Fatalf("RunOnce %d: %v", i+1, err)
		}
		if len(results) != 1 || results[0].Err == nil {
			t.Fatalf("RunOnce %d results = %#v", i+1, results)
		}
	}
	stored, err := st.GetDefinition(ctx, "health-test")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != CatalogDegraded {
		t.Fatalf("after three failures state = %q, want degraded", stored.State)
	}
	if stored.Body != definitionBody || stored.Hash != hash {
		t.Error("the health transition rewrote the definition")
	}
	if len(events) != 1 || events[0].Kind != notify.KindIndexerState || events[0].Source != "Health Test" {
		t.Errorf("notifications = %#v", events)
	}
	if events[0].Previous != notify.StateOK || events[0].Current != notify.StateDegraded {
		t.Errorf("degraded notification states = %q -> %q", events[0].Previous, events[0].Current)
	}

	failing.Store(false)
	if _, err := checker.RunOnce(ctx); err != nil {
		t.Fatalf("recovery RunOnce: %v", err)
	}
	stored, err = st.GetDefinition(ctx, "health-test")
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != CatalogVerified {
		t.Fatalf("after one success state = %q, want verified", stored.State)
	}
	if stored.Body != definitionBody {
		t.Error("recovery rewrote the definition body")
	}
	// A recovery is a state change too: the operator who was told about the
	// degradation is told when the site is healthy again.
	if len(events) != 2 || events[1].Kind != notify.KindIndexerState {
		t.Fatalf("recovery notifications = %#v", events)
	}
	if events[1].Previous != notify.StateDegraded || events[1].Current != notify.StateOK {
		t.Errorf("recovery notification states = %q -> %q", events[1].Previous, events[1].Current)
	}

	history, err := st.ListIndexerHistory(ctx, id, 10)
	if err != nil {
		t.Fatal(err)
	}
	healthRows := 0
	for _, row := range history {
		if row.Kind == "health" {
			healthRows++
		}
	}
	if healthRows != 4 {
		t.Errorf("health history rows = %d, want 4", healthRows)
	}
}

func TestReasonCodesAreStable(t *testing.T) {
	for _, code := range []string{ReasonCircuit, ReasonLastError, ReasonErrors, ReasonZeroResults, ReasonPaused, ReasonCompleteness} {
		if strings.TrimSpace(code) == "" {
			t.Errorf("empty reason code")
		}
	}
}

func TestNextStateGroupUsesTheWorstFilter(t *testing.T) {
	thresholds := DefaultThresholds()
	failing := []Outcome{failed(), failed(), failed()}
	healthy := []Outcome{clean(5), clean(5), clean(5)}
	oneClean := []Outcome{clean(5)}
	oneEmpty := []Outcome{emptyOutcome()}

	if got := NextStateGroup(CatalogVerified, [][]Outcome{healthy, failing}, thresholds); got != CatalogDegraded {
		t.Errorf("a healthy sibling hid a failing filter: %q", got)
	}
	if got := NextStateGroup(CatalogDegraded, [][]Outcome{oneClean, healthy}, thresholds); got != CatalogVerified {
		t.Errorf("recovery with all filters clean = %q", got)
	}
	if got := NextStateGroup(CatalogDegraded, [][]Outcome{oneEmpty, healthy}, thresholds); got != CatalogDegraded {
		t.Errorf("an empty filter recovered the definition: %q", got)
	}
	if got := NextStateGroup(CatalogVerified, [][]Outcome{healthy, healthy}, thresholds); got != CatalogStable {
		t.Errorf("both filters stable = %q", got)
	}
	if got := NextStateGroup(CatalogVerified, [][]Outcome{healthy, oneClean}, thresholds); got != CatalogVerified {
		t.Errorf("one filter not yet stable = %q", got)
	}
	if got := NextStateGroup(CatalogDraft, [][]Outcome{failing}, thresholds); got != CatalogDraft {
		t.Errorf("draft promoted = %q", got)
	}
}

// TestPausedOutcomeIsNeitherFailureNorSuccess locks the breaker pause: the
// source was not contacted, so the row must not count as a failure and must
// not be evidence of a clean run.
func TestPausedOutcomeIsNeitherFailureNorSuccess(t *testing.T) {
	paused := Outcome{Kind: "run", Status: "paused", Error: "engine: circuit breaker open"}
	ok := clean(3)

	if got := consecutiveFailures([]Outcome{paused, ok}); got != 0 {
		t.Errorf("consecutiveFailures = %d, want 0", got)
	}
	if got := consecutiveClean([]Outcome{paused, ok, ok}); got != 0 {
		t.Errorf("consecutiveClean = %d, want 0", got)
	}
	if got := NextState(CatalogVerified, []Outcome{paused, ok, ok}, DefaultThresholds()); got != CatalogVerified {
		t.Errorf("paused promoted the definition to %q", got)
	}
	if got := NextStateGroup(CatalogDegraded, [][]Outcome{{paused, ok}, {ok, ok}}, DefaultThresholds()); got != CatalogDegraded {
		t.Errorf("paused recovered the definition to %q", got)
	}
}

// A run the breaker refused carries a metrics row with zero items, because it
// never reached the site. It must not be read as "the search found nothing":
// that sentence sent the operator to check a query while the real cause was
// the site's rate limit.
func TestPausedRunsAreNotEmptyAnswers(t *testing.T) {
	thresholds := DefaultThresholds()
	now := time.Date(2026, 11, 5, 15, 22, 0, 0, time.UTC)
	paused := Outcome{Kind: "run", Status: "paused", HasSeen: true, At: now}
	newer := Outcome{Kind: "run", Status: "paused", HasSeen: true, At: now.Add(4 * time.Hour)}

	got := Assess(Input{Enabled: true, Outcomes: []Outcome{newer, paused}}, thresholds)
	if got.State != StateDegraded {
		t.Fatalf("paused source = %q, want degraded", got.State)
	}
	if got.ZeroResults != 0 {
		t.Errorf("ZeroResults = %d, want 0: a paused run contacted nothing", got.ZeroResults)
	}
	found := false
	for _, reason := range got.Reasons {
		if reason.Code != ReasonPaused {
			continue
		}
		found = true
		if reason.Count != 2 {
			t.Errorf("paused count = %d, want 2", reason.Count)
		}
	}
	if !found {
		t.Fatalf("reasons = %#v, want %s", got.Reasons, ReasonPaused)
	}
	if !got.Since.Equal(now) {
		t.Errorf("Since = %s, want the oldest paused row %s", got.Since, now)
	}

	// A clean answer after the pause clears both the state and the start.
	recovered := Assess(Input{Enabled: true, Outcomes: []Outcome{clean(10), newer, paused}}, thresholds)
	if recovered.State != StateOK || !recovered.Since.IsZero() {
		t.Errorf("after a clean run = %q since %s, want ok and no start", recovered.State, recovered.Since)
	}

	// A health check refused by the breaker only says that the probe could not
	// run: the source's own last run was clean, so the source is not paused.
	healthPause := Outcome{Kind: "health", Status: "paused", HasSeen: true, At: now}
	if got := Assess(Input{Enabled: true, Outcomes: []Outcome{healthPause, clean(10)}}, thresholds); got.State != StateOK {
		t.Errorf("a paused health check alone = %q, want ok", got.State)
	}
}

// The start of the problem is the oldest row of the streak, not the moment the
// last error was written. A day-old outage must not look like a new one.
func TestProblemSinceFollowsTheWholeStreak(t *testing.T) {
	t1 := time.Date(2026, 11, 5, 10, 34, 0, 0, time.UTC)
	t2 := t1.Add(2 * time.Hour)
	t3 := t2.Add(1 * time.Hour)
	outcomes := []Outcome{
		{Kind: "run", Status: "paused", At: t3},
		{Kind: "run", Status: "error", At: t2},
		{Kind: "run", Status: "ok", Seen: 10, HasSeen: true, At: t1},
	}
	if got := problemSince(outcomes); !got.Equal(t2) {
		t.Errorf("problemSince = %s, want %s", got, t2)
	}
	// A canceled row says nothing and is skipped, not used as a start.
	if got := problemSince([]Outcome{{Kind: "run", Status: "canceled", At: t3}, {Kind: "run", Status: "error", At: t2}}); !got.Equal(t2) {
		t.Errorf("problemSince with a canceled head = %s, want %s", got, t2)
	}
	if got := problemSince([]Outcome{clean(10)}); !got.IsZero() {
		t.Errorf("problemSince on a healthy source = %s, want zero", got)
	}
}

// A restart cancels the in-flight health pass. That must not become one
// warning per indexer: the site did nothing wrong and the model already
// ignores the canceled row.
func TestHealthCheckCanceledIsNotAWarning(t *testing.T) {
	if !healthCheckCanceled(fmt.Errorf("indexer site: health: %w", context.Canceled)) {
		t.Error("a wrapped cancellation is not recognised")
	}
	if healthCheckCanceled(errors.New("health query returned 0 items")) {
		t.Error("a real health failure is treated as a cancellation")
	}
}
