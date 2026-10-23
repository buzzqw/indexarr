package importers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchJackettCatalogIncludesUnconfiguredSites(t *testing.T) {
	const key = "jackett-catalog-key-SECRET"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/v2.0/indexers/all/results/torznab/api" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("t"); got != "indexers" {
			t.Errorf("t = %q", got)
		}
		if got := r.URL.Query().Get("configured"); got != "false" {
			t.Errorf("configured = %q, want false", got)
		}
		if got := r.URL.Query().Get("apikey"); got != key {
			t.Errorf("apikey was not sent")
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<indexers>
  <indexer id="alpha" configured="false">
    <title>Alpha</title>
    <description>Alpha is a public tracker</description>
    <link>https://alpha.example/</link>
    <language>it-IT</language>
    <type>public</type>
    <caps><categories><category id="2000" name="Movies"><subcat id="2040" name="Movies/HD"/></category><category id="5000" name="TV"/></categories></caps>
  </indexer>
  <indexer id="beta" configured="true">
    <title>Beta</title>
    <link>https://beta.example/</link>
    <type>private</type>
  </indexer>
</indexers>`)
	}))
	defer server.Close()

	sites, err := FetchJackettCatalog(context.Background(), server.URL, key, server.Client())
	if err != nil {
		t.Fatalf("FetchJackettCatalog: %v", err)
	}
	if len(sites) != 2 {
		t.Fatalf("sites = %d, want 2: %+v", len(sites), sites)
	}
	alpha := sites[0]
	if alpha.SourceID != "alpha" || alpha.Name != "Alpha" || alpha.Type != "public" || alpha.Language != "it-IT" {
		t.Errorf("alpha = %+v", alpha)
	}
	if alpha.Configured {
		t.Error("alpha configured = true, want false")
	}
	if got, want := fmt.Sprint(alpha.Categories), "[2000 2040 5000]"; got != want {
		t.Errorf("alpha categories = %s, want %s", got, want)
	}
	if !sites[1].Configured {
		t.Error("beta configured = false, want true")
	}
}

func TestFetchProwlarrCatalogReadsPublicMetadata(t *testing.T) {
	const key = "prowlarr-catalog-key-SECRET"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/indexer" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("X-Api-Key"); got != key {
			t.Errorf("X-Api-Key was not sent")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[
  {
    "id": 7,
    "name": "Example",
    "description": "Public example",
    "privacy": "public",
    "language": "it-IT",
    "priority": 25,
    "enable": true,
    "indexerUrls": ["https://example.test/"],
    "legacyUrls": ["https://legacy.example.test/"],
    "capabilities": {"categories": [{"id": 2000, "name": "Movies"}, {"id": 5000, "name": "TV"}]},
    "fields": [{"name": "apiKey", "value": "must-not-leak"}]
  }
]`)
	}))
	defer server.Close()

	sites, err := FetchProwlarrCatalog(context.Background(), server.URL, key, server.Client())
	if err != nil {
		t.Fatalf("FetchProwlarrCatalog: %v", err)
	}
	if len(sites) != 1 {
		t.Fatalf("sites = %+v", sites)
	}
	site := sites[0]
	if site.SourceID != "7" || site.Name != "Example" || site.Type != "public" || !site.Configured {
		t.Errorf("site = %+v", site)
	}
	if site.URL != "https://example.test/" || len(site.URLs) != 2 {
		t.Errorf("urls = %v", site.URLs)
	}
	if got, want := fmt.Sprint(site.Categories), "[2000 5000]"; got != want {
		t.Errorf("categories = %s, want %s", got, want)
	}
	if strings.Contains(fmt.Sprint(site), "must-not-leak") {
		t.Error("credentials leaked into the catalog metadata")
	}
}

func TestCatalogMetadataDoesNotLeakKeyOnFailure(t *testing.T) {
	const key = "catalog-secret-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid API key "+key, http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := FetchJackettCatalog(context.Background(), server.URL, key, server.Client())
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("error leaked the API key: %v", err)
	}
}
