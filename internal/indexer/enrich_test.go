package indexer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/store"
)

func enrichDefinition(id, baseURL string, before bool) string {
	body := `schema: 1
meta:
  id: ` + id + `
  name: ` + id + `
  type: public
  protocol: torrent
  urls:
    - ` + baseURL + `/
capabilities:
  search: [q]
search:
  request:
    get: /search
  response:
    type: html
    rows: { selector: "tr" }
    fields:
      title: { selector: "a" }
      details: { selector: "a", attr: href, type: url }
download:
  resolve_on: grab
  from: details
`
	if before {
		body += `  before:
    extract:
      thanks: { selector: 'a.thanks', attr: href }
    action:
      get: "{{ thanks }}"
`
	}
	body += `  extract:
    - magnet: { selector: 'a[href^="magnet:"]', attr: href }
`
	return body
}

func seedHashlessSource(t *testing.T, st *store.Store, ref string, indexerID int64, title string) {
	t.Helper()
	ctx := context.Background()
	release := &store.Release{DedupKey: "key-" + title, Title: title, Size: 1 << 30, Protocol: "torrent"}
	if _, err := st.UpsertRelease(ctx, release); err != nil {
		t.Fatalf("UpsertRelease: %v", err)
	}
	if err := st.RecordReleaseSource(ctx, store.ReleaseSource{ReleaseID: release.ID, IndexerID: indexerID, DownloadRef: ref}); err != nil {
		t.Fatalf("RecordReleaseSource: %v", err)
	}
}

// The infohash backfill opened MirCrew topics in the background: download.before
// thanked about ten topics per run on the user's account, for releases no
// client had asked for. Smartfeed topics, sealed for the native MirCrew
// indexer, failed every pass with the feed's own connector. Only a plain
// details page is opened.
func TestEnrichNeverThanksATopicOrOpensAnotherIndexersReference(t *testing.T) {
	var engageHits, plainHits atomic.Int32
	engage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		engageHits.Add(1)
		_, _ = w.Write([]byte(`<html><body><a class="thanks" href="/thanks">Grazie</a></body></html>`))
	}))
	t.Cleanup(engage.Close)
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plainHits.Add(1)
		_, _ = w.Write([]byte(`<html><body><a href="magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Plain">m</a></body></html>`))
	}))
	t.Cleanup(plain.Close)

	st := newTestStore(t)
	v := newTestVault(t, 7)
	seedDefinition(t, st, "enrich-engage", "", enrichDefinition("enrich-engage", engage.URL, true))
	seedDefinition(t, st, "enrich-plain", "", enrichDefinition("enrich-plain", plain.URL, false))
	engageID := createIndexer(t, st, "enrich-engage", "enrich-engage", "Engage", "torrent", "{}")
	plainID := createIndexer(t, st, "enrich-plain", "enrich-plain", "Plain", "torrent", "{}")
	feedID := createIndexer(t, st, "enrich-plain", "enrich-feed", "Feed", "torrent", "{}")
	m := newTestManager(t, st, v)

	seal := func(indexerID int64, details string) string {
		ref, err := SealDownloadRef(v, indexerID, engine.Item{Title: "x", DetailsURL: details})
		if err != nil {
			t.Fatalf("SealDownloadRef: %v", err)
		}
		return ref
	}
	seedHashlessSource(t, st, seal(engageID, engage.URL+"/topic/1"), engageID, "Engage Topic")
	// A feed row whose reference belongs to the engaging indexer.
	seedHashlessSource(t, st, seal(engageID, engage.URL+"/topic/2"), feedID, "Feed Topic")
	seedHashlessSource(t, st, seal(plainID, plain.URL+"/topic/3"), plainID, "Plain Topic")

	report, err := m.EnrichMissingInfohashes(context.Background(), 10)
	if err != nil {
		t.Fatalf("EnrichMissingInfohashes: %v", err)
	}
	if engageHits.Load() != 0 {
		t.Errorf("the engaging site was opened %d times in the background", engageHits.Load())
	}
	if plainHits.Load() != 1 || report.Recovered != 1 {
		t.Errorf("plain hits = %d, recovered = %d; want 1 and 1", plainHits.Load(), report.Recovered)
	}
	if report.Skipped != 2 {
		t.Errorf("skipped = %d, want 2 (%+v)", report.Skipped, report)
	}
	if report.Failed != 0 {
		t.Errorf("failed = %d, want 0", report.Failed)
	}
}

// TestEnrichStopsRetryingADeadReleasePage covers the flood seen in the field
// (about a hundred 404s a day on limetorrents): the candidates are read oldest
// first, so a page the site removed at the front of the queue was reopened at
// every pass. A 404 counts on the source and, at the second failure, quarantines
// it; a release that answers still gets its infohash.
func TestEnrichStopsRetryingADeadReleasePage(t *testing.T) {
	var deadHits, liveHits atomic.Int32
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadHits.Add(1)
		http.NotFound(w, r)
	}))
	t.Cleanup(dead.Close)
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		liveHits.Add(1)
		_, _ = w.Write([]byte(`<html><body><a href="magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=Live">m</a></body></html>`))
	}))
	t.Cleanup(live.Close)

	st := newTestStore(t)
	v := newTestVault(t, 9)
	seedDefinition(t, st, "enrich-dead", "", enrichDefinition("enrich-dead", dead.URL, false))
	seedDefinition(t, st, "enrich-live", "", enrichDefinition("enrich-live", live.URL, false))
	deadID := createIndexer(t, st, "enrich-dead", "enrich-dead", "Dead", "torrent", "{}")
	liveID := createIndexer(t, st, "enrich-live", "enrich-live", "Live", "torrent", "{}")
	m := newTestManager(t, st, v)

	seal := func(indexerID int64, details string) string {
		ref, err := SealDownloadRef(v, indexerID, engine.Item{Title: "x", DetailsURL: details})
		if err != nil {
			t.Fatalf("SealDownloadRef: %v", err)
		}
		return ref
	}
	seedHashlessSource(t, st, seal(deadID, dead.URL+"/gone.html"), deadID, "Dead Release")
	seedHashlessSource(t, st, seal(liveID, live.URL+"/live.html"), liveID, "Live Release")

	for pass := 1; pass <= 3; pass++ {
		report, err := m.EnrichMissingInfohashes(context.Background(), 10)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		want := 0
		if pass == 1 {
			want = 1 // the live release receives its hash and stops being a candidate
		}
		if report.Recovered != want {
			t.Fatalf("pass %d: recovered = %d, want %d", pass, report.Recovered, want)
		}
	}
	if got := deadHits.Load(); got != 2 {
		t.Fatalf("dead page opened %d times, want 2 (the first failure, then the one that quarantines)", got)
	}
	if got := liveHits.Load(); got != 1 {
		t.Fatalf("live page opened %d times, want 1", got)
	}
}

// TestEnrichNotesATemporaryFailureWithoutQuarantine keeps the quarantine for
// dead links only: a login or a timeout says nothing about the release.
func TestEnrichNotesATemporaryFailureWithoutQuarantine(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)

	st := newTestStore(t)
	v := newTestVault(t, 11)
	seedDefinition(t, st, "enrich-broken", "", enrichDefinition("enrich-broken", broken.URL, false))
	id := createIndexer(t, st, "enrich-broken", "enrich-broken", "Broken", "torrent", "{}")
	m := newTestManager(t, st, v)
	ref, err := SealDownloadRef(v, id, engine.Item{Title: "x", DetailsURL: broken.URL + "/page.html"})
	if err != nil {
		t.Fatalf("SealDownloadRef: %v", err)
	}
	seedHashlessSource(t, st, ref, id, "Broken Release")

	for pass := 1; pass <= 3; pass++ {
		if _, err := m.EnrichMissingInfohashes(context.Background(), 10); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}
	// Still a candidate every time: the source is not quarantined.
	items, err := st.HashlessSources(context.Background(), 100)
	if err != nil {
		t.Fatalf("HashlessSources: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("sources = %d, want the release still attempted", len(items))
	}
}
