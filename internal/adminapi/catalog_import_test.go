package adminapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
)

// TestCatalogImportEndpoint covers the "definition fix without a release" path
// over the API: the address is validated, the documents are stored with the
// remote origin, and a bad address is a client error rather than a crash.
func TestCatalogImportEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog/index.json":
			_, _ = w.Write([]byte(`[{"id":"api-site","name":"API Site","state":"verificata"}]`))
		case "/catalog/definitions/api-site.yaml":
			_, _ = w.Write([]byte(`schema: 1
meta:
  id: api-site
  name: API Site
  type: public
  protocol: torrent
  urls:
    - https://api-site.example/
capabilities:
  search: [q]
search:
  request:
    get: /
  response:
    type: html
    rows: { selector: "tr" }
    fields:
      title: { selector: "a" }
`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	e := newEnv(t, true, true)
	res := e.do(http.MethodPost, "/api/v2/catalog/import", jsonBody(map[string]string{
		"url": server.URL + "/catalog",
	}))
	requireStatus(t, res, http.StatusOK)
	result := decodeJSON[map[string]any](t, res.body)
	if result["written"] != float64(1) || result["entries"] != float64(1) {
		t.Fatalf("result = %v", result)
	}
	def, err := e.store.GetDefinition(t.Context(), "api-site")
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if def.Origin != catalog.OriginRemote {
		t.Fatalf("origin = %q", def.Origin)
	}

	empty := e.do(http.MethodPost, "/api/v2/catalog/import", jsonBody(map[string]string{"url": ""}))
	requireStatus(t, empty, http.StatusBadRequest)

	bad := e.do(http.MethodPost, "/api/v2/catalog/import", jsonBody(map[string]string{"url": "ftp://example/catalog"}))
	requireStatus(t, bad, http.StatusBadGateway)
}
