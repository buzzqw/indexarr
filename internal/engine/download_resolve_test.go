package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
)

func newConnectorFromDefinition(t *testing.T, def *definition.Definition, clock Clock) *DefinitionConnector {
	t.Helper()
	transport := NewTransport(TransportConfig{AllowPrivate: true, Clock: clock})
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Transport:  transport,
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	return connector
}

func TestDefinitionDownloadResolveOnSearch(t *testing.T) {
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	torrent := []byte("torrent-bytes")
	var detailRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			writeBody(t, w, fixture(t, "download_list.html"))
		case "/details/1":
			detailRequests.Add(1)
			writeBody(t, w, fixture(t, "download_details_magnet.html"))
		case "/details/2":
			detailRequests.Add(1)
			writeBody(t, w, fixture(t, "download_details_torrent.html"))
		case "/details/3":
			detailRequests.Add(1)
			http.NotFound(w, r)
		case "/files/2.torrent":
			w.Header().Set("Content-Type", "application/x-bittorrent")
			writeBody(t, w, torrent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "download_resolve.yaml", server.URL, nil, newDefinitionClock())
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	if got := detailRequests.Load(); got != 2 {
		t.Errorf("details requests = %d, want 2 (the direct magnet row must not be opened)", got)
	}

	first := items[0]
	if !strings.HasPrefix(strings.ToLower(first.Magnet), "magnet:?xt=urn:btih:"+hash) {
		t.Errorf("magnet = %q", first.Magnet)
	}
	if first.InfoHash != hash {
		t.Errorf("infohash = %q, want %q", first.InfoHash, hash)
	}
	if first.DownloadURL != "" {
		t.Errorf("download URL = %q, want empty for a magnet", first.DownloadURL)
	}

	second := items[1]
	if second.Magnet != "" {
		t.Errorf("magnet = %q, want empty", second.Magnet)
	}
	if second.DownloadURL != server.URL+"/files/2.torrent" {
		t.Errorf("download URL = %q", second.DownloadURL)
	}

	third := items[2]
	if third.InfoHash != "cccccccccccccccccccccccccccccccccccccccc" {
		t.Errorf("direct magnet infohash = %q", third.InfoHash)
	}

	payload, err := connector.Download(context.Background(), first)
	if err != nil || payload.Magnet != first.Magnet {
		t.Errorf("magnet Download = %+v, %v", payload, err)
	}

	payload, err = connector.Download(context.Background(), second)
	if err != nil {
		t.Fatalf("torrent Download: %v", err)
	}
	if string(payload.Body) != string(torrent) {
		t.Errorf("body = %q", payload.Body)
	}
}

func TestDefinitionDownloadResolveOnGrab(t *testing.T) {
	torrent := []byte("grab-torrent")
	var detailRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			writeBody(t, w, fixture(t, "download_list.html"))
		case "/details/1":
			detailRequests.Add(1)
			writeBody(t, w, fixture(t, "download_details_magnet.html"))
		case "/details/2":
			detailRequests.Add(1)
			writeBody(t, w, fixture(t, "download_details_torrent.html"))
		case "/files/2.torrent":
			writeBody(t, w, torrent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	def := loadTestDefinition(t, "download_resolve.yaml", server.URL)
	def.Download.ResolveOn = "grab"
	connector := newConnectorFromDefinition(t, def, newDefinitionClock())

	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	if got := detailRequests.Load(); got != 0 {
		t.Errorf("search in grab mode opened %d details pages, want 0", got)
	}
	if items[0].Magnet != "" || items[1].DownloadURL != "" {
		t.Errorf("grab search resolved details early: %+v", items)
	}

	payload, err := connector.Download(context.Background(), items[0])
	if err != nil {
		t.Fatalf("grab magnet Download: %v", err)
	}
	if !strings.HasPrefix(strings.ToLower(payload.Magnet), "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Errorf("grab magnet = %q", payload.Magnet)
	}

	payload, err = connector.Download(context.Background(), items[1])
	if err != nil {
		t.Fatalf("grab torrent Download: %v", err)
	}
	if string(payload.Body) != string(torrent) {
		t.Errorf("grab body = %q", payload.Body)
	}

	// The details resolution is cached: asking again does not hit the site.
	before := detailRequests.Load()
	if _, err := connector.Download(context.Background(), items[0]); err != nil {
		t.Fatalf("second grab Download: %v", err)
	}
	if after := detailRequests.Load(); after != before {
		t.Errorf("details requests = %d after cached Download, want %d", after, before)
	}
}

func TestDefinitionDownloadGrabIgnoresItemDownloadURL(t *testing.T) {
	// A grab-time definition declares that the link comes from the details
	// page. An item can still carry a DownloadURL from an import or a stale
	// reference (TorrentLeech imported from Prowlarr pointed at
	// http://192.168.1.10:9696/14/download and the SSRF guard refused it).
	// The declared path must win, on the indexer's own host.
	torrent := []byte("grab-torrent")
	var details, foreign atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/details/2":
			details.Add(1)
			writeBody(t, w, fixture(t, "download_details_torrent.html"))
		case "/files/2.torrent":
			writeBody(t, w, torrent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		foreign.Add(1)
		http.NotFound(w, r)
	}))
	defer other.Close()

	def := loadTestDefinition(t, "download_resolve.yaml", server.URL)
	def.Download.ResolveOn = "grab"
	connector := newConnectorFromDefinition(t, def, newDefinitionClock())

	item := Item{
		Title:       "Imported Release",
		DetailsURL:  server.URL + "/details/2",
		DownloadURL: other.URL + "/download",
	}
	payload, err := connector.Download(context.Background(), item)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(payload.Body) != string(torrent) {
		t.Errorf("body = %q, want %q", payload.Body, torrent)
	}
	if got := foreign.Load(); got != 0 {
		t.Errorf("foreign DownloadURL was requested %d times, want 0", got)
	}
	if got := details.Load(); got != 1 {
		t.Errorf("details requests = %d, want 1", got)
	}
}

func TestDefinitionDownloadConcurrencyLimit(t *testing.T) {
	var inFlight, maxInFlight, detailRequests atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			writeBody(t, w, fixture(t, "download_many.html"))
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/details/many/") {
			http.NotFound(w, r)
			return
		}
		requestNumber := detailRequests.Add(1)
		current := inFlight.Add(1)
		for {
			max := maxInFlight.Load()
			if current <= max || maxInFlight.CompareAndSwap(max, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		inFlight.Add(-1)

		hash := fmt.Sprintf("%040x", requestNumber)
		fmt.Fprintf(w, `<html><body><a href="magnet:?xt=urn:btih:%s">M</a></body></html>`, hash)
	}))
	defer server.Close()

	def := loadTestDefinition(t, "download_resolve.yaml", server.URL)
	def.Download.ResolveOn = "search"
	def.Download.MaxConcurrent = 2
	connector := newConnectorFromDefinition(t, def, newDefinitionClock())

	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 20 {
		t.Fatalf("items = %d, want 20", len(items))
	}
	if got := detailRequests.Load(); got != 20 {
		t.Errorf("details requests = %d, want 20", got)
	}
	if got := maxInFlight.Load(); got > 2 {
		t.Errorf("max details requests in flight = %d, want at most 2", got)
	}
	for i, item := range items {
		if item.Magnet == "" || item.InfoHash == "" {
			t.Errorf("item %d was not resolved: %+v", i, item)
		}
	}
}

func TestDefinitionDownloadMagnetRedirect(t *testing.T) {
	const hash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			writeBody(t, w, []byte(`<html><body><table class="results"><tbody>
<tr><td><a class="title" href="/details/redir">Redirected</a></td></tr>
</tbody></table></body></html>`))
		case "/details/redir":
			w.Header().Set("Location", "magnet:?xt=urn:btih:"+hash+"&tr=https%3A%2F%2Ftracker.example%2Fannounce")
			w.WriteHeader(http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "download_resolve.yaml", server.URL, nil, newDefinitionClock())
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if !strings.HasPrefix(items[0].Magnet, "magnet:?xt=urn:btih:"+hash) {
		t.Errorf("magnet = %q", items[0].Magnet)
	}
	if items[0].InfoHash != hash {
		t.Errorf("infohash = %q", items[0].InfoHash)
	}
}

func TestDefinitionDownloadResolveOnlyAfterLimit(t *testing.T) {
	var detailRequests atomic.Int32

	var listing strings.Builder
	listing.WriteString(`<html><body><table class="results"><tbody>`)
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&listing, `<tr><td><a class="title" href="/details/limit/%d">Row %d</a></td></tr>`, i, i)
	}
	listing.WriteString(`</tbody></table></body></html>`)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			writeBody(t, w, []byte(listing.String()))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/details/limit/") {
			detailRequests.Add(1)
			writeBody(t, w, fixture(t, "download_details_magnet.html"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	def := loadTestDefinition(t, "download_resolve.yaml", server.URL)
	def.Download.ResolveOn = "search"
	connector := newConnectorFromDefinition(t, def, newDefinitionClock())

	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 10 {
		t.Fatalf("items = %d, want 10", len(items))
	}
	if got := detailRequests.Load(); got != 10 {
		t.Errorf("details requests = %d, want 10: resolution must run after limit and dedup", got)
	}
}

func TestDefinitionDownloadDefaultResolveOnIsGrab(t *testing.T) {
	var detailRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			writeBody(t, w, fixture(t, "download_list.html"))
		case "/details/1":
			detailRequests.Add(1)
			writeBody(t, w, fixture(t, "download_details_magnet.html"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	def := loadTestDefinition(t, "download_resolve.yaml", server.URL)
	def.Download.ResolveOn = "" // absent in the definition: grab is the default
	connector := newConnectorFromDefinition(t, def, newDefinitionClock())

	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("items = 0, want the listing rows")
	}
	if got := detailRequests.Load(); got != 0 {
		t.Errorf("details requests during search = %d, want 0 without resolve_on: search", got)
	}
	if items[0].Magnet != "" || items[0].DownloadURL != "" {
		t.Errorf("search resolved a details page early: %+v", items[0])
	}

	payload, err := connector.Download(context.Background(), items[0])
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !strings.HasPrefix(payload.Magnet, "magnet:?xt=urn:btih:") {
		t.Errorf("grab magnet = %q", payload.Magnet)
	}
	if got := detailRequests.Load(); got != 1 {
		t.Errorf("details requests after Download = %d, want 1", got)
	}
}

func TestDefinitionDownloadCacheAndTTL(t *testing.T) {
	const listing = `<html><body><table class="results"><tbody>
<tr><td><a class="title" href="/details/cache">Cached One</a></td></tr>
<tr><td><a class="title" href="/details/cache">Cached Two</a></td></tr>
</tbody></table></body></html>`

	var detailRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			writeBody(t, w, []byte(listing))
			return
		}
		if r.URL.Path == "/details/cache" {
			detailRequests.Add(1)
			writeBody(t, w, fixture(t, "download_details_magnet.html"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	clock := newDefinitionClock()
	connector := newDefinitionConnector(t, "download_resolve.yaml", server.URL, nil, clock)

	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch}); err != nil {
		t.Fatalf("first Search: %v", err)
	}
	if got := detailRequests.Load(); got != 1 {
		t.Errorf("details requests = %d, want 1 (duplicate rows share one request)", got)
	}

	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch}); err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if got := detailRequests.Load(); got != 1 {
		t.Errorf("details requests = %d after cached Search, want 1", got)
	}

	clock.Advance(defaultDownloadCacheTTL + time.Second)
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch}); err != nil {
		t.Fatalf("third Search: %v", err)
	}
	if got := detailRequests.Load(); got != 2 {
		t.Errorf("details requests = %d after TTL, want 2", got)
	}
}

func TestDefinitionDownloadErrorsAreRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			writeBody(t, w, []byte(`<html><body><table class="results"><tbody>
<tr><td><a class="title" href="/details/fail?passkey=super-secret">Failing</a></td></tr>
</tbody></table></body></html>`))
			return
		}
		http.Error(w, "upstream exploded", http.StatusInternalServerError)
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "download_resolve.yaml", server.URL, nil, newDefinitionClock())
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch})
	if err != nil {
		t.Fatalf("Search should not abort on a failing details page: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].Magnet != "" {
		t.Errorf("magnet = %q, want empty", items[0].Magnet)
	}

	_, err = connector.Download(context.Background(), items[0])
	if err == nil {
		t.Fatal("Download succeeded, want an error")
	}
	message := err.Error()
	for _, secret := range []string{"super-secret", "passkey", "tracker.example", server.URL} {
		if strings.Contains(message, secret) {
			t.Errorf("error leaks %q: %q", secret, message)
		}
	}
	if !strings.Contains(message, "HTTP 500") {
		t.Errorf("error %q does not report the HTTP status", message)
	}
}
