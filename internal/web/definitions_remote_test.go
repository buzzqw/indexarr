package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/store"
)

func remoteDefinitionYAML(id, name string) string {
	return fmt.Sprintf(`schema: 1
meta:
  id: %s
  name: %s
  type: public
  protocol: torrent
  urls:
    - https://%s.example/
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
`, id, name, id)
}

// remoteCatalogServer serves index.json plus definitions/<id>.yaml, the layout
// the import action expects.
func remoteCatalogServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/catalog/index.json":
			_, _ = w.Write([]byte(`[{"id":"remote-site","name":"Remote Site","state":"verificata"}]`))
		case "/catalog/definitions/remote-site.yaml":
			_, _ = w.Write([]byte(remoteDefinitionYAML("remote-site", "Remote Site")))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestDefinitionsPageOffersTheRemoteCatalog keeps the action reachable from the
// place it belongs: the definitions page says what it does and posts to the
// handler.
func TestDefinitionsPageOffersTheRemoteCatalog(t *testing.T) {
	env := newTestEnv(t)
	page := get(t, env.handler, "/settings?section=advanced")
	if page.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", page.Code, page.Body.String())
	}
	body := page.Body.String()
	for _, want := range []string{
		`action="/definitions/import"`,
		"Remote catalog",
		"Import from the catalog",
		"without a new release",
		"Reads the catalog from this address",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("definitions page missing %q", want)
		}
	}
}

// TestImportActionWritesAndLabelsTheDefinitions covers the whole path: one
// POST stores the documents with the remote origin, the page labels them as
// such, and the operator's own definitions stay untouched.
func TestImportActionWritesAndLabelsTheDefinitions(t *testing.T) {
	env := newTestEnv(t)
	if err := env.store.UpsertDefinition(context.Background(), store.Definition{
		ID: "mine", SchemaVersion: 1, State: "verificata", Origin: catalog.OriginLocal,
		Author: "me", Body: remoteDefinitionYAML("mine", "Mine"), Hash: "mine-hash",
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	server := remoteCatalogServer(t)

	token, cookie := csrfFrom(t, env.handler, "/settings?section=advanced")
	recorder := postForm(t, env.handler, "/definitions/import", url.Values{
		"csrf": {token},
		"url":  {server.URL + "/catalog"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	if !strings.Contains(location, "Catalog+imported") {
		t.Errorf("location = %q, want the import summary", location)
	}

	stored, err := env.store.GetDefinition(context.Background(), "remote-site")
	if err != nil {
		t.Fatalf("GetDefinition: %v", err)
	}
	if stored.Origin != catalog.OriginRemote {
		t.Fatalf("origin = %q, want %q", stored.Origin, catalog.OriginRemote)
	}

	page := get(t, env.handler, "/settings?section=advanced")
	if !strings.Contains(page.Body.String(), "remote catalog") {
		t.Error("the imported definition is not labelled as a remote catalog one")
	}
}

// TestImportActionRejectsAnEmptyAddress keeps the form honest: the field is
// required, and a failed read leaves the page usable with the reason.
func TestImportActionRejectsAnEmptyAddress(t *testing.T) {
	env := newTestEnv(t)
	token, cookie := csrfFrom(t, env.handler, "/settings?section=advanced")
	recorder := postForm(t, env.handler, "/definitions/import", url.Values{
		"csrf": {token},
		"url":  {""},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Location"); !strings.Contains(got, "could+not+be+read") {
		t.Errorf("location = %q, want the failure message", got)
	}
}
