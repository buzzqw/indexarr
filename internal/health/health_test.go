package health

import (
	"context"
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
	if len(events) != 1 || events[0].Kind != "indexer_degraded" || events[0].Source != "Health Test" {
		t.Errorf("notifications = %#v", events)
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
	for _, code := range []string{ReasonCircuit, ReasonLastError, ReasonErrors, ReasonZeroResults, ReasonCompleteness} {
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
