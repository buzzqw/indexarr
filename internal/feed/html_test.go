package feed

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const simpleRSS = `<rss version="2.0"><channel><title>t</title>
	<item><title>Discovered release</title>
	<link>magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567</link></item>
	</channel></rss>`

func TestParseRSSHTMLSentinel(t *testing.T) {
	_, err := ParseRSS([]byte(`<!DOCTYPE html><html><head><title>x</title></head></html>`))
	if !errors.Is(err, ErrHTMLPage) {
		t.Fatalf("error = %v, want ErrHTMLPage", err)
	}
	// A valid feed that mentions an HTML tag in its text is not an HTML page.
	feed := `<rss version="2.0"><channel><item><title>How &lt;html&gt; works</title>
		<link>magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567</link></item></channel></rss>`
	if _, err := ParseRSS([]byte(feed)); err != nil {
		t.Fatalf("valid feed rejected: %v", err)
	}
}

func TestFetchRSSEmbeddedFeed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html><body><pre>`+simpleRSS+`</pre></body></html>`)
	}))
	defer server.Close()
	items, err := FetchRSS(context.Background(), server.Client(), server.URL, RequestOptions{})
	if err != nil {
		t.Fatalf("FetchRSS: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Discovered release" {
		t.Fatalf("items = %#v", items)
	}
}

func TestFetchRSSLinkDiscovery(t *testing.T) {
	var pageCalls, feedCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			pageCalls++
			_, _ = io.WriteString(w, `<html><head><link rel="alternate" type="application/rss+xml" href="/feed.xml"></head><body>site</body></html>`)
		case "/feed.xml":
			feedCalls++
			_, _ = io.WriteString(w, simpleRSS)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	items, err := FetchRSS(context.Background(), server.Client(), server.URL+"/page", RequestOptions{})
	if err != nil {
		t.Fatalf("FetchRSS: %v", err)
	}
	if len(items) != 1 || pageCalls != 1 || feedCalls != 1 {
		t.Errorf("items=%d page=%d feed=%d", len(items), pageCalls, feedCalls)
	}
}

func TestFetchRSSAnchorAndEmbeddedDiscovery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/browse":
			// Mirrors extto.org: the feed is linked from an anchor, and the
			// linked page wraps the XML in an HTML viewer.
			_, _ = io.WriteString(w, `<html><body><a href="/rss">RSS</a></body></html>`)
		case "/rss":
			_, _ = io.WriteString(w, `<html><body><pre>`+simpleRSS+`</pre></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	items, err := FetchRSS(context.Background(), server.Client(), server.URL+"/browse", RequestOptions{})
	if err != nil {
		t.Fatalf("FetchRSS: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Discovered release" {
		t.Fatalf("items = %#v", items)
	}
}

func TestFetchRSSWrongPageError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><head><title>Browse Torrents</title></head><body>not a feed</body></html>`)
	}))
	defer server.Close()
	_, err := FetchRSS(context.Background(), server.Client(), server.URL, RequestOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "parse feed") {
		t.Errorf("generic XML error is back: %v", err)
	}
	if !strings.Contains(err.Error(), "HTML page") || !strings.Contains(err.Error(), "Browse Torrents") {
		t.Errorf("error is not explanatory: %v", err)
	}
}

func TestTorznabHTMLIsReportedClearly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><title>Login</title></html>`)
	}))
	defer server.Close()
	client := NewClient(ClientConfig{URL: server.URL, Manager: ManagerTorznab})
	_, err := client.Fetch(context.Background(), "x", 10)
	if err == nil || !strings.Contains(err.Error(), "HTML instead of Torznab XML") {
		t.Fatalf("error = %v", err)
	}
}

func TestDiscoverFeedURLs(t *testing.T) {
	page := []byte(`<html><head>
		<link rel="alternate" type="application/atom+xml" href="/atom.xml">
		<link rel="stylesheet" href="/style.css">
		</head><body>
		<a href="/rss">RSS</a>
		<a href="/help">Help</a>
		<a href="https://other.example/feed">Feed</a>
		</body></html>`)
	base, err := url.Parse("https://site.example/browse/")
	if err != nil {
		t.Fatal(err)
	}
	got := discoverFeedURLs(page, base)
	want := []string{
		"https://site.example/atom.xml",
		"https://site.example/rss",
		"https://other.example/feed",
	}
	if len(got) != len(want) {
		t.Fatalf("candidates = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate %d = %q, want %q", i, got[i], want[i])
		}
	}
}
