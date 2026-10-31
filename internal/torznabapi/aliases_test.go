package torznabapi

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func seedDefinition(t *testing.T, st *store.Store, id, family, language string) {
	t.Helper()
	body := "schema: 1\nconnector: torznab\n"
	if language != "" {
		body += "meta:\n  language: " + language + "\n"
	}
	err := st.UpsertDefinition(context.Background(), store.Definition{
		ID:            id,
		SchemaVersion: 1,
		Family:        family,
		State:         "verificata",
		Origin:        "local",
		Author:        "test",
		Body:          body,
		Hash:          strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatalf("UpsertDefinition(%s): %v", id, err)
	}
}

func seedIndexer(t *testing.T, st *store.Store, definitionID, slug, name, tags, protocol string, enabled bool) store.Indexer {
	t.Helper()
	if protocol == "" {
		protocol = "torrent"
	}
	id, err := st.CreateIndexer(context.Background(), store.Indexer{
		DefinitionID: definitionID,
		Protocol:     protocol,
		Slug:         slug,
		Name:         name,
		Enabled:      enabled,
		Tags:         tags,
	})
	if err != nil {
		t.Fatalf("CreateIndexer(%s): %v", slug, err)
	}
	indexer, err := st.GetIndexer(context.Background(), id)
	if err != nil {
		t.Fatalf("GetIndexer(%s): %v", slug, err)
	}
	return indexer
}

func insertScopedRelease(t *testing.T, st *store.Store, index int, title string, indexer store.Indexer, withTorrent bool) int64 {
	t.Helper()
	hash := fmt.Sprintf("%040x", index+1)
	release := store.Release{
		DedupKey:    hash,
		InfoHash:    hash,
		Title:       title,
		SearchTitle: store.NormalizeSearch(title),
		Size:        4096,
		Seeders:     5,
		Leechers:    1,
		Category:    "5040",
		PublishedAt: time.Now().Add(-time.Hour),
		Magnet:      "magnet:?xt=urn:btih:" + hash + "&dn=" + strings.ReplaceAll(title, " ", "+"),
		FirstSeenAt: time.Now().Add(-time.Hour),
		LastSeenAt:  time.Now(),
	}
	if withTorrent {
		release.Torrent = []byte("d4:infod4:name" + strconv.Itoa(index) + "e")
	}
	if _, err := st.UpsertRelease(context.Background(), &release); err != nil {
		t.Fatalf("UpsertRelease(%s): %v", title, err)
	}
	if err := st.RecordReleaseSource(context.Background(), store.ReleaseSource{
		ReleaseID: release.ID, IndexerID: indexer.ID,
		FirstSeenAt: release.FirstSeenAt, LastSeenAt: release.LastSeenAt,
		Seeders: release.Seeders, Leechers: release.Leechers,
	}); err != nil {
		t.Fatalf("RecordReleaseSource(%s): %v", title, err)
	}
	return release.ID
}

// seedScopedIndexer creates one native indexer.
func seedScopedIndexer(t *testing.T, st *store.Store, definitionID, slug, name string) store.Indexer {
	t.Helper()
	return seedIndexer(t, st, definitionID, slug, name, "", "torrent", true)
}

func wantError(t *testing.T, recorder *httptest.ResponseRecorder, status, code int) {
	t.Helper()
	if recorder.Code != status {
		t.Errorf("status = %d, want %d", recorder.Code, status)
	}
	if body := recorder.Body.String(); !strings.Contains(body, fmt.Sprintf(`<error code="%d"`, code)) {
		t.Errorf("body missing torznab code %d: %s", code, body)
	}
}

func TestJackettAliases(t *testing.T) {
	api, st := newAPI(t)
	seedDefinition(t, st, "alias-def", "torznab", "")
	source := seedScopedIndexer(t, st, "alias-def", "test-indexer", "Test Source")
	id := insertScopedRelease(t, st, 0, "Alias Show 1080p", source, true)

	searchPaths := []string{
		"/api?t=search&apikey=secret&q=Alias",
		"/torznab/api?t=search&apikey=secret&q=Alias",
		"/torznab/test-indexer/api?t=search&apikey=secret&q=Alias",
		"/api/v2.0/indexers/test-indexer/results/torznab/api?t=search&apikey=secret&q=Alias",
		"/api/v2.0/indexers/all/results/torznab/api?t=search&apikey=secret&q=Alias",
	}
	var canonical string
	for i, target := range searchPaths {
		recorder := do(t, api, target)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", target, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		if i == 0 {
			canonical = body
		} else if body != canonical {
			t.Errorf("%s does not match /api:\n%s\n--- want ---\n%s", target, body, canonical)
		}
		if !strings.Contains(body, "Alias Show 1080p") {
			t.Errorf("%s missing release", target)
		}
		// The item points at the local proxy, never at the tracker.
		if !strings.Contains(body, `<link>http://example.com/dl/`+itoa(id)+`.torrent?apikey=secret</link>`) {
			t.Errorf("%s does not proxy the download link:\n%s", target, body)
		}
		if strings.Contains(body, "<link>magnet:") {
			t.Errorf("%s exposes a magnet as the item link", target)
		}
	}

	capsPaths := []string{
		"/api?t=caps",
		"/torznab/api?t=caps",
		"/torznab/test-indexer/api?t=caps",
		"/api/v2.0/indexers/test-indexer/results/torznab/api?t=caps",
		"/api/v2.0/indexers/all/results/torznab/api?t=caps",
	}
	var caps string
	for i, target := range capsPaths {
		recorder := do(t, api, target)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", target, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		if i == 0 {
			caps = body
		} else if body != caps {
			t.Errorf("%s caps differ from /api", target)
		}
		if !strings.Contains(body, `<caps>`) || !strings.Contains(body, `<limits max="100" default="50"/>`) {
			t.Errorf("%s has invalid caps: %s", target, body)
		}
		var decoded struct {
			XMLName xml.Name
		}
		if err := xml.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			t.Errorf("%s caps is not valid XML: %v", target, err)
		}
		if decoded.XMLName.Local != "caps" {
			t.Errorf("%s caps root = %q", target, decoded.XMLName.Local)
		}
	}
}

func TestPerIndexerRouting(t *testing.T) {
	api, st := newAPI(t)
	seedDefinition(t, st, "routing-def", "torznab", "")
	alpha := seedScopedIndexer(t, st, "routing-def", "alpha", "Alpha")
	beta := seedScopedIndexer(t, st, "routing-def", "beta", "Beta")
	insertScopedRelease(t, st, 1, "Alpha Show 1080p", alpha, true)
	insertScopedRelease(t, st, 2, "Beta Show 1080p", beta, true)

	onlyAlpha := do(t, api, "/torznab/alpha/api?t=search&apikey=secret")
	if onlyAlpha.Code != http.StatusOK {
		t.Fatalf("alpha status = %d", onlyAlpha.Code)
	}
	alphaBody := onlyAlpha.Body.String()
	if !strings.Contains(alphaBody, "Alpha Show 1080p") || strings.Contains(alphaBody, "Beta Show 1080p") {
		t.Errorf("alpha route returned the wrong releases:\n%s", alphaBody)
	}
	if !strings.Contains(alphaBody, `total="1"`) {
		t.Errorf("alpha total:\n%s", alphaBody)
	}

	jackettAlpha := do(t, api, "/api/v2.0/indexers/alpha/results/torznab/api?t=search&apikey=secret")
	if jackettAlpha.Code != http.StatusOK || jackettAlpha.Body.String() != alphaBody {
		t.Errorf("jackett alpha route differs: %d\n%s", jackettAlpha.Code, jackettAlpha.Body.String())
	}

	// Legacy source name lookup, case-insensitive.
	byName := do(t, api, "/torznab/Beta/api?t=search&apikey=secret")
	if byName.Code != http.StatusOK || !strings.Contains(byName.Body.String(), "Beta Show 1080p") ||
		strings.Contains(byName.Body.String(), "Alpha Show 1080p") {
		t.Errorf("legacy name route:\n%s", byName.Body.String())
	}

	aggregate := do(t, api, "/api/v2.0/indexers/all/results/torznab/api?t=search&apikey=secret")
	allBody := aggregate.Body.String()
	if aggregate.Code != http.StatusOK || !strings.Contains(allBody, `total="2"`) ||
		!strings.Contains(allBody, "Alpha Show 1080p") || !strings.Contains(allBody, "Beta Show 1080p") {
		t.Errorf("aggregate route:\n%s", allBody)
	}

	// An indexer without legacy_source_id still matches releases by source name.
	seedDefinition(t, st, "name-def", "torznab", "")
	named := seedIndexer(t, st, "name-def", "name-only", "Name Only", "", "torrent", true)
	insertScopedRelease(t, st, 3, "Name Only Show", named, true)
	byNameOnly := do(t, api, "/torznab/name-only/api?t=search&apikey=secret")
	if byNameOnly.Code != http.StatusOK || !strings.Contains(byNameOnly.Body.String(), "Name Only Show") {
		t.Errorf("name-only indexer route: %d\n%s", byNameOnly.Code, byNameOnly.Body.String())
	}

	unknown := do(t, api, "/torznab/missing-indexer/api?t=search&apikey=secret")
	if unknown.Code != http.StatusNotFound || !strings.Contains(unknown.Body.String(), `<error code="201"`) {
		t.Errorf("unknown indexer = %d: %s", unknown.Code, unknown.Body.String())
	}
}

func TestTorznabErrorCodes(t *testing.T) {
	api, st := newAPI(t)
	seedDefinition(t, st, "error-def", "torznab", "")
	source := seedScopedIndexer(t, st, "error-def", "error-indexer", "Error Source")
	id := insertScopedRelease(t, st, 10, "Error Show", source, true)

	badKey := do(t, api, "/api?t=search&apikey=wrong")
	wantError(t, badKey, http.StatusUnauthorized, 100)

	noKey := do(t, api, "/api?t=search")
	wantError(t, noKey, http.StatusUnauthorized, 100)

	noFunction := do(t, api, "/api?t=unsupported&apikey=secret")
	wantError(t, noFunction, http.StatusBadRequest, 300)

	missingID := do(t, api, "/api?t=get&apikey=secret")
	wantError(t, missingID, http.StatusBadRequest, 200)

	missingRelease := do(t, api, "/api?t=get&id=does-not-exist&apikey=secret")
	wantError(t, missingRelease, http.StatusNotFound, 201)

	badNumeric := do(t, api, "/api?t=get&id=-4&apikey=secret")
	wantError(t, badNumeric, http.StatusBadRequest, 201)

	byDedup := do(t, api, "/torznab/error-indexer/api?t=get&id="+
		fmt.Sprintf("%040x", 11)+"&apikey=secret")
	if byDedup.Code != http.StatusOK || byDedup.Body.String() != string([]byte("d4:infod4:name10e")) {
		t.Errorf("t=get by dedup key = %d: %q", byDedup.Code, byDedup.Body.String())
	}
	if byID := do(t, api, "/torznab/error-indexer/api?t=get&id="+itoa(id)+"&apikey=secret"); byID.Code != http.StatusOK {
		t.Errorf("t=get by numeric id on alias = %d", byID.Code)
	}

	settings, _ := st.LoadSettings(context.Background())
	settings.APIRateLimit = 1
	if err := st.SaveSettings(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	limited, err := st.AddAPIKey(context.Background(), "rate-limited")
	if err != nil {
		t.Fatal(err)
	}
	first := do(t, api, "/api?t=search&apikey="+limited.Key)
	if first.Code != http.StatusOK {
		t.Fatalf("first limited request = %d", first.Code)
	}
	second := do(t, api, "/api?t=search&apikey="+limited.Key)
	wantError(t, second, http.StatusTooManyRequests, 400)
	if second.Header().Get("Retry-After") == "" {
		t.Error("rate-limited response is missing Retry-After")
	}
}

func TestAggregateFilters(t *testing.T) {
	api, st := newAPI(t)
	seedDefinition(t, st, "filter-unit", "unit3d", "en")
	seedDefinition(t, st, "filter-torznab", "torznab", "it")
	movies := seedIndexer(t, st, "filter-unit", "movies-indexer", "Movies", "movies,hd", "torrent", true)
	tv := seedIndexer(t, st, "filter-torznab", "tv-indexer", "TV", "tv", "usenet", false)
	insertScopedRelease(t, st, 20, "Movie Release", movies, true)
	insertScopedRelease(t, st, 21, "TV Release", tv, true)

	tests := []struct {
		query string
		want  string
		total string
	}{
		{"tag=movies", "Movie Release", `total="1"`},
		{"tag=hd", "Movie Release", `total="1"`},
		{"tag=tv", "TV Release", `total="1"`},
		{"indexer_type=unit3d", "Movie Release", `total="1"`},
		{"indexer_type=torznab", "TV Release", `total="1"`},
		{"indexer_type=usenet", "TV Release", `total="1"`},
		{"status=enabled", "Movie Release", `total="1"`},
		{"status=disabled", "TV Release", `total="1"`},
		{"lang=en", "Movie Release", `total="1"`},
		{"lang=it", "TV Release", `total="1"`},
		{"tag=movies&status=enabled", "Movie Release", `total="1"`},
	}
	for _, test := range tests {
		recorder := do(t, api, "/api?t=search&apikey=secret&"+test.query)
		body := recorder.Body.String()
		if recorder.Code != http.StatusOK {
			t.Errorf("%s = %d: %s", test.query, recorder.Code, body)
			continue
		}
		if !strings.Contains(body, test.want) {
			t.Errorf("%s missing %q:\n%s", test.query, test.want, body)
		}
		if !strings.Contains(body, test.total) {
			t.Errorf("%s missing %s:\n%s", test.query, test.total, body)
		}
	}

	emptyQueries := []string{
		"tag=does-not-exist",
		"indexer_type=does-not-exist",
		"status=does-not-exist",
		"lang=zz",
		"tag=movies&status=disabled",
	}
	for _, query := range emptyQueries {
		recorder := do(t, api, "/api?t=search&apikey=secret&"+query)
		if recorder.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", query, recorder.Code)
			continue
		}
		body := recorder.Body.String()
		if strings.Contains(body, "<item>") || !strings.Contains(body, `total="0"`) {
			t.Errorf("%s should be empty:\n%s", query, body)
		}
	}

	// The aggregate Jackett alias honors the same filters.
	alias := do(t, api, "/api/v2.0/indexers/all/results/torznab/api?t=search&apikey=secret&tag=movies")
	if alias.Code != http.StatusOK || !strings.Contains(alias.Body.String(), "Movie Release") ||
		strings.Contains(alias.Body.String(), "TV Release") {
		t.Errorf("filtered aggregate alias:\n%s", alias.Body.String())
	}
}

func TestAggregateHealthFilters(t *testing.T) {
	api, st := newAPI(t)
	seedDefinition(t, st, "health-def", "torznab", "en")
	healthy := seedIndexer(t, st, "health-def", "healthy-indexer", "Healthy", "", "torrent", true)
	failing := seedIndexer(t, st, "health-def", "failing-indexer", "Failing", "", "torrent", true)
	unknown := seedIndexer(t, st, "health-def", "unknown-indexer", "Unknown", "", "torrent", true)
	insertScopedRelease(t, st, 30, "Healthy Release", healthy, true)
	insertScopedRelease(t, st, 31, "Failing Release", failing, true)
	insertScopedRelease(t, st, 32, "Unknown Release", unknown, true)
	for _, row := range []struct {
		indexer store.Indexer
		status  string
	}{
		{healthy, "ok"},
		{failing, "error"},
	} {
		if _, err := st.RecordIndexerHistory(context.Background(), store.IndexerHistory{
			IndexerID: row.indexer.ID,
			Kind:      "health",
			Query:     "health",
			Status:    row.status,
			StartedAt: time.Now(),
		}); err != nil {
			t.Fatalf("record health %s: %v", row.indexer.Slug, err)
		}
	}

	tests := []struct {
		query string
		want  string
		total string
	}{
		{"status=healthy", "Healthy Release", `total="1"`},
		{"status=failing", "Failing Release", `total="1"`},
		{"status=unknown", "Unknown Release", `total="1"`},
		{"test=passed", "Healthy Release", `total="1"`},
		{"test=failed", "Failing Release", `total="1"`},
		{"status=healthy&test=passed", "Healthy Release", `total="1"`},
		{"status=failing&test=failed", "Failing Release", `total="1"`},
	}
	for _, test := range tests {
		recorder := do(t, api, "/api?t=search&apikey=secret&"+test.query)
		body := recorder.Body.String()
		if recorder.Code != http.StatusOK {
			t.Errorf("%s = %d: %s", test.query, recorder.Code, body)
			continue
		}
		if !strings.Contains(body, test.want) {
			t.Errorf("%s missing %q:\n%s", test.query, test.want, body)
		}
		if !strings.Contains(body, test.total) {
			t.Errorf("%s missing %s:\n%s", test.query, test.total, body)
		}
	}

	emptyQueries := []string{
		"status=healthy&test=failed",
		"status=failing&test=passed",
		"test=does-not-exist",
		"status=does-not-exist",
	}
	for _, query := range emptyQueries {
		recorder := do(t, api, "/api?t=search&apikey=secret&"+query)
		if recorder.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", query, recorder.Code)
			continue
		}
		body := recorder.Body.String()
		if strings.Contains(body, "<item>") || !strings.Contains(body, `total="0"`) {
			t.Errorf("%s should be empty:\n%s", query, body)
		}
	}
}

func TestAggregateFiltersPagination(t *testing.T) {
	api, st := newAPI(t)
	seedDefinition(t, st, "page-def", "unit3d", "en")
	first := seedIndexer(t, st, "page-def", "first", "First", "common", "torrent", true)
	second := seedIndexer(t, st, "page-def", "second", "Second", "common", "torrent", true)
	insertScopedRelease(t, st, 40, "First A", first, true)
	insertScopedRelease(t, st, 41, "First B", first, true)
	insertScopedRelease(t, st, 42, "Second A", second, true)
	insertScopedRelease(t, st, 43, "Second B", second, true)

	for offset := 0; offset < 4; offset++ {
		recorder := do(t, api, "/api?t=search&apikey=secret&tag=common&limit=1&offset="+strconv.Itoa(offset))
		if recorder.Code != http.StatusOK {
			t.Fatalf("offset %d = %d: %s", offset, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		if !strings.Contains(body, `total="4"`) {
			t.Errorf("offset %d: total should stay 4:\n%s", offset, body)
		}
		if strings.Count(body, "<item>") != 1 {
			t.Errorf("offset %d: want one item per page:\n%s", offset, body)
		}
	}
}
