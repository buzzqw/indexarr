package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/store"
)

func newWP9Env(t *testing.T) *testEnv {
	t.Helper()
	env := newTestEnv(t)
	svc, err := indexersvc.New(indexersvc.Config{Store: env.store})
	if err != nil {
		t.Fatalf("indexersvc.New: %v", err)
	}
	env.server.svc = svc
	return env
}

func recordWP9Run(t *testing.T, env *testEnv, indexerID int64, started time.Time, metrics *store.RunMetrics) {
	t.Helper()
	if _, err := env.store.RecordIndexerHistory(context.Background(), store.IndexerHistory{
		IndexerID: indexerID, Kind: "run", Status: "ok",
		LatencyMS: 120, StartedAt: started, Metrics: metrics,
	}); err != nil {
		t.Fatalf("RecordIndexerHistory: %v", err)
	}
}

func TestDegradedStateComesFromTheSameModelEverywhere(t *testing.T) {
	env := newWP9Env(t)
	indexerID := seedWP17Indexer(t, env, "wp9-low", "WP9 Low")
	now := time.Now()
	recordWP9Run(t, env, indexerID, now.Add(-time.Hour), &store.RunMetrics{
		Seen: 10, ItemsNew: 10,
		FieldTotal: 10, FieldSize: 10, FieldSeeders: 1,
		FieldCategory: 10, FieldDate: 10, FieldHash: 10,
	})

	status := env.server.HomeStatus(context.Background())
	if status.Level != "ok" || status.ProblemIndexers != 0 || len(status.Problems) != 0 {
		t.Fatalf("low completeness must be neutral: HomeStatus = %#v", status)
	}

	home := get(t, env.handler, "/").Body.String()
	if strings.Contains(home, "Some fields are missing") || strings.Contains(home, "indexers have problems") {
		t.Errorf("complete fields became a home alarm: %s", home)
	}

	list := get(t, env.handler, "/indexers").Body.String()
	if strings.Contains(list, "Degraded") || strings.Contains(list, "This site does not provide seeders") {
		t.Errorf("site characteristic shown as a problem in the list: %s", list)
	}
	if !strings.Contains(list, `data-state="ok"`) {
		t.Error("the healthy list row does not carry the ok state")
	}

	detail := get(t, env.handler, "/indexers/"+strconv.FormatInt(indexerID, 10)).Body.String()
	for _, want := range []string{"Catalog state", "verified", "What this site provides", "10% of the fields present"} {
		if !strings.Contains(detail, want) {
			t.Errorf("indexer detail missing %q", want)
		}
	}
}

func TestRepeatedZeroResultsDegradeTheSite(t *testing.T) {
	env := newWP9Env(t)
	indexerID := seedWP17Indexer(t, env, "wp9-empty", "WP9 Empty")
	now := time.Now()
	for i := 0; i < 3; i++ {
		recordWP9Run(t, env, indexerID, now.Add(-time.Duration(i+1)*time.Hour), &store.RunMetrics{
			Seen: 0, FieldTotal: 0,
		})
	}
	status := env.server.HomeStatus(context.Background())
	if status.ProblemIndexers != 1 || status.Problems[0].Code != "home.problem.zero" {
		t.Fatalf("HomeStatus = %#v", status)
	}
	detail := get(t, env.handler, "/indexers/"+strconv.FormatInt(indexerID, 10)).Body.String()
	if !strings.Contains(detail, "no results in the last 3 checks") {
		t.Errorf("detail does not explain the repeated empty answers: %s", detail)
	}
}

func TestRepeatedFailuresDegradeTheSite(t *testing.T) {
	env := newWP9Env(t)
	indexerID := seedWP17Indexer(t, env, "wp9-errors", "WP9 Errors")
	now := time.Now()
	for i := 0; i < 3; i++ {
		if _, err := env.store.RecordIndexerHistory(context.Background(), store.IndexerHistory{
			IndexerID: indexerID, Kind: "run", Status: "error",
			Error: "HTTP 429 too many requests", LatencyMS: 90,
			StartedAt: now.Add(-time.Duration(i+1) * time.Hour),
		}); err != nil {
			t.Fatalf("RecordIndexerHistory: %v", err)
		}
	}
	status := env.server.HomeStatus(context.Background())
	if status.ProblemIndexers != 1 {
		t.Fatalf("HomeStatus = %#v", status)
	}
	if status.Problems[0].Code != "home.problem.rateLimit" {
		t.Errorf("home problem code = %q, want the concrete rate-limit cause", status.Problems[0].Code)
	}
	list := get(t, env.handler, "/indexers").Body.String()
	if !strings.Contains(list, "The site is limiting requests") {
		t.Errorf("list does not explain the repeated failures: %s", list)
	}
	detail := get(t, env.handler, "/indexers/"+strconv.FormatInt(indexerID, 10)).Body.String()
	if !strings.Contains(detail, "The site is limiting requests") {
		t.Errorf("detail does not show the concrete failure cause: %s", detail)
	}
}

func TestDisabledIndexerIsNotAProblem(t *testing.T) {
	env := newWP9Env(t)
	indexerID := seedWP17Indexer(t, env, "wp9-disabled", "WP9 Disabled")
	in, err := env.store.GetIndexer(context.Background(), indexerID)
	if err != nil {
		t.Fatal(err)
	}
	in.Enabled = false
	if err := env.store.UpdateIndexer(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if err := env.store.UpsertIndexerState(context.Background(), store.IndexerState{
		IndexerID: indexerID, LastError: "HTTP 403",
	}); err != nil {
		t.Fatal(err)
	}
	status := env.server.HomeStatus(context.Background())
	if status.ProblemIndexers != 0 || status.DisabledIndexers != 1 {
		t.Fatalf("HomeStatus = %#v", status)
	}
	list := get(t, env.handler, "/indexers").Body.String()
	if !strings.Contains(list, "Disabled") {
		t.Error("list does not show the disabled state")
	}
}

func TestDegradedPageUsesTheRequestedLanguage(t *testing.T) {
	env := newWP9Env(t)
	indexerID := seedWP17Indexer(t, env, "wp9-it", "WP9 IT")
	recordWP9Run(t, env, indexerID, time.Now().Add(-time.Hour), &store.RunMetrics{
		Seen: 10, FieldTotal: 10, FieldSize: 10, FieldSeeders: 1,
		FieldCategory: 10, FieldDate: 10, FieldHash: 10,
	})
	recorder := get(t, env.handler, "/indexers/"+strconv.FormatInt(indexerID, 10)+"?lang=it")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{"Cosa fornisce questo sito", "Stato del catalogo", "10% dei campi presenti"} {
		if !strings.Contains(body, want) {
			t.Errorf("italian detail missing %q", want)
		}
	}
	if strings.Contains(body, "Degradato") {
		t.Error("completeness still degrades the Italian detail")
	}
}
