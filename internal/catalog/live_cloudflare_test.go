//go:build live

package catalog_test

// Manual live check for the Cloudflare definitions. Run with:
//
//	go test ./internal/catalog -tags live -run TestLiveCloudflare -v
//
// It needs FlareSolverr on 127.0.0.1:8191 and is never run by the normal suite.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestLiveCloudflare(t *testing.T) {
	solver := engine.NewFlareSolverrSolver("http://127.0.0.1:8191", 90*time.Second)
	transport := engine.NewTransport(engine.TransportConfig{Solver: solver})
	for _, tc := range []struct {
		id      string
		minRows int
		grab    bool
	}{
		{"extratorrent-st", 5, false},
		{"kickasstorrents-to", 5, false},
		{"kickasstorrents", 5, false},
		{"kickasstorrents-ws", 5, false},
		{"magnetcat", 5, false},
		{"btetree", 5, true},
		{"damagnet", 5, true},
	} {
		t.Run(tc.id, func(t *testing.T) {
			id := tc.id
			if id == "kickasstorrents-ws" {
				id = "kickasstorrents"
			}
			def := parseDefinition(t, id)
			if tc.id == "kickasstorrents-ws" {
				def.Meta.URLs[0] = "https://kickass.ws/"
				def.Meta.ID = "kickasstorrents-ws"
			}
			connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
				Indexer:    engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
				Definition: def,
				Settings:   map[string]string{},
				Transport:  transport,
			})
			if err != nil {
				t.Fatalf("NewDefinitionConnector: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			items, err := connector.Search(ctx, engine.SearchRequest{Mode: engine.ModeSearch, Query: "matrix"})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(items) < tc.minRows {
				t.Fatalf("items = %d, want at least %d", len(items), tc.minRows)
			}
			t.Logf("rows=%d first=%q magnet=%t download=%t size=%d", len(items), items[0].Title, items[0].Magnet != "", items[0].DownloadURL != "", items[0].Size)
			if tc.grab {
				payload, err := connector.Download(ctx, items[0])
				if err != nil {
					t.Fatalf("Download: %v", err)
				}
				switch {
				case payload.Magnet != "":
					t.Logf("grab magnet=%s", payload.Magnet[:min(60, len(payload.Magnet))])
				case len(payload.Body) > 0:
					t.Logf("grab body=%d content-type=%s", len(payload.Body), payload.ContentType)
				default:
					t.Fatal("grab returned neither magnet nor body")
				}
				if tc.id == "btetree" && !strings.Contains(payload.ContentType, "bittorrent") && len(payload.Body) < 1000 {
					t.Fatalf("btetree grab looks wrong: %d bytes %q", len(payload.Body), payload.ContentType)
				}
			}
		})
	}
}
