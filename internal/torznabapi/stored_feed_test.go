package torznabapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/store"
)

// TestStoredSourceInTVSearchWithoutQuery proves that the *arr RSS request
// (tvsearch with no q) reads the cache of a site the user chose to memorise.
// The site is search-only in the definition (meta.latest: false); the store
// flag is what puts it in the latest set.
func TestStoredSourceInTVSearchWithoutQuery(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	entry, ok := catalog.Get("limetorrents")
	if !ok {
		t.Fatal("limetorrents definition is missing")
	}
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID: entry.ID, SchemaVersion: 1, Family: entry.Family, State: "verificata",
		Origin: "local", Author: "test", Body: string(entry.Body),
		Hash: entry.Hash,
	}); err != nil {
		t.Fatal(err)
	}
	indexerID, err := st.CreateIndexer(ctx, store.Indexer{
		DefinitionID: entry.ID, Protocol: "torrent", Slug: "stored-lime",
		Name: "Stored Lime", Enabled: true, ConfigJSON: `{"store_latest":"true"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	releaseID := insertRelease(t, st, "Stored Show S01E01 1080p", "5040",
		"9999999999999999999999999999999999999999", "", nil)
	if err := st.RecordReleaseSource(ctx, store.ReleaseSource{
		ReleaseID: releaseID, IndexerID: indexerID,
		FirstSeenAt: time.Now().Add(-time.Hour), LastSeenAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := indexer.New(indexer.Config{
		Store:  st,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	api.SetLatestIndexers(manager.LatestIndexerIDs)

	recorder := do(t, api, "/api?t=tvsearch&apikey=secret&cat=5000")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "Stored Show S01E01 1080p") {
		t.Errorf("tvsearch without q did not include the stored source:\n%s", body)
	}
}

// TestCachedStoredReleaseBeforeLiveResponse proves that a release already in
// the cache is served even when the live site does not answer within the wait
// ceiling. The live call is still allowed to finish in the background.
func TestCachedStoredReleaseBeforeLiveResponse(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	settings, err := st.LoadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.TorznabLiveSearch = true
	settings.TorznabLiveWait = 1
	settings.TorznabLiveTimeout = 5
	if err := st.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	insertRelease(t, st, "Cached Show S01E01 1080p", "5040",
		"8888888888888888888888888888888888888888", "", nil)
	api.SetLiveSearch(func(ctx context.Context, _ indexer.LiveQuery) (indexer.LiveResult, error) {
		select {
		case <-time.After(3 * time.Second):
			return indexer.LiveResult{}, nil
		case <-ctx.Done():
			return indexer.LiveResult{}, ctx.Err()
		}
	})

	started := time.Now()
	recorder := do(t, api, "/api?t=search&apikey=secret&q=Cached")
	elapsed := time.Since(started)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); !strings.Contains(body, "Cached Show S01E01 1080p") {
		t.Errorf("the cached release is missing from the response:\n%s", body)
	}
	if elapsed >= 2500*time.Millisecond {
		t.Errorf("response waited %v for the live site; the cache must answer at the wait ceiling", elapsed)
	}
}
