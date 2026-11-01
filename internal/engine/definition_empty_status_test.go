package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/buzzqw/indexarr/internal/definition"
)

// emptyStatusConnector builds a definition whose search answers with the
// status the body carries. defEmpty adds the empty_status declaration, so the
// same site can be observed with and without it.
func emptyStatusConnector(t *testing.T, status int, defEmpty bool) *DefinitionConnector {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`<html><body><table>
			<tr><td><a href="/t/1">A row that must not be read</a></td></tr>
		</table></body></html>`))
	}))
	t.Cleanup(server.Close)

	emptyLine := ""
	if defEmpty {
		emptyLine = "  empty_status: [404]\n"
	}
	def, err := definition.Parse([]byte(`schema: 1
meta:
  id: empty-status
  name: Empty status
  type: public
  protocol: torrent
  urls:
    - ` + server.URL + `/
capabilities:
  search: [q]
search:
` + emptyLine + `  request:
    get: /search
    params: { q: "{{ query.keywords }}" }
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
	t.Cleanup(connector.Close)
	return connector
}

// TestSearchEmptyStatusIsNotAFailure is the "empty is not broken" rule: a site
// that renders "no results" with a 404 does not mark the source as failing and
// does not return the rows of the error page.
func TestSearchEmptyStatusIsNotAFailure(t *testing.T) {
	connector := emptyStatusConnector(t, http.StatusNotFound, true)
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "nothing"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("items = %+v, want none", items)
	}
}

// TestSearchUnknownStatusStillFails guards the other side: without the
// declaration the same answer stays an error, so a moved endpoint is visible
// instead of silently returning nothing.
func TestSearchUnknownStatusStillFails(t *testing.T) {
	connector := emptyStatusConnector(t, http.StatusNotFound, false)
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "nothing"}); err == nil {
		t.Fatal("a 404 without empty_status was accepted")
	}
}
