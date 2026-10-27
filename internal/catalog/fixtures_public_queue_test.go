package catalog_test

// Offline checks for the public sources taken from the "to write" queue on
// 2026-10-27. Each test runs the real definition engine against the captured
// pages. Release pages are served for every other path, so definitions that
// resolve the magnet during the search are exercised end to end; for those
// that resolve it at grab time with a magnet on the page, Download is checked
// too. Grabs of a .torrent are not run here: the captured links are absolute
// and point to the real sites.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestQueuePublicSourceFixtures(t *testing.T) {
	tests := []struct {
		id            string
		searchPath    string
		searchFixture string
		detailFixture string
		charset       string
		minRows       int
		needLink      bool // magnet or .torrent on every row after the search
		needSize      bool
		grabMagnet    bool
		category      int
		cyrillic      bool
	}{
		{"torrentdownload", "/search", "torrentdownload/search.html", "", "", 20, true, true, false, 0, false},
		{"magnetdownload", "/search/matrix/", "magnetdownload/search.html", "magnetdownload/info.json", "", 10, true, true, false, 0, false},
		{"blueroms", "/search", "blueroms/search.html", "blueroms/download.html", "", 20, true, true, false, 0, false},
		{"xxxclub", "/torrents/search/all/matrix", "xxxclub/search.html", "", "", 20, true, true, false, 0, false},
		{"freejavtorrent", "/s.php", "freejavtorrent/search.html", "freejavtorrent/download.html", "", 20, false, true, false, 6000, false},
		{"pornxlab", "/s.php", "pornxlab/search.html", "pornxlab/download.html", "", 20, false, true, false, 6000, false},
		{"bigfangroup", "/browse.php", "bigfangroup/search.html", "", "windows-1251", 10, true, true, false, 0, true},
		{"catorrent", "/index.php", "catorrent/search.html", "catorrent/detail.html", "", 10, false, false, false, 4050, false},
		{"skidrowrepack", "/", "skidrowrepack/search.html", "skidrowrepack/detail.html", "", 10, false, false, false, 4050, false},
		{"pctorrent", "/", "pctorrent/search.html", "pctorrent/detail.html", "windows-1251", 10, false, true, false, 4050, false},
		{"zamundalife", "/api/torrents", "zamundalife/search.json", "", "", 10, true, true, false, 0, false},
		{"mypornclub", "/s/matrix", "mypornclub/search.html", "mypornclub/detail.html", "", 20, false, true, true, 6000, false},
		{"pornotorrent", "/", "pornotorrent/search.html", "pornotorrent/detail.html", "", 10, false, false, true, 6000, false},
		{"mixtapetorrent", "/search/node/matrix", "mixtapetorrent/search.html", "mixtapetorrent/detail.html", "", 5, false, false, false, 3000, false},
		{"sosulki", "/", "sosulki/search.html", "sosulki/detail.html", "", 10, false, true, false, 6000, true},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.charset != "" {
					w.Header().Set("Content-Type", "text/html; charset="+tc.charset)
				}
				if r.URL.Path == tc.searchPath {
					serveFile(t, w, tc.searchFixture)
					return
				}
				if tc.detailFixture == "" {
					http.NotFound(w, r)
					return
				}
				serveFile(t, w, tc.detailFixture)
			}))
			t.Cleanup(server.Close)

			connector := nativeConnector(t, tc.id, server.URL)
			items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "matrix"})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(items) < tc.minRows {
				t.Fatalf("items = %d, want at least %d", len(items), tc.minRows)
			}
			cyrillic := false
			for i, item := range items {
				if item.Title == "" || !utf8.ValidString(item.Title) || strings.ContainsRune(item.Title, utf8.RuneError) {
					t.Errorf("item %d: bad title %q", i, item.Title)
				}
				if item.DetailsURL == "" && item.Magnet == "" {
					t.Errorf("item %d (%q): no details and no magnet", i, item.Title)
				}
				if tc.needLink && item.Magnet == "" && item.DownloadURL == "" {
					t.Errorf("item %d (%q): no magnet or torrent", i, item.Title)
				}
				if tc.needSize && item.Size <= 0 {
					t.Errorf("item %d (%q): size=%d", i, item.Title, item.Size)
				}
				if tc.category != 0 && (len(item.Categories) != 1 || item.Categories[0] != tc.category) {
					t.Errorf("item %d (%q): categories=%v, want %d", i, item.Title, item.Categories, tc.category)
				}
				for _, r := range item.Title {
					if r >= 0x0410 && r <= 0x044f {
						cyrillic = true
					}
				}
			}
			if tc.cyrillic && !cyrillic {
				t.Errorf("no Cyrillic title decoded; first title %q", items[0].Title)
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

// TestBlueRomsDecodesBase64Magnet pins the decoded magnet of the captured
// download page, so a change of the base64 filter cannot pass silently.
func TestBlueRomsDecodesBase64Magnet(t *testing.T) {
	server := serveSearchOrDetail(t, "/search", "blueroms/search.html", "blueroms/download.html")
	connector := nativeConnector(t, "blueroms", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "mario"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("no rows")
	}
	if want := "magnet:?xt=urn:btih:5abe078babce41fdf371c9b0a1bb7b8bf175db25"; !strings.HasPrefix(items[0].Magnet, want) {
		t.Errorf("magnet = %q, want prefix %q", items[0].Magnet, want)
	}
}
