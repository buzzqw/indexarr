package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

// TestDownloadBeforeLogsInWithoutSearch covers a grab coming from a stored
// release on a fresh process: no search ran first, so the browser session is
// not authenticated. The details page is then the login form, which has no
// thank link; the grab used to fail with "before action rendered an empty URL"
// even though a login was available. Download must log in by itself.
func TestDownloadBeforeLogsInWithoutSearch(t *testing.T) {
	var (
		logins  atomic.Int32
		thanks  atomic.Int32
		thanked atomic.Bool
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			logins.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "synthetic-session", Path: "/"})
			_, _ = w.Write([]byte(`<html><body><a href="./ucp.php?mode=logout">Logout</a></body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<html><body><form method="post" action="/login">
<input type="hidden" name="form_token" value="synthetic-token"></form></body></html>`))
	})
	mux.HandleFunc("/topic", func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("sid")
		if err != nil || cookie.Value != "synthetic-session" {
			_, _ = w.Write([]byte(`<html><body><form method="post" action="/login">
<input type="hidden" name="form_token" value="synthetic-token"></form></body></html>`))
			return
		}
		if !thanked.Load() {
			_, _ = w.Write([]byte(`<html><body>
<a href="./thanks?p=1&amp;thanks=1&amp;to_id=2">Grazie</a>
</body></html>`))
			return
		}
		_, _ = w.Write([]byte(`<html><body>
<a href="magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&amp;dn=Show.S01E02.ITA.1080p.mkv">S01E02</a>
</body></html>`))
	})
	mux.HandleFunc("/thanks", func(w http.ResponseWriter, r *http.Request) {
		thanks.Add(1)
		thanked.Store(true)
		_, _ = w.Write([]byte(`<html><body>Grazie inviato</body></html>`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	def, err := definition.Parse([]byte(`schema: 1
meta:
  id: before-login-test
  name: Before login test
  type: private
  protocol: torrent
  urls: ["` + server.URL + `/"]
capabilities:
  search: [q]
auth:
  strategy: form
  steps:
    - get: /login
      extract:
        form_token: { selector: 'input[name="form_token"]', attr: value }
    - post: /login
      form:
        form_token: "{{ form_token }}"
  success: { selector: 'a[href*="mode=logout"]' }
search:
  request:
    get: /search
    params: { q: "{{ query.keywords }}" }
  response:
    type: html
    rows: { selector: "table.results tbody tr" }
    fields:
      title: { selector: "a.title" }
      details: { selector: "a.title", attr: href, type: url }
    required: [title, details]
download:
  resolve_on: grab
  from: details
  before:
    extract:
      thanks: { selector: 'a[href*="thanks="]', attr: href }
    action:
      get: "{{ thanks }}"
  extract:
    - magnet: { selector: 'a[href^="magnet:"]', attr: href }
      all: true
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

	// No Search and no Test: this is exactly the state after a restart.
	payload, err := connector.Download(context.Background(), Item{
		Title:      "Show S01E02",
		DetailsURL: server.URL + "/topic",
		Attributes: map[string]string{"season": "1", "episode": "2"},
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !strings.Contains(payload.Magnet, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Errorf("magnet = %q", payload.Magnet)
	}
	if logins.Load() != 1 {
		t.Errorf("logins = %d, want 1", logins.Load())
	}
	if thanks.Load() != 1 {
		t.Errorf("thanks = %d, want 1", thanks.Load())
	}
}
