package indexer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
)

type usenetFakeConnector struct {
	items       []engine.Item
	body        []byte
	contentType string
	downloads   int
}

func (f *usenetFakeConnector) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		Protocol:   engine.ProtocolUsenet,
		Modes:      []engine.Mode{engine.ModeSearch},
		Categories: []int{5030},
	}
}

func (f *usenetFakeConnector) Search(context.Context, engine.SearchRequest) ([]engine.Item, error) {
	return append([]engine.Item(nil), f.items...), nil
}

func (f *usenetFakeConnector) Latest(context.Context, engine.LatestRequest) ([]engine.Item, error) {
	return append([]engine.Item(nil), f.items...), nil
}

func (f *usenetFakeConnector) Download(context.Context, engine.Item) (engine.Payload, error) {
	f.downloads++
	return engine.Payload{Body: f.body, ContentType: f.contentType}, nil
}

func (f *usenetFakeConnector) Test(context.Context) (engine.TestResult, error) {
	return engine.TestResult{OK: true}, nil
}

func newUsenetManager(t *testing.T, st *store.Store, fake *usenetFakeConnector, maxTorrents int) *Manager {
	t.Helper()
	registry := engine.NewRegistry()
	if err := registry.RegisterConnector("fake", func(engine.Config) (engine.Connector, error) {
		return fake, nil
	}); err != nil {
		t.Fatalf("RegisterConnector: %v", err)
	}
	m, err := New(Config{
		Store:             st,
		Vault:             newTestVault(t, 1),
		Registry:          registry,
		Transport:         newTestTransport(),
		MaxTorrentsPerRun: maxTorrents,
		Logger:            slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func seedUsenetIndexer(t *testing.T, st *store.Store) int64 {
	t.Helper()
	body := `schema: 1
meta: {id: fake-usenet, name: Fake Usenet, type: private, protocol: usenet, urls: ["https://tracker.example/"]}
connector: fake
`
	seedDefinition(t, st, "fake-usenet", "fake", body)
	return createIndexer(t, st, "fake-usenet", "fake-usenet", "Fake Usenet", "usenet", `{"query":"anything"}`)
}

func TestRunUsenetStoresNZBPayloadAndUpdatesWithoutDuplicating(t *testing.T) {
	st := newTestStore(t)
	nzb := []byte(`<?xml version="1.0"?><nzb><file subject="release"/></nzb>`)
	fake := &usenetFakeConnector{
		items: []engine.Item{{
			Protocol:    engine.ProtocolUsenet,
			Title:       "Usenet Release 1080p",
			GUID:        "usenet-guid-1",
			DownloadURL: "https://tracker.example/get/1.nzb?apikey=" + secretValue,
			Size:        42,
			Categories:  []int{5030},
		}},
		body:        nzb,
		contentType: "application/x-nzb",
	}
	manager := newUsenetManager(t, st, fake, 10)
	indexerID := seedUsenetIndexer(t, st)
	ctx := context.Background()

	stats, err := manager.Run(ctx, indexerID)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if stats.New != 1 || stats.Torrents != 1 || stats.Skipped != 0 {
		t.Fatalf("first stats = %+v", stats)
	}

	key := releaseKey("", fake.items[0], "fake-usenet")
	release, ok, err := st.ReleaseByKey(ctx, key)
	if err != nil || !ok {
		t.Fatalf("ReleaseByKey(%s): ok=%t err=%v", key, ok, err)
	}
	if release.Protocol != "usenet" {
		t.Errorf("protocol = %q", release.Protocol)
	}
	if string(release.Payload) != string(nzb) {
		t.Errorf("payload = %q", release.Payload)
	}
	if release.PayloadType != "application/x-nzb" {
		t.Errorf("payload type = %q", release.PayloadType)
	}
	if len(release.Torrent) != 0 {
		t.Errorf("torrent = %d bytes, want none", len(release.Torrent))
	}
	if release.InfoHash != "" {
		t.Errorf("infohash = %q, want empty for usenet", release.InfoHash)
	}
	sources, err := st.ListReleaseSources(ctx, release.ID)
	if err != nil {
		t.Fatalf("ListReleaseSources: %v", err)
	}
	if len(sources) != 1 || sources[0].DownloadRef != "" {
		t.Errorf("sources = %+v, want no download ref once the payload is stored", sources)
	}

	second, err := manager.Run(ctx, indexerID)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if second.New != 0 || second.Updated != 1 {
		t.Errorf("second stats = %+v", second)
	}
	if fake.downloads != 1 {
		t.Errorf("downloads = %d, want 1 (payload is cached)", fake.downloads)
	}
	_, total, err := st.SearchReleases(ctx, store.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("SearchReleases: %v", err)
	}
	if total != 1 {
		t.Errorf("releases = %d, want 1", total)
	}
	again, err := st.GetRelease(ctx, release.ID)
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if string(again.Payload) != string(nzb) {
		t.Errorf("payload after second run = %q", again.Payload)
	}
}

func TestRunUsenetStoresSealedDownloadRefWithoutSecrets(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	fake := &usenetFakeConnector{
		items: []engine.Item{
			{
				Protocol:    engine.ProtocolUsenet,
				Title:       "First",
				GUID:        "guid-first",
				DownloadURL: "https://tracker.example/get/1.nzb?apikey=" + secretValue,
				Size:        10,
				Categories:  []int{5030},
			},
			{
				Protocol:    engine.ProtocolUsenet,
				Title:       "Second",
				GUID:        "guid-second",
				DownloadURL: "https://tracker.example/get/2.nzb?apikey=" + secretValue,
				Size:        20,
				Categories:  []int{5030},
			},
		},
		body:        []byte("<nzb/>"),
		contentType: "application/x-nzb",
	}
	manager := newUsenetManager(t, st, fake, 1)
	indexerID := seedUsenetIndexer(t, st)
	ctx := context.Background()

	stats, err := manager.Run(ctx, indexerID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.New != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	if fake.downloads != 1 {
		t.Fatalf("downloads = %d, want budget of 1", fake.downloads)
	}

	key := releaseKey("", fake.items[1], "fake-usenet")
	release, ok, err := st.ReleaseByKey(ctx, key)
	if err != nil || !ok {
		t.Fatalf("ReleaseByKey: ok=%t err=%v", ok, err)
	}
	sources, err := st.ListReleaseSources(ctx, release.ID)
	if err != nil {
		t.Fatalf("ListReleaseSources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %+v", sources)
	}
	ref := sources[0].DownloadRef
	if ref == "" {
		t.Fatal("download ref is empty")
	}
	for _, secret := range []string{"tracker.example", "https://", secretValue, "2.nzb"} {
		if strings.Contains(ref, secret) {
			t.Errorf("download ref leaks %q: %s", secret, ref)
		}
	}
	parsed, err := OpenDownloadRef(v, ref)
	if err != nil {
		t.Fatalf("OpenDownloadRef: %v", err)
	}
	if parsed.IndexerID != indexerID {
		t.Errorf("ref indexer = %d, want %d", parsed.IndexerID, indexerID)
	}
	if parsed.Item.DownloadURL != fake.items[1].DownloadURL {
		t.Errorf("ref download URL = %q", parsed.Item.DownloadURL)
	}
	if parsed.Item.GUID != "guid-second" {
		t.Errorf("ref GUID = %q", parsed.Item.GUID)
	}
}

const newznabLiveCaps = `<?xml version="1.0" encoding="UTF-8"?>
<caps>
  <searching>
    <search available="yes" supportedParams="q"/>
    <tv-search available="yes" supportedParams="q,season,ep"/>
    <movie-search available="yes" supportedParams="q,imdbid"/>
    <music-search available="yes" supportedParams="q,artist,album"/>
    <book-search available="yes" supportedParams="q,author,title"/>
  </searching>
  <categories>
    <category id="3000" name="Audio"/>
    <category id="7000" name="Books"/>
  </categories>
</caps>`

// TestSearchLiveIncludesNewznabBookAndMusic proves the Newznab connector is
// part of the live fan-out, not only the archive run, and that the book and
// music functions reach it with the Newznab t= value.
func TestSearchLiveIncludesNewznabBookAndMusic(t *testing.T) {
	var mu sync.Mutex
	modes := map[string]int{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := r.URL.Query().Get("t")
		switch mode {
		case "caps":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, newznabLiveCaps)
		case "get":
			w.Header().Set("Content-Type", "application/x-nzb")
			_, _ = io.WriteString(w, `<?xml version="1.0"?><nzb><file subject="live"/></nzb>`)
		default:
			mu.Lock()
			modes[mode]++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/rss+xml")
			title := "Usenet Live Book"
			if mode == "music" {
				title = "Usenet Live Music"
			}
			_, _ = fmt.Fprintf(w, `<?xml version="1.0"?>
<rss version="2.0" xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
<channel><item>
<title>%s</title><guid isPermaLink="false">%s-guid</guid>
<link>https://indexer.example/details/%s</link>
<enclosure url="https://indexer.example/api?t=get&amp;id=%s-guid" length="123" type="application/x-nzb"/>
<newznab:attr name="size" value="123"/>
<newznab:attr name="category" value="7000"/>
</item></channel></rss>`, title, mode, mode, mode)
		}
	}))
	defer upstream.Close()

	st := newTestStore(t)
	manager := newTestManager(t, st, newTestVault(t, 1))
	body := `schema: 1
connector: newznab
meta:
  id: newznab-live
  name: Newznab Live
  type: private
  protocol: usenet
  urls:
    - https://indexer.example/
settings:
  - {name: url, type: text, required: true}
`
	seedDefinition(t, st, "newznab-live", "newznab", body)
	indexerID := createIndexer(t, st, "newznab-live", "newznab-live", "Newznab Live", "usenet",
		fmt.Sprintf(`{"url":%q,"categories":"7000,3000"}`, upstream.URL+"/api"))

	for _, tc := range []struct {
		mode  string
		query string
	}{
		{"book", "Book title"},
		{"music", "Music album"},
	} {
		result, err := manager.SearchLive(context.Background(), LiveQuery{
			Mode: tc.mode, Query: tc.query, Limit: 50,
		})
		if err != nil {
			t.Fatalf("%s SearchLive(%q): %v", tc.mode, tc.query, err)
		}
		if result.Stored != 1 {
			t.Fatalf("%s SearchLive(%q) stored = %d, want 1", tc.mode, tc.query, result.Stored)
		}
		if len(result.Indexers) != 1 || result.Indexers[0].IndexerID != indexerID ||
			result.Indexers[0].Mode != tc.mode || result.Indexers[0].Results != 1 {
			t.Fatalf("%s SearchLive(%q) outcome = %+v", tc.mode, tc.query, result.Indexers)
		}
		mu.Lock()
		count := modes[tc.mode]
		mu.Unlock()
		if count == 0 {
			t.Errorf("%s search never reached Newznab: %v", tc.mode, modes)
		}
	}

	for _, title := range []string{"Usenet Live Book", "Usenet Live Music"} {
		releases, total, err := st.SearchReleases(context.Background(), store.Filter{Query: title, Limit: 10})
		if err != nil || total != 1 || len(releases) != 1 {
			t.Fatalf("stored %q = %d/%d, %v", title, total, len(releases), err)
		}
		release, err := st.GetRelease(context.Background(), releases[0].ID)
		if err != nil {
			t.Fatalf("GetRelease: %v", err)
		}
		if release.Protocol != "usenet" {
			t.Errorf("live usenet release = protocol %q, want usenet", release.Protocol)
		}
		if release.InfoHash != "" || release.Magnet != "" {
			t.Errorf("live usenet release gained torrent fields: hash %q magnet %q", release.InfoHash, release.Magnet)
		}
		// A live search never downloads a payload: the NZB stays behind a
		// sealed reference that is opened only when a client grabs the row.
		if len(release.Payload) != 0 || release.PayloadType != "" {
			t.Errorf("live usenet release downloaded eagerly: %d bytes type %q", len(release.Payload), release.PayloadType)
		}
		sources, err := st.ListReleaseSources(context.Background(), release.ID)
		if err != nil || len(sources) != 1 || strings.TrimSpace(sources[0].DownloadRef) == "" {
			t.Errorf("live usenet source = %+v, %v; want a sealed download reference", sources, err)
		}
	}
}

func TestRunTorrentPathStillSetsTorrentProtocol(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	server := newSearchServer(t)
	manager := newTestManager(t, st, v)
	indexerID := seedNative(t, st, v, server.URL, true)
	ctx := context.Background()

	if _, err := manager.Run(ctx, indexerID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	magnetRelease, ok, err := st.ReleaseByInfoHash(ctx, hashA)
	if err != nil || !ok {
		t.Fatalf("ReleaseByInfoHash(%s): ok=%t err=%v", hashA, ok, err)
	}
	if magnetRelease.Protocol != "torrent" {
		t.Errorf("protocol = %q, want torrent", magnetRelease.Protocol)
	}
	if len(magnetRelease.Payload) != 0 || magnetRelease.PayloadType != "" {
		t.Errorf("torrent release has payload %q type %q", magnetRelease.Payload, magnetRelease.PayloadType)
	}

	expected, err := feed.InfoHash(torrentBody)
	if err != nil {
		t.Fatalf("feed.InfoHash: %v", err)
	}
	downloaded, ok, err := st.ReleaseByInfoHash(ctx, expected)
	if err != nil || !ok {
		t.Fatalf("ReleaseByInfoHash(%s): ok=%t err=%v", expected, ok, err)
	}
	if downloaded.Protocol != "torrent" {
		t.Errorf("downloaded protocol = %q", downloaded.Protocol)
	}
	if string(downloaded.Torrent) != string(torrentBody) {
		t.Errorf("torrent bytes changed")
	}
	if len(downloaded.Payload) != 0 {
		t.Errorf("torrent path stored a generic payload: %q", downloaded.Payload)
	}
}
