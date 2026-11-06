package indexer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/store"
)

// TestCircuitOpenUntilIncludesTheHostCooldown proves that the "next retry"
// the interface and the Prowlarr status report is the time the operator really
// waits. A 429 opens the per-indexer breaker for the base cooldown and the
// shared host breaker for the longer, learned one: when only the host breaker
// is open, the source is still paused and the caller must get that instant.
func TestCircuitOpenUntilIncludesTheHostCooldown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	transport := engine.NewTransport(engine.TransportConfig{
		AllowPrivate: true,
		Cooldown:     time.Minute,
	})
	manager := &Manager{
		transport: transport,
		cache:     map[int64]cachedConnector{},
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	indexer := store.Indexer{
		ID:         1,
		Slug:       "tgx-one",
		ConfigJSON: `{"base_url":"` + server.URL + `/"}`,
	}
	req, err := http.NewRequest(http.MethodGet, server.URL+"/get-posts/user:one/", nil)
	if err != nil {
		t.Fatal(err)
	}
	// An empty breaker key makes the transport count the request as a host
	// probe only, so the per-indexer entry stays closed.
	if _, err := transport.Do(context.Background(), "", req); !errors.Is(err, engine.ErrRateLimited) {
		t.Fatalf("Do = %v, want ErrRateLimited", err)
	}
	if _, ok := transport.BreakerOpenUntil(indexer.Slug); ok {
		t.Fatal("the per-indexer breaker is open, the test needs only the host one")
	}

	until, ok := manager.CircuitOpenUntil(indexer)
	if !ok {
		t.Fatal("CircuitOpenUntil = false, want the host cooldown")
	}
	if remaining := time.Until(until); remaining < 100*time.Second {
		t.Fatalf("CircuitOpenUntil = %s (%s away), want the host Retry-After", until, remaining)
	}
}
