package web

import (
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
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

	body := get(t, env.handler, "/indexers/new?all=1").Body.String()
	if !strings.Contains(body, "Coverage: 1 ready sources, 1 with known software, 2 to write.") {
		t.Errorf("coverage line missing: %s", body)
	}

	body = get(t, env.handler, "/indexers/new?all=1&route=pending").Body.String()
	if strings.Contains(body, "AlphaNative") || strings.Contains(body, "BravoFamily") {
		t.Error("the pending filter returned a source that already has a native route")
	}
	for _, want := range []string{"CharlieQueue", "DeltaQueue"} {
		if !strings.Contains(body, want) {
			t.Errorf("pending filter missing %q", want)
		}
	}

	body = get(t, env.handler, "/indexers/new?all=1&route=family").Body.String()
	if !strings.Contains(body, "BravoFamily") || strings.Contains(body, "CharlieQueue") {
		t.Error("the known-software filter is wrong")
	}

	body = get(t, env.handler, "/indexers/new?all=1&route=native").Body.String()
	if !strings.Contains(body, "AlphaNative") || strings.Contains(body, "BravoFamily") {
		t.Error("the ready filter is wrong")
	}
}
