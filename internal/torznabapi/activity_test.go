package torznabapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

func seedActivityIndexer(t *testing.T, st *store.Store, slug string) int64 {
	t.Helper()
	ctx := context.Background()
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID: slug + "-def", SchemaVersion: 1, State: "verificata", Origin: "local",
		Body: "schema: 1\nconnector: torznab\n", Hash: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	id, err := st.CreateIndexer(ctx, store.Indexer{
		DefinitionID: slug + "-def", Slug: slug, Name: "Activity " + slug,
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	return id
}

func TestTryProducesTheSameXMLAsAPI(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	indexerID := seedActivityIndexer(t, st, "try-indexer")
	releaseID := insertRelease(t, st, "Try Show S01E01 1080p", "5040",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", nil)
	if err := st.RecordReleaseSource(ctx, store.ReleaseSource{
		ReleaseID: releaseID, IndexerID: indexerID,
		FirstSeenAt: time.Now().Add(-time.Hour), LastSeenAt: time.Now(),
	}); err != nil {
		t.Fatalf("RecordReleaseSource: %v", err)
	}

	target := "/torznab/try-indexer/api?t=tvsearch&apikey=secret&q=Try+Show&season=1&ep=1&limit=10"
	recorder := httptest.NewRecorder()
	api.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("api status = %d body = %s", recorder.Code, recorder.Body.String())
	}
	result, err := api.Try(ctx, "http://example.com", TryRequest{
		IndexerSlug: "try-indexer", Mode: "tvsearch", Query: "Try Show",
		Season: 1, Episode: 1, Limit: 10, APIKey: "secret",
	})
	if err != nil {
		t.Fatalf("Try: %v", err)
	}
	if result.Status != http.StatusOK {
		t.Fatalf("Try status = %d body = %s", result.Status, result.XML)
	}
	if string(result.XML) != recorder.Body.String() {
		t.Fatalf("Try XML differs from /api\n--- api ---\n%s\n--- try ---\n%s",
			recorder.Body.String(), result.XML)
	}
	if len(result.Releases) != 1 || result.Releases[0].ID != releaseID {
		t.Fatalf("Try releases = %+v", result.Releases)
	}
	// Windows' monotonic clock can report a zero duration for a very fast
	// in-memory request; the field is still populated, so only a negative
	// value is a bug.
	if result.Duration < 0 {
		t.Errorf("Try duration = %s", result.Duration)
	}

	// The same request is visible in Activity → Searches.
	searches, err := st.ListAppSearches(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListAppSearches: %v", err)
	}
	if len(searches) != 1 {
		t.Fatalf("app searches = %d, want 1", len(searches))
	}
	got := searches[0]
	if got.Mode != "tvsearch" || got.Query != "Try Show" ||
		got.IndexerIDs[0] != indexerID || got.IndexerNames[0] != "Activity try-indexer" {
		t.Fatalf("app search = %+v", got)
	}
	if got.TotalResults != 1 || got.Results["Activity try-indexer"] != 1 {
		t.Fatalf("app search results = %+v total=%d", got.Results, got.TotalResults)
	}
}

func TestAppSearchRecordsErrorsAndAppName(t *testing.T) {
	api, st := newAPI(t)
	request := httptest.NewRequest(http.MethodGet,
		"/api?t=search&apikey=secret&q=missing", nil)
	request.Header.Set("User-Agent", "Sonarr/4.0.0")
	recorder := httptest.NewRecorder()
	api.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	searches, err := st.ListAppSearches(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("ListAppSearches: %v", err)
	}
	if len(searches) != 1 || searches[0].App != "sonarr" {
		t.Fatalf("searches = %+v", searches)
	}
	if searches[0].Error != "" {
		t.Errorf("unexpected error = %q", searches[0].Error)
	}

	// A search for an unknown indexer is recorded as an error without
	// changing the protocol response.
	request = httptest.NewRequest(http.MethodGet,
		"/torznab/does-not-exist/api?t=search&apikey=secret&q=x", nil)
	recorder = httptest.NewRecorder()
	api.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
	searches, err = st.ListAppSearches(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("ListAppSearches: %v", err)
	}
	if len(searches) != 2 || searches[0].Error == "" {
		t.Fatalf("error search = %+v", searches)
	}
}

func TestTryAggregateWithoutIndexer(t *testing.T) {
	api, st := newAPI(t)
	insertRelease(t, st, "Aggregate Movie 2026", "2040",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "", nil)
	result, err := api.Try(context.Background(), "http://example.com", TryRequest{
		Mode: "movie", Query: "Aggregate Movie", APIKey: "secret",
	})
	if err != nil {
		t.Fatalf("Try: %v", err)
	}
	if result.Status != http.StatusOK {
		t.Fatalf("status = %d body = %s", result.Status, result.XML)
	}
	if result.Total != 1 || !strings.Contains(string(result.XML), "Aggregate Movie 2026") {
		t.Fatalf("total = %d xml = %s", result.Total, result.XML)
	}
}
