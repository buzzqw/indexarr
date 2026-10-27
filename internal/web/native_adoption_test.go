package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/indexersvc"
)

func adoptionCatalog() []catalog.IndexEntry {
	return []catalog.IndexEntry{
		{
			ID: "alpha-def", Name: "AlphaNative", Source: "native",
			Type: "public", Protocol: "torrent", State: "verificata",
			Hosts: []string{"alpha.example"},
		},
		{
			ID: "bravo-family", Name: "BravoFamily", Source: "jackett",
			Type: "private", Protocol: "torrent", State: "da-provvi",
			Family: "nexusphp", Hosts: []string{"bravo.example"},
			Required: []string{"username", "password"},
		},
		{
			ID: "charlie-queue", Name: "CharlieQueue", Source: "jackett",
			Type: "private", Protocol: "torrent", State: "da-provvi",
			Hosts:    []string{"charlie.example"},
			Required: []string{"username", "password"},
		},
		{
			ID: "delta-queue", Name: "DeltaQueue", Source: "prowlarr",
			Type: "semi-private", Protocol: "torrent", State: "da-provvi",
			Hosts:    []string{"delta.example"},
			Required: []string{"url"},
		},
	}
}

func TestCatalogCoverageAndRouteFilter(t *testing.T) {
	env := newNativeEnv(t)
	env.server.catalogIndex = func() ([]catalog.IndexEntry, error) { return adoptionCatalog(), nil }

	// The user catalogue only lists what already works. The full inventory is
	// still in the index and is served by the maintenance view (?queue=1).
	body := get(t, env.handler, "/indexers/new?all=1").Body.String()
	if !strings.Contains(body, "AlphaNative") {
		t.Error("the ready source is missing from the catalogue")
	}
	for _, hidden := range []string{"BravoFamily", "CharlieQueue", "DeltaQueue"} {
		if strings.Contains(body, hidden) {
			t.Errorf("unfinished source %q is shown in the user catalogue", hidden)
		}
	}

	body = get(t, env.handler, "/indexers/new?all=1&queue=1").Body.String()
	if !strings.Contains(body, "Coverage: 1 ready sources, 1 with known software.") {
		t.Errorf("coverage line missing from the queue view: %s", body)
	}
	for _, want := range []string{"AlphaNative", "BravoFamily", "CharlieQueue", "DeltaQueue"} {
		if !strings.Contains(body, want) {
			t.Errorf("queue view is missing %q", want)
		}
	}

	body = get(t, env.handler, "/indexers/new?all=1&queue=1&route=pending").Body.String()
	for _, hidden := range []string{"AlphaNative", "BravoFamily"} {
		if strings.Contains(body, hidden) {
			t.Errorf("route=pending returned %q", hidden)
		}
	}
	for _, want := range []string{"CharlieQueue", "DeltaQueue"} {
		if !strings.Contains(body, want) {
			t.Errorf("route=pending is missing %q", want)
		}
	}

	body = get(t, env.handler, "/indexers/new?all=1&queue=1&route=family").Body.String()
	if !strings.Contains(body, "BravoFamily") || strings.Contains(body, "CharlieQueue") {
		t.Error("the known-software filter is wrong")
	}

	body = get(t, env.handler, "/indexers/new?all=1&queue=1&route=native").Body.String()
	if !strings.Contains(body, "AlphaNative") || strings.Contains(body, "BravoFamily") {
		t.Error("the ready filter is wrong")
	}
}

func TestManagerImportPageIsGone(t *testing.T) {
	env := newTestEnv(t)
	for _, path := range []string{
		"/import", "/import/prowlarr", "/import/jackett", "/import/apply", "/import/gextto",
	} {
		recorder := get(t, env.handler, path)
		if recorder.Code != 404 {
			t.Errorf("GET %s = %d, want 404", path, recorder.Code)
		}
	}

	indexers := get(t, env.handler, "/indexers").Body.String()
	if strings.Contains(indexers, `href="/import"`) {
		t.Error("the indexers page still links to the removed manager import")
	}
	setup := get(t, env.handler, "/setup?step=2").Body.String()
	if strings.Contains(setup, `href="/import"`) {
		t.Error("the setup wizard still links to the removed manager import")
	}
	if !strings.Contains(setup, "/indexers/new") {
		t.Error("the setup wizard does not open the native catalog")
	}
}

func TestManagerEndpointErrorIsTranslated(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/indexers/new?lang=it", nil)
	msg := env.server.indexerFriendlyError(req, indexersvc.ErrManagerEndpoint)
	if !strings.Contains(msg, "sostituisce Jackett e Prowlarr") {
		t.Fatalf("translated error = %q", msg)
	}
}
