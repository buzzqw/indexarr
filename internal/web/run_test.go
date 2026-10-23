package web

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestMetricsEndpoint(t *testing.T) {
	env := newTestEnv(t)
	recorder := get(t, env.handler, "/metrics")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, metric := range []string{
		"indexarr_up 1",
		"indexarr_releases ",
		"indexarr_sources ",
		"indexarr_blocklist_rules ",
		"indexarr_uptime_seconds ",
	} {
		if !strings.Contains(body, metric) {
			t.Errorf("metric missing: %s", metric)
		}
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "text/plain") {
		t.Errorf("content type = %q", contentType)
	}
}

func TestRunDetail(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	sourceID, err := env.store.CreateSource(ctx, store.Source{
		Name: "Run source", Kind: "torznab", Manager: "auto",
		URL: "http://run.example/api", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	runID, started, err := env.store.BeginSourceRun(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	source, _ := env.store.GetSource(ctx, sourceID)
	if err := env.store.FinishSourceRun(ctx, runID, source, started,
		store.RunResult{Seen: 5, New: 2, Blocked: 1}, nil); err != nil {
		t.Fatal(err)
	}
	recorder := get(t, env.handler, "/runs/"+strconvI(runID))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, want := range []string{"Run source", "Blocked"} {
		if !strings.Contains(body, want) {
			t.Errorf("run page missing %q", want)
		}
	}
}
