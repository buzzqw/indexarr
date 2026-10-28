package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/family"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

const (
	secretValue = "super-secret-api-key"
	hashA       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB       = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var torrentBody = []byte("d4:infod4:name4:testee")

const searchHTML = `<html><body>
<table class="results"><tbody>
<tr>
  <td class="name"><a href="/details/1">Ubuntu 24.04</a></td>
  <td><a class="download" href="/download/1.torrent">DL</a></td>
  <td><a class="cat" href="/browse?cat=5">TV</a></td>
  <td class="size">1.5 GB</td>
  <td class="seeders">1,234</td>
  <td class="leechers">10</td>
  <td class="hash">` + hashA + `</td>
  <td class="date">02-01-2026 15:04</td>
</tr>
<tr>
  <td class="name"><a href="/details/2">Ubuntu Server 24.04</a></td>
  <td><a class="download" href="/download/2.torrent">DL</a></td>
  <td><a class="cat" href="/browse?cat=5">TV</a></td>
  <td class="size">2 GB</td>
  <td class="seeders">5</td>
  <td class="leechers">0</td>
  <td class="hash"></td>
  <td class="date">03-01-2026 10:00</td>
</tr>
<tr>
  <td class="name"><a href="/details/3">Ubuntu 24.04 REPACK</a></td>
  <td><a class="download" href="/download/1.torrent">DL</a></td>
  <td><a class="cat" href="/browse?cat=5">TV</a></td>
  <td class="size">1.5 GB</td>
  <td class="seeders">1,234</td>
  <td class="leechers">10</td>
  <td class="hash">` + hashA + `</td>
  <td class="date">02-01-2026 15:04</td>
</tr>
</tbody></table>
</body></html>`

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return string(body)
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return st
}

func newTestVault(t *testing.T, seed byte) *vault.Vault {
	t.Helper()
	key := make([]byte, 32)
	key[0] = seed
	v, err := vault.New(key)
	if err != nil {
		t.Fatalf("vault.New: %v", err)
	}
	return v
}

func newTestTransport() *engine.Transport {
	return engine.NewTransport(engine.TransportConfig{AllowPrivate: true})
}

func newTestManager(t *testing.T, st *store.Store, v *vault.Vault) *Manager {
	t.Helper()
	m, err := New(Config{
		Store:     st,
		Vault:     v,
		Transport: newTestTransport(),
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if err != nil {
		t.Fatalf("indexer.New: %v", err)
	}
	return m
}

func seedDefinition(t *testing.T, st *store.Store, id, familyName, body string) store.Definition {
	t.Helper()
	if _, err := definition.Parse([]byte(body)); err != nil {
		t.Fatalf("test definition %s does not parse: %v", id, err)
	}
	d := store.Definition{
		ID:            id,
		SchemaVersion: definition.CurrentSchema,
		Family:        familyName,
		State:         "verificata",
		Origin:        "local",
		Author:        "test",
		Body:          body,
		Hash:          definition.Fingerprint([]byte(body)),
		UpdatedAt:     time.Now(),
	}
	if err := st.UpsertDefinition(context.Background(), d); err != nil {
		t.Fatalf("UpsertDefinition(%s): %v", id, err)
	}
	return d
}

func createIndexer(t *testing.T, st *store.Store, definitionID, slug, name, protocol, configJSON string) int64 {
	t.Helper()
	id, err := st.CreateIndexer(context.Background(), store.Indexer{
		DefinitionID: definitionID,
		Protocol:     protocol,
		Slug:         slug,
		Name:         name,
		Enabled:      true,
		Priority:     25,
		ConfigJSON:   configJSON,
	})
	if err != nil {
		t.Fatalf("CreateIndexer(%s): %v", slug, err)
	}
	return id
}

func putSecret(t *testing.T, st *store.Store, v *vault.Vault, indexerID int64, key, value string) {
	t.Helper()
	nonce, ciphertext, err := v.Seal(value)
	if err != nil {
		t.Fatalf("seal secret: %v", err)
	}
	if err := st.PutSecret(context.Background(), store.Secret{
		IndexerID:  indexerID,
		Key:        key,
		Nonce:      nonce,
		Ciphertext: ciphertext,
	}); err != nil {
		t.Fatalf("PutSecret(%s): %v", key, err)
	}
}

func seedNative(t *testing.T, st *store.Store, v *vault.Vault, baseURL string, withSecret bool) int64 {
	t.Helper()
	body := strings.ReplaceAll(readTestdata(t, "native_search.yaml"), "http://REPLACE", baseURL)
	seedDefinition(t, st, "native-test", "", body)
	config := fmt.Sprintf(`{"url":%q,"query":"ubuntu","categories":[5040]}`, baseURL)
	id := createIndexer(t, st, "native-test", "native-test", "Native Test", "torrent", config)
	if withSecret {
		putSecret(t, st, v, id, "api_key", secretValue)
	}
	return id
}

// newSearchServer serves the HTML search page, a .torrent payload and a root
// page for the Test reach phase.
func newSearchServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			_, _ = w.Write([]byte("<html><body>ok</body></html>"))
		case r.URL.Path == "/search":
			if got := r.URL.Query().Get("apikey"); got != secretValue {
				t.Errorf("search apikey = %q, want the decrypted secret", got)
				http.Error(w, "bad api key", http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(searchHTML))
		case strings.HasPrefix(r.URL.Path, "/download/"):
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = w.Write(torrentBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestConnectorAndSearch(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	server := newSearchServer(t)
	manager := newTestManager(t, st, v)
	indexerID := seedNative(t, st, v, server.URL, true)

	connector, indexer, err := manager.Connector(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("Connector: %v", err)
	}
	if connector == nil {
		t.Fatal("Connector returned nil")
	}
	if indexer.ID != indexerID || indexer.Name != "Native Test" {
		t.Fatalf("indexer = %+v", indexer)
	}

	items, err := manager.Search(context.Background(), indexerID, engine.SearchRequest{
		Mode:       engine.ModeSearch,
		Query:      "ubuntu",
		Categories: []int{5040},
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].Title != "Ubuntu 24.04" {
		t.Errorf("first title = %q", items[0].Title)
	}
	if items[0].InfoHash != hashA {
		t.Errorf("first infohash = %q", items[0].InfoHash)
	}
	if len(items[0].Categories) == 0 || items[0].Categories[0] != 5040 {
		t.Errorf("first categories = %v", items[0].Categories)
	}

	history, err := st.ListIndexerHistory(context.Background(), indexerID, 10)
	if err != nil {
		t.Fatalf("ListIndexerHistory: %v", err)
	}
	if len(history) == 0 || history[0].Kind != "search" || history[0].Status != "ok" {
		t.Fatalf("search history = %+v", history)
	}
}

func TestMissingSecretAndVaultRequired(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	server := newSearchServer(t)

	// A required setting with no secret row fails with a clear message that
	// names the setting, never a value.
	manager := newTestManager(t, st, v)
	missingID := seedNative(t, st, v, server.URL, false)
	_, _, err := manager.Connector(context.Background(), missingID)
	if err == nil {
		t.Fatal("Connector without the required secret succeeded")
	}
	if !strings.Contains(err.Error(), "missing required setting") || !strings.Contains(err.Error(), "apikey") {
		t.Fatalf("error is not clear: %v", err)
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("error leaked a secret value: %v", err)
	}

	// With a sealed secret but no vault the manager refuses to build.
	config := fmt.Sprintf(`{"url":%q}`, server.URL)
	sealedID := createIndexer(t, st, "native-test", "native-no-vault", "Native Without Vault", "torrent", config)
	putSecret(t, st, v, sealedID, "api_key", secretValue)
	noVault := newTestManager(t, st, nil)
	_, _, err = noVault.Connector(context.Background(), sealedID)
	if !errors.Is(err, ErrVaultRequired) {
		t.Fatalf("error = %v, want ErrVaultRequired", err)
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("error leaked a secret value: %v", err)
	}

	// A wrong master key also fails without echoing the plaintext.
	wrongID := createIndexer(t, st, "native-test", "native-wrong-key", "Native Wrong Key", "torrent", config)
	wrongVault := newTestVault(t, 9)
	putSecret(t, st, wrongVault, wrongID, "api_key", secretValue)
	_, _, err = manager.Connector(context.Background(), wrongID)
	if err == nil {
		t.Fatal("Connector with a wrong vault key succeeded")
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("error leaked a secret value: %v", err)
	}
}

func TestNativeRSSConnector(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rss" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<rss version="2.0"><channel>
<item><title>Native RSS Release</title><guid>rss-1</guid>
<link>magnet:?xt=urn:btih:cccccccccccccccccccccccccccccccccccccccc&amp;dn=RSS</link>
</item>
</channel></rss>`))
	}))
	t.Cleanup(server.Close)
	manager := newTestManager(t, st, v)

	body := readTestdata(t, "legacy_torznab.yaml")
	body = strings.ReplaceAll(body, "id: native-torznab", "id: native-rss")
	body = strings.ReplaceAll(body, "name: Legacy Torznab", "name: Native RSS")
	body = strings.ReplaceAll(body, "connector: torznab", "connector: rss")
	seedDefinition(t, st, "native-rss", "rss", body)
	config := fmt.Sprintf(`{"url":%q}`, server.URL+"/rss")
	id := createIndexer(t, st, "native-rss", "native-rss", "Native RSS", "torrent", config)

	connector, _, err := manager.Connector(context.Background(), id)
	if err != nil {
		t.Fatalf("Connector(native rss): %v", err)
	}
	rss, ok := connector.(*family.RSS)
	if !ok {
		t.Fatalf("connector = %T, want *family.RSS", connector)
	}
	items, err := rss.Search(context.Background(), engine.SearchRequest{Query: "native"})
	if err != nil {
		t.Fatalf("Search(native rss): %v", err)
	}
	if len(items) != 1 || items[0].Title != "Native RSS Release" {
		t.Fatalf("items = %+v", items)
	}

}

func TestConnectorCacheAndInvalidation(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	server := newSearchServer(t)
	manager := newTestManager(t, st, v)
	indexerID := seedNative(t, st, v, server.URL, true)

	first, _, err := manager.Connector(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("first Connector: %v", err)
	}
	second, _, err := manager.Connector(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("second Connector: %v", err)
	}
	if first != second {
		t.Fatal("connector cache did not reuse the same pointer")
	}

	// Editing the definition body changes its fingerprint, so the next call
	// must rebuild even when the row timestamp has not moved.
	body := strings.ReplaceAll(readTestdata(t, "native_search.yaml"), "http://REPLACE", server.URL)
	body += "\n# cache bust\n"
	seedDefinition(t, st, "native-test", "", body)
	third, _, err := manager.Connector(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("Connector after definition update: %v", err)
	}
	if third == first {
		t.Fatal("connector was not rebuilt after a definition update")
	}

	manager.Invalidate(indexerID)
	fourth, _, err := manager.Connector(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("Connector after Invalidate: %v", err)
	}
	if fourth == third {
		t.Fatal("Invalidate did not drop the cached connector")
	}

	manager.InvalidateAll()
	fifth, _, err := manager.Connector(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("Connector after InvalidateAll: %v", err)
	}
	if fifth == fourth {
		t.Fatal("InvalidateAll did not drop the cached connector")
	}
}

func TestFamilyConnectorBuild(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	manager := newTestManager(t, st, v)

	seedDefinition(t, st, "family-newznab", "newznab", readTestdata(t, "family_newznab.yaml"))
	newznabID := createIndexer(t, st, "family-newznab", "family-newznab", "Family Newznab", "usenet",
		`{"base_url":"https://newznab.example/api","api_key":"key"}`)
	connector, _, err := manager.Connector(context.Background(), newznabID)
	if err != nil {
		t.Fatalf("newznab Connector: %v", err)
	}
	if _, ok := connector.(*family.Newznab); !ok {
		t.Fatalf("newznab connector = %T", connector)
	}

	seedDefinition(t, st, "family-unit3d", "unit3d", readTestdata(t, "family_unit3d.yaml"))
	unit3dID := createIndexer(t, st, "family-unit3d", "family-unit3d", "Family UNIT3D", "torrent",
		`{"base_url":"https://unit3d.example","api_key":"key"}`)
	connector, _, err = manager.Connector(context.Background(), unit3dID)
	if err != nil {
		t.Fatalf("unit3d Connector: %v", err)
	}
	if _, ok := connector.(*family.Unit3D); !ok {
		t.Fatalf("unit3d connector = %T", connector)
	}
}

func TestHelpersRecordHistory(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	server := newSearchServer(t)
	manager := newTestManager(t, st, v)
	indexerID := seedNative(t, st, v, server.URL, true)

	items, err := manager.Latest(context.Background(), indexerID, engine.LatestRequest{Categories: []int{5040}, Limit: 10})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("latest items = %d, want 2", len(items))
	}

	payload, err := manager.Download(context.Background(), indexerID, engine.Item{
		Title:       "Ubuntu Server 24.04",
		DownloadURL: server.URL + "/download/2.torrent",
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(payload.Body) != string(torrentBody) {
		t.Fatalf("download body = %q", payload.Body)
	}

	result, err := manager.Test(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK {
		t.Fatalf("Test result = %+v", result)
	}

	history, err := st.ListIndexerHistory(context.Background(), indexerID, 10)
	if err != nil {
		t.Fatalf("ListIndexerHistory: %v", err)
	}
	kinds := make(map[string]store.IndexerHistory, len(history))
	for _, row := range history {
		kinds[row.Kind] = row
	}
	for _, kind := range []string{"latest", "download", "test"} {
		row, ok := kinds[kind]
		if !ok {
			t.Errorf("no %s history row", kind)
			continue
		}
		if row.Status != "ok" {
			t.Errorf("%s status = %q, want ok", kind, row.Status)
		}
	}
	if kinds["download"].Bytes != int64(len(torrentBody)) {
		t.Errorf("download bytes = %d", kinds["download"].Bytes)
	}
}

// fakeConnector lets the run deduplication be tested without the engine's own
// in-search dedupe.
type fakeConnector struct {
	items       []engine.Item
	downloadErr error
}

func (f *fakeConnector) Capabilities() engine.Capabilities {
	return engine.Capabilities{
		Protocol:   engine.ProtocolTorrent,
		Modes:      []engine.Mode{engine.ModeSearch},
		Categories: []int{5040},
	}
}

func (f *fakeConnector) Search(context.Context, engine.SearchRequest) ([]engine.Item, error) {
	return append([]engine.Item(nil), f.items...), nil
}

func (f *fakeConnector) Latest(context.Context, engine.LatestRequest) ([]engine.Item, error) {
	return append([]engine.Item(nil), f.items...), nil
}

func (f *fakeConnector) Download(context.Context, engine.Item) (engine.Payload, error) {
	if f.downloadErr != nil {
		return engine.Payload{}, f.downloadErr
	}
	return engine.Payload{Magnet: feed.MagnetFromHash(hashB, "Two")}, nil
}

func (f *fakeConnector) Test(context.Context) (engine.TestResult, error) {
	return engine.TestResult{OK: true}, nil
}

func TestRunDeduplicatesWithinRun(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	registry := engine.NewRegistry()
	fake := &fakeConnector{items: []engine.Item{
		{Title: "One", InfoHash: hashA, Magnet: feed.MagnetFromHash(hashA, "One"), Size: 10, Categories: []int{5040}},
		{Title: "Two", GUID: "guid-1", DownloadURL: "https://fake.example/dl/1", Size: 20, Categories: []int{5040}},
		{Title: "Three", InfoHash: hashA, Magnet: feed.MagnetFromHash(hashA, "Three"), Size: 10, Categories: []int{5040}},
	}}
	if err := registry.RegisterConnector("fake", func(engine.Config) (engine.Connector, error) {
		return fake, nil
	}); err != nil {
		t.Fatalf("RegisterConnector: %v", err)
	}
	manager, err := New(Config{
		Store:     st,
		Vault:     v,
		Registry:  registry,
		Transport: newTestTransport(),
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := `schema: 1
meta: {id: fake-test, name: Fake Test, type: public, protocol: torrent, urls: ["https://fake.example/"]}
connector: fake
`
	seedDefinition(t, st, "fake-test", "fake", body)
	indexerID := createIndexer(t, st, "fake-test", "fake-test", "Fake Test", "torrent", `{"query":"anything"}`)

	stats, err := manager.Run(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stats.Seen != 3 {
		t.Errorf("Seen = %d, want 3", stats.Seen)
	}
	if stats.New != 2 {
		t.Errorf("New = %d, want 2", stats.New)
	}
	if stats.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", stats.Skipped)
	}
	if len(stats.Items) != 2 {
		t.Errorf("Items = %d, want 2", len(stats.Items))
	}

	_, total, err := st.SearchReleases(context.Background(), store.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("SearchReleases: %v", err)
	}
	if total != 2 {
		t.Errorf("releases = %d, want 2", total)
	}
}

func TestNativeTorznabConnector(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	manager := newTestManager(t, st, v)

	body := "schema: 1\nconnector: torznab\nmeta:\n  id: native-torznab\n  name: Native Torznab\n  type: private\n  protocol: torrent\n  urls:\n    - https://tracker.example/api\nsettings:\n  - {name: apikey, type: password, required: true}\n"
	seedDefinition(t, st, "native-torznab", "", body)
	id := createIndexer(t, st, "native-torznab", "native-torznab", "Native Torznab", "torrent", `{"base_url":"https://tracker.example/api"}`)
	putSecret(t, st, v, id, "apikey", "torznab-key")

	connector, _, err := manager.Connector(context.Background(), id)
	if err != nil {
		t.Fatalf("Connector: %v", err)
	}
	if _, ok := connector.(*family.Torznab); !ok {
		t.Fatalf("connector = %T, want *family.Torznab", connector)
	}
}

func TestEd2kSourceStoresTorrentTopicsAsTorrents(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 1)
	registry := engine.NewRegistry()
	const md4 = "0123456789abcdef0123456789abcdef"
	items := []engine.Item{
		{Protocol: engine.ProtocolEd2k, Title: "Film.mkv", GUID: "ed2k:" + md4, Size: 700,
			Ed2k: "ed2k://|file|Film.mkv|700|" + md4 + "|/", Ed2kHash: md4, Categories: []int{2000}},
		// A topic that carried a magnet: the engine turns the row into a torrent.
		{Protocol: engine.ProtocolTorrent, Title: "Old Game", GUID: "topic-2", InfoHash: hashA,
			Magnet: feed.MagnetFromHash(hashA, "Old Game"), Size: 10, Categories: []int{1000},
			DetailsURL: "https://fake.example/viewtopic.php?t=2"},
		// A topic whose page could not be opened: no link at all.
		{Protocol: engine.ProtocolEd2k, Title: "Unopened", GUID: "topic-3", Categories: []int{2000},
			DetailsURL: "https://fake.example/viewtopic.php?t=3"},
	}
	fake := &fakeConnector{items: items}
	if err := registry.RegisterConnector("fake", func(engine.Config) (engine.Connector, error) {
		return fake, nil
	}); err != nil {
		t.Fatalf("RegisterConnector: %v", err)
	}
	manager, err := New(Config{
		Store:     st,
		Vault:     v,
		Registry:  registry,
		Transport: newTestTransport(),
		Logger:    slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	body := `schema: 1
meta: {id: fake-ed2k, name: Fake eD2k, type: public, protocol: ed2k, urls: ["https://fake.example/"]}
connector: fake
`
	seedDefinition(t, st, "fake-ed2k", "fake", body)
	indexerID := createIndexer(t, st, "fake-ed2k", "fake-ed2k", "Fake eD2k", "ed2k", `{"query":"anything"}`)

	protocols := func() map[string]string {
		t.Helper()
		releases, _, err := st.SearchReleases(context.Background(), store.Filter{Limit: 10})
		if err != nil {
			t.Fatalf("SearchReleases: %v", err)
		}
		out := make(map[string]string, len(releases))
		for _, release := range releases {
			out[release.Title] = release.Protocol
		}
		return out
	}

	if _, err := manager.Run(context.Background(), indexerID); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := protocols()
	if got["Film.mkv"] != "ed2k" || got["Old Game"] != "torrent" || len(got) != 2 {
		t.Errorf("archive = %v, want Film.mkv=ed2k and Old Game=torrent only", got)
	}

	indexer, err := st.GetIndexer(context.Background(), indexerID)
	if err != nil {
		t.Fatalf("GetIndexer: %v", err)
	}
	items[1].Title, items[1].InfoHash = "Old Game Live", hashB
	items[1].Magnet = feed.MagnetFromHash(hashB, "Old Game Live")
	items[2].Title = "Unopened Live"
	if _, err := manager.storeLiveItems(context.Background(), indexer, items[1:], store.NewBlocklistMatcher(nil)); err != nil {
		t.Fatalf("storeLiveItems: %v", err)
	}
	got = protocols()
	if got["Old Game Live"] != "torrent" {
		t.Errorf("live magnet topic protocol = %q, want torrent", got["Old Game Live"])
	}
	if _, stored := got["Unopened Live"]; stored {
		t.Error("a live topic without any link was stored: its grab would return the first file")
	}
}

// A search without a flood turn is queued, and the background send that
// really reaches the site leaves its own row: the caller's canceled row alone
// would hide whether the site accepted, refused or dropped the queued search.
func TestDeferredSearchSendIsRecordedInHistory(t *testing.T) {
	st := newTestStore(t)
	v := newTestVault(t, 6)
	var searches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			_, _ = w.Write([]byte("<html><body>ok</body></html>"))
		case r.URL.Path == "/search":
			searches.Add(1)
			_, _ = w.Write([]byte(searchHTML))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	body := strings.ReplaceAll(readTestdata(t, "native_search.yaml"), "http://REPLACE", server.URL)
	body = strings.Replace(body, "search:\n", "search:\n  interval: 500ms\n  cache: 1m\n", 1)
	seedDefinition(t, st, "native-flood", "", body)
	config := fmt.Sprintf(`{"url":%q,"categories":[5040]}`, server.URL)
	indexerID := createIndexer(t, st, "native-flood", "native-flood", "Native Flood", "torrent", config)
	putSecret(t, st, v, indexerID, "api_key", secretValue)
	manager := newTestManager(t, st, v)

	// The flood margin is 15s, so any short deadline has no turn: the search
	// is queued instead of sent.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err := manager.Search(ctx, indexerID, engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "queued", Limit: 1, FirstPageOnly: true,
	})
	cancel()
	if !errors.Is(err, engine.ErrSearchDeferred) {
		t.Fatalf("search err = %v, want ErrSearchDeferred", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	var background *store.RunMetricRow
	for time.Now().Before(deadline) {
		rows, listErr := st.ListRunMetrics(context.Background(), indexerID, 20)
		if listErr != nil {
			t.Fatalf("ListRunMetrics: %v", listErr)
		}
		for i := range rows {
			row := rows[i]
			if row.Kind == "search" && row.Query == "queued" && row.Status == "ok" {
				background = &row
				break
			}
		}
		if background != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if background == nil {
		t.Fatalf("no ok history row for the background send; requests to /search = %d", searches.Load())
	}
	if background.Error != "" {
		t.Errorf("background row error = %q, want empty", background.Error)
	}
	if searches.Load() == 0 {
		t.Error("the background send never reached the site")
	}
}
