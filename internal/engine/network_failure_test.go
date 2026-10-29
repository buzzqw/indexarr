package engine

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

func networkFailureConnector(t *testing.T, baseURL string) *DefinitionConnector {
	t.Helper()
	def, err := definition.Parse([]byte(`schema: 1
meta:
  id: network-failure
  name: Network failure
  type: public
  protocol: torrent
  urls:
    - ` + baseURL + `
capabilities:
  search: [q]
search:
  request:
    get: /search
  response:
    type: html
    rows: { selector: "tr" }
    fields:
      title: { selector: "a" }
`))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Transport:  NewTransport(TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	return connector
}

// "request failed" alone hid why thirteen TorrentGalaxy sources failed every
// run (the domain no longer resolves). The cause is named, the host is not.
func TestRequestErrorNamesTheNetworkFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := listener.Addr().String()
	_ = listener.Close()

	cases := []struct {
		name, baseURL, want string
	}{
		{"refused", "http://" + closed + "/", "connection refused"},
		{"dns", "http://indexarr-test.invalid/", "host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			connector := networkFailureConnector(t, tc.baseURL)
			_, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "test"})
			if err == nil {
				t.Fatal("Search succeeded against an unreachable site")
			}
			text := err.Error()
			if !strings.Contains(text, tc.want) && !strings.Contains(text, "name resolution") {
				t.Errorf("error = %q, want it to name %q", text, tc.want)
			}
			if strings.Contains(text, "indexarr-test.invalid") || strings.Contains(text, closed) {
				t.Errorf("error leaks the address: %q", text)
			}
		})
	}
}

// TorrentGalaxy lost torrentgalaxy.one on 2026-10-28 while the mirror
// torrentgalaxy.info kept the same pages: with only meta.urls[0] in use every
// run failed. A dead address moves the connector to the next one and it stays
// there.
func TestConnectorMovesToTheNextMirrorWhenTheDomainIsGone(t *testing.T) {
	var hits atomic.Int32
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		writeBody(t, w, []byte(`<html><body><table><tr><td><a href="/t/1">Mirror Release</a></td></tr></table></body></html>`))
	}))
	t.Cleanup(mirror.Close)

	def, err := definition.Parse([]byte(`schema: 1
meta:
  id: mirror-test
  name: Mirror test
  type: public
  protocol: torrent
  urls:
    - http://indexarr-dead.invalid/
    - ` + mirror.URL + `/
capabilities:
  search: [q]
search:
  request:
    get: /search
  response:
    type: html
    rows: { selector: "tr" }
    fields:
      title: { selector: "a" }
      details: { selector: "a", attr: href, type: url }
`))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Transport:  NewTransport(TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	for round := 0; round < 3; round++ {
		items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "release"})
		if err != nil {
			t.Fatalf("round %d: Search: %v", round, err)
		}
		if len(items) != 1 || items[0].Title != "Mirror Release" {
			t.Fatalf("round %d: items = %+v", round, items)
		}
		if !strings.HasPrefix(items[0].DetailsURL, mirror.URL) {
			t.Errorf("round %d: details = %q, want the mirror", round, items[0].DetailsURL)
		}
	}
	if connector.mirror.Load() != 1 {
		t.Errorf("mirror = %d, want 1", connector.mirror.Load())
	}
	if hits.Load() != 3 {
		t.Errorf("mirror hits = %d, want 3", hits.Load())
	}
}

// A connector that moved to a mirror goes back to the first address when the
// mirror dies too, and a Cloudflare "origin is down" page (521) counts as a
// dead address.
func TestConnectorGoesAroundTheMirrorsWhenTheOriginIsDown(t *testing.T) {
	var firstDown atomic.Bool
	firstDown.Store(true)
	page := []byte(`<html><body><table><tr><td><a href="/t/1">Release</a></td></tr></table></body></html>`)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if firstDown.Load() {
			w.WriteHeader(521)
			return
		}
		writeBody(t, w, page)
	}))
	t.Cleanup(first.Close)
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBody(t, w, page)
	}))

	def, err := definition.Parse([]byte(`schema: 1
meta:
  id: mirror-circle
  name: Mirror circle
  type: public
  protocol: torrent
  urls:
    - ` + first.URL + `/
    - ` + second.URL + `/
capabilities:
  search: [q]
search:
  request:
    get: /search
  response:
    type: html
    rows: { selector: "tr" }
    fields:
      title: { selector: "a" }
      details: { selector: "a", attr: href, type: url }
`))
	if err != nil {
		t.Fatalf("parse definition: %v", err)
	}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Transport:  NewTransport(TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	search := func(want string) {
		t.Helper()
		items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "release"})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(items) != 1 || !strings.HasPrefix(items[0].DetailsURL, want) {
			t.Fatalf("items = %+v, want details on %s", items, want)
		}
	}
	search(second.URL)
	if connector.mirror.Load() != 1 {
		t.Fatalf("mirror = %d, want 1 after a 521", connector.mirror.Load())
	}
	firstDown.Store(false)
	second.Close()
	search(first.URL)
	if connector.mirror.Load() != 0 {
		t.Errorf("mirror = %d, want 0 after the mirror died", connector.mirror.Load())
	}
}
