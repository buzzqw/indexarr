package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const healthDetailsDefinition = `schema: 1
meta:
  id: health-details
  name: Health Details
  type: public
  protocol: torrent
  urls: ["http://REPLACE/"]
capabilities:
  search: [q]
search:
  request:
    get: /search
  response:
    type: html
    rows: "table.results tr"
    fields:
      title: { selector: "td.name a", filters: [trim] }
      details: { selector: "td.name a", attr: href, type: url }
      size: { selector: "td.size", type: size }
    required: [title, details, size]
download:
  resolve_on: search
  from: details
  max_concurrent: 1
  extract:
    - magnet: { selector: 'a[href^="magnet:"]', attr: href }
health:
  query: ""
  min_rows: 1
`

// TestHealthCheckDoesNotOpenResultDetails keeps the periodic probe cheap: it
// only counts rows, so it must not pay one details request per result. On
// TorrentGalaxy that walk was 50-150 requests and opened the 429 breaker that
// then paused the scheduled runs too.
func TestHealthCheckDoesNotOpenResultDetails(t *testing.T) {
	var detailRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search":
			_, _ = w.Write([]byte(`<html><body><table class="results">
<tr><td class="name"><a href="/details/1">One</a></td><td class="size">1 GB</td></tr>
<tr><td class="name"><a href="/details/2">Two</a></td><td class="size">2 GB</td></tr>
</table></body></html>`))
		case strings.HasPrefix(r.URL.Path, "/details/"):
			detailRequests.Add(1)
			_, _ = w.Write([]byte(`<html><body><a href="magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa">M</a></body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	st := newTestStore(t)
	v := newTestVault(t, 1)
	manager := newTestManager(t, st, v)
	body := strings.ReplaceAll(healthDetailsDefinition, "http://REPLACE", server.URL)
	seedDefinition(t, st, "health-details", "", body)
	id := createIndexer(t, st, "health-details", "health-details", "Health Details", "torrent",
		`{"url":"`+server.URL+`"}`)

	result, err := manager.Health(context.Background(), id)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if result.Items != 2 {
		t.Errorf("items = %d, want 2", result.Items)
	}
	if got := detailRequests.Load(); got != 0 {
		t.Errorf("details requests = %d, want 0", got)
	}
}
