package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

// TestDownloadSendsReleasePageAsReferer covers the DLE game sites: the
// .torrent link on the release page answers with an HTML page unless the
// request carries the release page as Referer, like a browser click.
func TestDownloadSendsReleasePageAsReferer(t *testing.T) {
	var server *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/42-game.html", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><a class="dwn" href="/index.php?do=download&amp;id=7">Download</a></body></html>`))
	})
	mux.HandleFunc("/index.php", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != server.URL+"/42-game.html" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<html>no referer</html>`))
			return
		}
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write([]byte("d8:announce0:e"))
	})
	server = httptest.NewServer(mux)
	defer server.Close()

	def, err := definition.Parse([]byte(`schema: 1
meta:
  id: referer-test
  name: Referer test
  type: public
  protocol: torrent
  urls: ["` + server.URL + `/"]
capabilities:
  search: [q]
search:
  request:
    get: /search
  response:
    type: html
    rows: { selector: "div.row" }
    fields:
      title: { selector: "a" }
      details: { selector: "a", attr: href, type: url }
    required: [title, details]
download:
  resolve_on: grab
  extract:
    - torrent: { selector: "a.dwn", attr: href, type: url }
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Transport:  NewTransport(TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	payload, err := connector.Download(context.Background(), Item{Title: "Game", DetailsURL: server.URL + "/42-game.html"})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(payload.Body) != "d8:announce0:e" {
		t.Fatalf("body = %q, want the torrent", payload.Body)
	}
}

func TestDownloadRefererStaysOnSameHost(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Referer")
		_, _ = w.Write([]byte("d8:announce0:e"))
	}))
	defer server.Close()
	def, err := definition.Parse([]byte(`schema: 1
meta:
  id: referer-host-test
  name: Referer host test
  type: public
  protocol: torrent
  urls: ["` + server.URL + `/"]
capabilities:
  search: [q]
search:
  request:
    get: /search
  response:
    type: html
    rows: { selector: "div.row" }
    fields:
      title: { selector: "a" }
    required: [title]
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Transport:  NewTransport(TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	if _, err := connector.Download(context.Background(), Item{
		Title:       "Game",
		DetailsURL:  "https://other.example/details/1",
		DownloadURL: server.URL + "/file.torrent",
	}); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got != "" {
		t.Errorf("Referer = %q, want none for another host", got)
	}
}
