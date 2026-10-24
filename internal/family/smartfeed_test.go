package family

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/smartfeed"
)

func smartfeedFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "catalog", "definitions", "fixtures", "mircrew-smartfeed", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func newSmartfeedConnector(t *testing.T, body []byte, contentType string) (*RSS, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/smartfeed/feed" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	connector, err := NewRSS(RSSConfig{
		Indexer:   engine.IndexerRef{Slug: "mircrew-smartfeed", Name: "MirCrew"},
		URL:       server.URL + "/smartfeed/feed?u=1&e=SYNTHETICTOKEN&f=51&f=26&t=2&d=3&x=200",
		Transport: engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
		Smartfeed: true,
	})
	if err != nil {
		t.Fatalf("NewRSS: %v", err)
	}
	return connector, server
}

func TestSmartfeedRSSAndAtomBecomePendingTopics(t *testing.T) {
	cases := []struct {
		name        string
		fixture     string
		contentType string
		title       string
		category    int
	}{
		{"rss", "feed.rss", "application/rss+xml", "Reacher - Stagione 3 (2026) 1080p", 5040},
		{"atom", "feed.atom", "application/atom+xml", "VisionQuest - Stagione 1 (2026) 1080p", 5040},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			connector, _ := newSmartfeedConnector(t, smartfeedFixture(t, tc.fixture), tc.contentType)
			items, err := connector.Latest(context.Background(), engine.LatestRequest{Limit: 10})
			if err != nil {
				t.Fatalf("Latest: %v", err)
			}
			if len(items) < 2 {
				t.Fatalf("items = %d, want at least 2", len(items))
			}
			var found bool
			for _, item := range items {
				if strings.Contains(item.Title, "::") || strings.Contains(item.Title, "Risposta di") ||
					strings.Contains(item.Title, "CDATA") || strings.Contains(item.Title, "<") {
					t.Errorf("decorated title leaked: %q", item.Title)
				}
				if item.Magnet != "" || item.InfoHash != "" || item.DownloadURL != "" {
					t.Errorf("%q is not a pending topic: %+v", item.Title, item)
				}
				if item.DetailsURL == "" || strings.Contains(item.DetailsURL, "#") {
					t.Errorf("%q details URL = %q", item.Title, item.DetailsURL)
				}
				if item.Seeders != -1 || item.Leechers != -1 {
					t.Errorf("%q seeders = %d/%d, want unknown", item.Title, item.Seeders, item.Leechers)
				}
				if item.Attributes[smartfeed.AttributePending] != "true" {
					t.Errorf("%q is not marked pending: %v", item.Title, item.Attributes)
				}
				if item.Title == tc.title {
					found = true
					if len(item.Categories) == 0 || item.Categories[0] != tc.category {
						t.Errorf("%q categories = %v, want %d", item.Title, item.Categories, tc.category)
					}
					if item.Published.IsZero() {
						t.Errorf("%q lost the feed date", item.Title)
					}
				}
			}
			if !found {
				t.Errorf("cleaned title %q not found", tc.title)
			}
		})
	}
}

func TestSmartfeedExpiredFeedNamesTheUIPageWithoutTheToken(t *testing.T) {
	connector, _ := newSmartfeedConnector(t, []byte(`<!doctype html><html><body>login</body></html>`), "text/html")
	_, err := connector.Latest(context.Background(), engine.LatestRequest{})
	if err == nil {
		t.Fatal("an HTML login page was accepted as a feed")
	}
	message := err.Error()
	if !strings.Contains(message, "Smartfeed expired") || !strings.Contains(message, "/smartfeed/ui") {
		t.Errorf("error = %q, want an expiry message and the /smartfeed/ui link", message)
	}
	if strings.Contains(message, "SYNTHETICTOKEN") {
		t.Errorf("the token leaked into the error: %q", message)
	}
}

func TestSmartfeedNeverDownloadsDuringLatest(t *testing.T) {
	var topicRequests int
	connector, server := newSmartfeedConnector(t, smartfeedFixture(t, "feed.rss"), "application/rss+xml")
	// A separate topic path must stay untouched: Latest only reads the feed.
	connector.cfg.URL = server.URL + "/smartfeed/feed?u=1&e=SYNTHETICTOKEN&t=2"
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "viewtopic") {
			topicRequests++
		}
		_, _ = w.Write(smartfeedFixture(t, "feed.rss"))
	})
	if _, err := connector.Latest(context.Background(), engine.LatestRequest{}); err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if topicRequests != 0 {
		t.Fatalf("a poll opened %d topic page(s)", topicRequests)
	}
}
