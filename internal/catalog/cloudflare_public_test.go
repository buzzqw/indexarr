package catalog_test

// Offline checks for the public sources taken from the Cloudflare group of the
// "to write" queue on 2026-10-27. The captured pages come from the local
// FlareSolverr; the tests run the real definition engine against them, so the
// selectors are exercised without a network call.

import (
	"context"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestCloudflarePublicSourceFixtures(t *testing.T) {
	tests := []struct {
		id            string
		searchPath    string
		searchFixture string
		detailFixture string
		minRows       int
		needMagnet    bool
		needDownload  bool
		needSize      bool
		needSeeders   bool
		grabMagnet    bool
	}{
		{"extratorrent-st", "/search/", "extratorrent-st/search.html", "", 20, true, false, true, true, false},
		{"kickasstorrents-to", "/search/", "kickasstorrents-to/search.html", "", 20, true, false, true, true, false},
		{"magnetcat", "/search", "magnetcat/search.html", "", 10, true, false, true, false, false},
		{"btetree", "/", "btetree/search.html", "btetree/search.html", 20, false, true, true, true, false},
		{"damagnet", "/", "damagnet/search.html", "damagnet/detail.html", 10, false, false, true, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			server := serveSearchOrDetail(t, tc.searchPath, tc.searchFixture, tc.detailFixture)
			connector := nativeConnector(t, tc.id, server.URL)
			items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "matrix"})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(items) < tc.minRows {
				t.Fatalf("items = %d, want at least %d", len(items), tc.minRows)
			}
			withSeeders := 0
			for i, item := range items {
				if item.Title == "" {
					t.Errorf("item %d: empty title", i)
				}
				if item.DetailsURL == "" && item.Magnet == "" && item.DownloadURL == "" {
					t.Errorf("item %d (%q): no details, magnet or torrent", i, item.Title)
				}
				if tc.needMagnet && !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") {
					t.Errorf("item %d (%q): magnet = %q", i, item.Title, item.Magnet)
				}
				if tc.needDownload && !strings.HasSuffix(strings.ToLower(item.DownloadURL), ".torrent") {
					t.Errorf("item %d (%q): download = %q", i, item.Title, item.DownloadURL)
				}
				if tc.needSize && item.Size <= 0 {
					t.Errorf("item %d (%q): size = %d", i, item.Title, item.Size)
				}
				if item.Seeders > 0 {
					withSeeders++
				}
			}
			if tc.needSeeders && withSeeders == 0 {
				t.Error("no row has a seeder count")
			}
			if tc.grabMagnet {
				payload, err := connector.Download(context.Background(), items[0])
				if err != nil {
					t.Fatalf("Download: %v", err)
				}
				if !strings.HasPrefix(payload.Magnet, "magnet:?xt=urn:btih:") {
					t.Errorf("Download magnet = %q", payload.Magnet)
				}
			}
		})
	}
}

// TestKickassWSMirrorLayout runs the existing Kickass definition against the
// page captured from kickass.ws, the second mirror. It pins the claim that the
// click-through magnet and the category anchors are the same on both hosts.
func TestKickassWSMirrorLayout(t *testing.T) {
	server := serveSearchOrDetail(t, "/usearch/", "kickasstorrents/ws-search.html", "kickasstorrents/ws-search.html")
	connector := nativeConnector(t, "kickasstorrents", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "matrix"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 20 {
		t.Fatalf("items = %d, want at least 20", len(items))
	}
	withCategory := 0
	for i, item := range items {
		if !strings.HasPrefix(item.Magnet, "magnet:?xt=urn:btih:") {
			t.Errorf("item %d (%q): magnet = %q", i, item.Title, item.Magnet)
		}
		if item.Size <= 0 {
			t.Errorf("item %d (%q): size = %d", i, item.Title, item.Size)
		}
		if len(item.Categories) > 0 && item.Categories[0] != 0 {
			withCategory++
		}
	}
	if withCategory == 0 {
		t.Error("no row has a mapped category")
	}
}
