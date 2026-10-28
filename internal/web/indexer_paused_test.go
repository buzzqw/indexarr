package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/health"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

// TestPausedSourceIsExplained proves the pages read the live breaker: a source
// the breaker refused after repeated failures is shown as paused with the
// explanation the catalog already has, not as a generic problem.
func TestPausedSourceIsExplained(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "paused.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	body := fmt.Sprintf(`schema: 1
meta:
  id: paused-site
  name: Paused Site
  type: public
  protocol: torrent
  urls: ["%s/"]
capabilities:
  search: [q]
search:
  request:
    get: /search
  response:
    type: html
    rows: "table.results tr"
    fields:
      title: { selector: "td.name a" }
    required: [title]
`, server.URL)
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID: "paused-site", Body: body, State: "verificata", Origin: "local",
	}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "paused-site",
		Protocol:     "torrent",
		Slug:         "paused-site",
		Name:         "Paused Site",
		Enabled:      true,
		ConfigJSON:   fmt.Sprintf(`{"url":%q}`, server.URL),
	})
	if err != nil {
		t.Fatal(err)
	}

	key := make([]byte, 32)
	key[0] = 1
	v, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := indexer.New(indexer.Config{
		Store:     st,
		Vault:     v,
		Transport: engine.NewTransport(engine.TransportConfig{AllowPrivate: true, FailureThreshold: 1, Cooldown: time.Hour}),
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}

	// The first run fails for real and opens the breaker (threshold 1), the
	// second one is refused before the request is sent.
	if _, err := manager.Run(ctx, id); err == nil {
		t.Fatal("the first run did not fail")
	}
	if _, err := manager.Run(ctx, id); err == nil {
		t.Fatal("the second run was not refused by the breaker")
	}

	row, err := st.GetIndexer(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: st, native: manager}
	view := srv.loadIndexerHealth(ctx, row, nil, nil)
	if view.State.Circuit != string(engine.BreakerOpen) {
		t.Fatalf("circuit = %q, want open", view.State.Circuit)
	}
	if view.LastRun == nil || view.LastRun.Status != indexer.StatusPaused {
		t.Fatalf("last run = %+v, want a paused row", view.LastRun)
	}
	if view.Assessment.State != health.StateProblem {
		t.Fatalf("state = %q, want problem", view.Assessment.State)
	}
	found := false
	for _, reason := range view.Assessment.Reasons {
		if reason.Code == health.ReasonCircuit {
			found = true
		}
	}
	if !found {
		t.Fatal("the assessment does not mention the circuit breaker")
	}
	if code, _ := homeProblemCode(view); code != "home.problem.circuit" {
		t.Errorf("home problem = %q, want home.problem.circuit", code)
	}
}
