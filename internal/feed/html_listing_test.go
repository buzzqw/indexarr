package feed

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	xhtml "golang.org/x/net/html"
)

func mustParseHTML(t *testing.T, body []byte) *xhtml.Node {
	t.Helper()
	document, err := xhtml.Parse(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("parse HTML: %v", err)
	}
	return document
}

const hashA = "0123456789abcdef0123456789abcdef01234567"

func TestHTMLSiteForURL(t *testing.T) {
	cases := map[string]htmlSite{
		"https://extto.org/browse/?filter=u=x":                siteExtTo,
		"https://ext.to/browse/":                              siteExtTo,
		"https://torrentgalaxy.one/get-posts/user:MIRCrewRS/": siteTorrentGalaxy,
		"https://corsaro.example/torrent/1":                   siteCorsaro,
		"https://rss.knaben.org/ita///1337x":                  siteNone,
		"https://example.org/feed.xml":                        siteNone,
	}
	for raw, want := range cases {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := htmlSiteForURL(parsed); got != want {
			t.Errorf("htmlSiteForURL(%q) = %d, want %d", raw, got, want)
		}
	}
}

func TestExtToListing(t *testing.T) {
	body := []byte(`<html><body><table><tbody>
		<tr>
			<td><a href="/post-detail/1/example/">Example Release 1080p</a></td>
			<td><a href="magnet:?xt=urn:btih:` + hashA + `&amp;dn=Example%20Release">magnet</a></td>
		</tr>
		<tr>
			<td><a href="/post-detail/2/second/">Second Release 720p</a></td>
			<td><a href="magnet:?xt=urn:btih:fedcba9876543210fedcba9876543210fedcba98">magnet</a></td>
		</tr>
	</tbody></table></body></html>`)
	base, _ := url.Parse("https://extto.org/browse/?filter=c=TV&filter=u=test")
	items, err := parseExtToListing(context.Background(), nil, mustParseHTML(t, body), base, RequestOptions{})
	if err != nil {
		t.Fatalf("parseExtToListing: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d", len(items))
	}
	if items[0].Title != "Example Release 1080p" || items[0].InfoHash != hashA {
		t.Errorf("first item = %#v", items[0])
	}
	if items[0].Category != "5000" {
		t.Errorf("category = %q", items[0].Category)
	}
}

func TestExtToMagnetOnlyFallback(t *testing.T) {
	body := []byte(`<html><body>
		<a href="magnet:?xt=urn:btih:` + hashA + `&amp;dn=Only%20a%20Magnet">copy</a>
	</body></html>`)
	base, _ := url.Parse("https://extto.org/browse/")
	items, err := parseExtToListing(context.Background(), nil, mustParseHTML(t, body), base, RequestOptions{})
	if err != nil {
		t.Fatalf("parseExtToListing: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Only a Magnet" {
		t.Fatalf("items = %#v", items)
	}
}

func TestTorrentGalaxyListingAndCache(t *testing.T) {
	magnet := "magnet:?xt=urn:btih:" + hashA + "&dn=TGX%20Release"
	var detailCalls int
	detail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		detailCalls++
		_, _ = io.WriteString(w, `<html><body><a href="`+magnet+`">magnet</a></body></html>`)
	}))
	defer detail.Close()

	body := []byte(`<html><body>
		<div class="tgxtablerow">
			<div class="tgxtablecell"><a href="` + detail.URL + `/post-detail/1/tgx-release/">TGX Release 2160p</a></div>
		</div>
	</body></html>`)
	base, _ := url.Parse("https://torrentgalaxy.one/get-posts/user:MIRCrewRS/")
	document := mustParseHTML(t, body)
	items, err := parseTorrentGalaxyListing(context.Background(), detail.Client(), document, base, RequestOptions{})
	if err != nil {
		t.Fatalf("parseTorrentGalaxyListing: %v", err)
	}
	if len(items) != 1 || items[0].Title != "TGX Release 2160p" || items[0].InfoHash != hashA {
		t.Fatalf("items = %#v", items)
	}
	// The second pass must use the detail cache.
	if _, err := parseTorrentGalaxyListing(context.Background(), detail.Client(), document, base, RequestOptions{}); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if detailCalls != 1 {
		t.Errorf("detail calls = %d, want 1 (cache)", detailCalls)
	}
}

func TestCorsaroListing(t *testing.T) {
	magnet := "magnet:?xt=urn:btih:" + hashA + "&dn=Corsaro%20Release"
	detail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html><body><a href="`+magnet+`">magnet</a></body></html>`)
	}))
	defer detail.Close()
	directHash := "fedcba9876543210fedcba9876543210fedcba98"
	body := []byte(`<html><body>
		<a href="` + detail.URL + `/torrent/123/corsaro-release/">Corsaro Release 1080p</a>
		<a href="magnet:?xt=urn:btih:` + directHash + `&dn=Direct">magnet</a>
	</body></html>`)
	base, _ := url.Parse("https://corsaro.example/")
	items, err := parseCorsaroListing(context.Background(), detail.Client(), mustParseHTML(t, body), base, RequestOptions{})
	if err != nil {
		t.Fatalf("parseCorsaroListing: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	seen := map[string]bool{}
	for _, item := range items {
		seen[item.Title] = true
	}
	if !seen["Direct"] || !seen["Corsaro Release 1080p"] {
		t.Errorf("items = %#v", items)
	}
}

func TestDiscoverFeeds(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<html><head>
			<link rel="alternate" type="application/rss+xml" title="Main feed" href="/feed.xml">
		</head><body><a href="/rss/news">News</a></body></html>`)
	}))
	defer page.Close()
	feeds, err := DiscoverFeeds(context.Background(), page.URL, RequestOptions{}, 5*time.Second)
	if err != nil {
		t.Fatalf("DiscoverFeeds: %v", err)
	}
	if len(feeds) != 2 || feeds[0].URL != page.URL+"/feed.xml" || feeds[0].Title != "Main feed" {
		t.Errorf("feeds = %#v", feeds)
	}
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `<rss version="2.0"><channel></channel></rss>`)
	}))
	defer direct.Close()
	feeds, err = DiscoverFeeds(context.Background(), direct.URL, RequestOptions{}, 5*time.Second)
	if err != nil || len(feeds) != 1 || feeds[0].URL != direct.URL {
		t.Errorf("direct feed = %#v, %v", feeds, err)
	}
}

func TestSuggestKind(t *testing.T) {
	cases := map[string]string{
		"https://extto.org/browse/":                  "rss",
		"https://torrentgalaxy.one/get-posts/user:x": "rss",
		"https://corsaro.example/torrent/1":          "rss",
		"https://example.org/feed.xml":               "rss",
		"http://prowlarr:9696":                       "torznab",
		"http://indexer:8080/api":                    "torznab",
	}
	for raw, want := range cases {
		if got := SuggestKind(raw); got != want {
			t.Errorf("SuggestKind(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestFetchRSSHTMLExtTo(t *testing.T) {
	listing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<html><body><table><tr>
			<td><a href="/post-detail/1/example/">HTML Release</a></td>
			<td><a href="magnet:?xt=urn:btih:`+hashA+`">magnet</a></td>
		</tr></table></body></html>`)
	}))
	defer listing.Close()
	items, err := FetchRSS(context.Background(), mappedClient("extto.org", listing),
		"http://extto.org/browse/?filter=u=test", RequestOptions{})
	if err != nil {
		t.Fatalf("FetchRSS: %v", err)
	}
	if len(items) != 1 || items[0].Title != "HTML Release" {
		t.Fatalf("items = %#v", items)
	}
}

func TestExtractMagnet(t *testing.T) {
	body := []byte(`<a href="magnet:?xt=urn:btih:` + hashA + `&amp;dn=Name">x</a>`)
	if got := extractMagnet(body); !strings.Contains(got, hashA) {
		t.Errorf("extractMagnet = %q", got)
	}
	if got := extractMagnet([]byte(`<html>no magnet</html>`)); got != "" {
		t.Errorf("extractMagnet without magnet = %q", got)
	}
}
