package torznabapi

import (
	"context"
	"encoding/xml"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

// provenanceIndexer creates one enabled indexer for the provenance tests.
func provenanceIndexer(t *testing.T, st *store.Store, suffix, name string) int64 {
	t.Helper()
	ctx := context.Background()
	definitionID := "prov-def-" + suffix
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID: definitionID, SchemaVersion: 1, State: "verificata", Origin: "local",
		Body: "schema: 1\n", Hash: "hash-" + suffix, UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}
	id, err := st.CreateIndexer(ctx, store.Indexer{
		DefinitionID: definitionID,
		Protocol:     "torrent",
		Slug:         "prov-indexer-" + suffix,
		Name:         name,
		Enabled:      true,
		ConfigJSON:   `{}`,
	})
	if err != nil {
		t.Fatalf("CreateIndexer: %v", err)
	}
	return id
}

func assertWellFormedXML(t *testing.T, body string) {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(body))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("response is not well-formed XML: %v\n%s", err, body)
		}
	}
}

func TestItemWithoutMultiIndexerProvenance(t *testing.T) {
	api, st := newAPI(t)
	hash := "6666666666666666666666666666666666666666"
	id := insertRelease(t, st, "Single Source Show 1080p", "5040", hash,
		"magnet:?xt=urn:btih:"+hash, []byte("d4:infod4:name4:testee"))
	indexer := provenanceIndexer(t, st, "single", "Only Indexer")
	if err := st.RecordReleaseSource(context.Background(), store.ReleaseSource{
		ReleaseID: id, IndexerID: indexer,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(), Seeders: 11, Leechers: 3,
	}); err != nil {
		t.Fatalf("RecordReleaseSource: %v", err)
	}

	body := do(t, api, "/api?t=search&apikey=secret&q=Single").Body.String()
	assertWellFormedXML(t, body)
	if !strings.Contains(body, `<torznab:attr name="seeders" value="11"/>`) {
		t.Errorf("seeders missing:\n%s", body)
	}
	if !strings.Contains(body, `<torznab:attr name="leechers" value="3"/>`) {
		t.Errorf("leechers missing:\n%s", body)
	}
	// A release seen by a single indexer has no provenance attributes, which
	// keeps the output identical to the pre-provenance version.
	if strings.Contains(body, `name="indexers"`) {
		t.Errorf("single-indexer item has an indexers attribute:\n%s", body)
	}
	if strings.Contains(body, `name="provenance"`) {
		t.Errorf("single-indexer item has a provenance attribute:\n%s", body)
	}
}

func TestItemMultiIndexerProvenance(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	hash := "7777777777777777777777777777777777777777"
	id := insertRelease(t, st, "Shared Show 1080p", "5040", hash,
		"magnet:?xt=urn:btih:"+hash, []byte("d4:infod4:name4:testee"))
	first := provenanceIndexer(t, st, "alpha", "Tracker & Co")
	second := provenanceIndexer(t, st, "beta", "Second Tracker")

	now := time.Now()
	if err := st.RecordReleaseSource(ctx, store.ReleaseSource{
		ReleaseID: id, IndexerID: first, FirstSeenAt: now, LastSeenAt: now,
		Seeders: 12, Leechers: 4, DownloadRef: "https://tracker.invalid/dl?passkey=secret",
	}); err != nil {
		t.Fatalf("RecordReleaseSource first: %v", err)
	}
	if err := st.RecordReleaseSource(ctx, store.ReleaseSource{
		ReleaseID: id, IndexerID: second, FirstSeenAt: now, LastSeenAt: now,
		Seeders: 9, Leechers: 1,
	}); err != nil {
		t.Fatalf("RecordReleaseSource second: %v", err)
	}
	// Force the cached release row below the provenance maximum: the item must
	// use the provenance seeders, not the stale parent row.
	release, err := st.GetRelease(ctx, id)
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	release.Seeders = 5
	release.Leechers = 1
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatalf("UpsertRelease downgrade: %v", err)
	}

	body := do(t, api, "/api?t=search&apikey=secret&q=Shared").Body.String()
	assertWellFormedXML(t, body)
	for _, want := range []string{
		`<torznab:attr name="seeders" value="12"/>`,
		`<torznab:attr name="leechers" value="4"/>`,
		`<torznab:attr name="peers" value="16"/>`,
		`<torznab:attr name="indexers" value="2"/>`,
		`<torznab:attr name="provenance" value="Second Tracker|Tracker &amp; Co"/>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q\n%s", want, body)
		}
	}
	if strings.Contains(body, "Tracker & Co") {
		t.Errorf("indexer name is not XML-escaped:\n%s", body)
	}
	// Provenance must never leak the internal download reference or tracker URL.
	for _, forbidden := range []string{"download_ref", "tracker.invalid", "passkey"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("item leaks %q:\n%s", forbidden, body)
		}
	}
}

func TestItemWithoutMagnetLinksToDownloadRef(t *testing.T) {
	api, st := newAPI(t)
	ctx := context.Background()
	// No infohash, no magnet, no stored payload: only the sealed reference
	// that the resolver opens at grab time. This is what a live search leaves
	// behind when the listing has no magnet.
	release := store.Release{
		DedupKey: "details-ref", Title: "Details Only Show S02E05",
		SearchTitle: store.NormalizeSearch("Details Only Show S02E05"),
		Size:        900, Seeders: 3, Leechers: 1, Category: "5040",
		PublishedAt: time.Now(), FirstSeenAt: time.Now(), LastSeenAt: time.Now(),
	}
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	indexer := provenanceIndexer(t, st, "ref", "Ref Indexer")
	if err := st.RecordReleaseSource(ctx, store.ReleaseSource{
		ReleaseID: release.ID, IndexerID: indexer,
		FirstSeenAt: time.Now(), LastSeenAt: time.Now(), DownloadRef: "sealed-ref",
	}); err != nil {
		t.Fatalf("RecordReleaseSource: %v", err)
	}
	body := do(t, api, "/api?t=search&apikey=secret&q=Details").Body.String()
	if !strings.Contains(body, "/dl/"+strconv.FormatInt(release.ID, 10)+".torrent?apikey=secret") {
		t.Errorf("download reference is not exposed through /dl:\n%s", body)
	}
}
