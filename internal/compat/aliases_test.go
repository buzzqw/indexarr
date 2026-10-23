package compat

import (
	"net/http"
	"strings"
	"testing"
)

func TestAggregateAliasesAreEquivalent(t *testing.T) {
	e := newEnv(t)
	query := "t=search&apikey=" + primaryKey + "&q=" + searchQuery + "&limit=100"
	paths := []string{
		"/api?" + query,
		"/torznab/api?" + query,
		"/newznab/api?" + query,
		"/api/v2.0/indexers/all/results/torznab/api?" + query,
	}
	var canonical string
	for i, path := range paths {
		recorder := e.get(path)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", path, recorder.Code, recorder.Body.String())
		}
		document := parseRSS(t, recorder)
		if document.Channel.Response.Total != 4 {
			t.Errorf("%s total = %d, want 4", path, document.Channel.Response.Total)
		}
		if i == 0 {
			canonical = recorder.Body.String()
			continue
		}
		if recorder.Body.String() != canonical {
			t.Errorf("%s differs from /api:\n%s\n--- want ---\n%s",
				path, recorder.Body.String(), canonical)
		}
	}

	capsPaths := []string{
		"/api?t=caps",
		"/torznab/api?t=caps",
		"/newznab/api?t=caps",
		"/api/v2.0/indexers/all/results/torznab/api?t=caps",
	}
	var caps string
	for i, path := range capsPaths {
		recorder := e.get(path)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s = %d", path, recorder.Code)
		}
		if i == 0 {
			caps = recorder.Body.String()
			continue
		}
		if recorder.Body.String() != caps {
			t.Errorf("%s caps differ from /api", path)
		}
	}
}

func TestPerIndexerSelection(t *testing.T) {
	e := newEnv(t)
	alphaPaths := []string{
		"/torznab/alpha/api?t=search&apikey=" + primaryKey + "&q=" + searchQuery,
		"/api/v2.0/indexers/alpha/results/torznab/api?t=search&apikey=" + primaryKey + "&q=" + searchQuery,
	}
	var alphaBody string
	for i, path := range alphaPaths {
		recorder := e.get(path)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", path, recorder.Code, recorder.Body.String())
		}
		document := parseRSS(t, recorder)
		if document.Channel.Response.Total != 3 {
			t.Errorf("%s total = %d, want 3", path, document.Channel.Response.Total)
		}
		body := recorder.Body.String()
		for _, want := range []string{"Compat.Magnet.Show", "Compat.Torrent.Movie", "Multi"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", path, want)
			}
		}
		if strings.Contains(body, "Compat.NZB.Album") {
			t.Errorf("%s returned the beta-only release", path)
		}
		if i == 0 {
			alphaBody = body
			continue
		}
		if body != alphaBody {
			t.Errorf("%s differs from /torznab/alpha/api", path)
		}
	}

	beta := e.get("/torznab/beta/api?t=search&apikey=" + primaryKey + "&q=" + searchQuery)
	document := parseRSS(t, beta)
	if document.Channel.Response.Total != 1 || len(document.Channel.Items) != 1 {
		t.Fatalf("beta total = %d items = %d, want 1/1",
			document.Channel.Response.Total, len(document.Channel.Items))
	}
	if !strings.Contains(beta.Body.String(), "Compat.NZB.Album") {
		t.Error("beta route did not return the usenet release")
	}
	if strings.Contains(beta.Body.String(), "Compat.Magnet.Show") {
		t.Error("beta route returned an alpha release")
	}

	// A per-indexer caps response is the same public caps document.
	alphaCaps := e.get("/torznab/alpha/api?t=caps")
	aggregateCaps := e.get("/api?t=caps")
	if alphaCaps.Code != http.StatusOK || alphaCaps.Body.String() != aggregateCaps.Body.String() {
		t.Errorf("per-indexer caps differ from aggregate caps (%d)", alphaCaps.Code)
	}

	for _, path := range []string{
		"/torznab/missing-indexer/api?t=search&apikey=" + primaryKey,
		"/api/v2.0/indexers/missing-indexer/results/torznab/api?t=search&apikey=" + primaryKey,
	} {
		recorder := e.get(path)
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, recorder.Code)
		}
		if document := parseError(t, recorder); document.Code != 201 {
			t.Errorf("%s torznab code = %d, want 201", path, document.Code)
		}
	}
}

func TestAggregateFilters(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		query string
		total int64
		want  string
		not   string
	}{
		{"tag=hd", 3, "Compat.Magnet.Show", "Compat.NZB.Album"},
		{"tag=tv", 1, "Compat.NZB.Album", "Compat.Magnet.Show"},
		{"status=enabled", 4, "Multi", ""},
		{"status=disabled", 0, "", "Multi"},
		{"indexer_type=usenet", 1, "Compat.NZB.Album", "Compat.Magnet.Show"},
	}
	for _, test := range tests {
		path := "/api/v2.0/indexers/all/results/torznab/api?t=search&apikey=" +
			primaryKey + "&q=" + searchQuery + "&" + test.query
		document := parseRSS(t, e.get(path))
		if document.Channel.Response.Total != test.total {
			t.Errorf("%s total = %d, want %d", test.query,
				document.Channel.Response.Total, test.total)
		}
		body := e.get(path).Body.String()
		if test.want != "" && !strings.Contains(body, test.want) {
			t.Errorf("%s missing %q", test.query, test.want)
		}
		if test.not != "" && strings.Contains(body, test.not) {
			t.Errorf("%s unexpectedly contains %q", test.query, test.not)
		}
	}
}
