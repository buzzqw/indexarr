package torznabapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/buzzqw/indexarr/internal/perf"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/torznabapi"
)

const benchAPIKey = "bench-torznab-key"

// BenchmarkTorznabSearch measures the full HTTP handler: settings lookup,
// cache search, provenance page query and RSS/Torznab XML generation. The
// store is pre-populated once, outside the timer.
func BenchmarkTorznabSearch(b *testing.B) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(b.TempDir(), "torznab-bench.db"))
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	defer st.Close()
	if err := perf.Seed(ctx, st, perf.Options{Releases: 100, APIKey: benchAPIKey}); err != nil {
		b.Fatalf("Seed: %v", err)
	}
	server := torznabapi.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), "bench")

	cases := []struct {
		name   string
		target string
	}{
		{"items_1", "/api?t=search&apikey=" + benchAPIKey + "&limit=1"},
		{"items_10", "/api?t=search&apikey=" + benchAPIKey + "&limit=10"},
		{"items_100", "/api?t=search&apikey=" + benchAPIKey + "&limit=100"},
		{"items_100_text", "/api?t=search&apikey=" + benchAPIKey + "&limit=100&q=1080p"},
		{"tvsearch_100", "/api?t=tvsearch&apikey=" + benchAPIKey + "&limit=100&q=benchmark&season=1&ep=1"},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			request := httptest.NewRequest(http.MethodGet, tc.target, nil)
			warm := httptest.NewRecorder()
			server.ServeHTTP(warm, request)
			if warm.Code != http.StatusOK {
				b.Fatalf("status = %d, body = %s", warm.Code, warm.Body.String())
			}
			b.SetBytes(int64(warm.Body.Len()))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				recorder := httptest.NewRecorder()
				server.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK {
					b.Fatalf("status = %d", recorder.Code)
				}
			}
		})
	}
}
