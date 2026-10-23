package catalog_test

// These tests compare the legacy parsers in internal/feed with the native
// definitions shipped in the catalog. They use the real engine/definition
// code, an httptest server serving the cleaned fixture pages, and the exported
// legacy API (feed.FetchRSS / feed.ParseRSS). The HTML parsers themselves are
// unexported, so the legacy side reaches them through feed.FetchRSS with a
// transport that rewrites the public tracker host to the local test server:
// the host-based dispatch inside internal/feed then selects exactly the same
// parser used in production, without adding test-only code to that package.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/family"
	"github.com/buzzqw/indexarr/internal/feed"
)

type rewriteTransport struct {
	target *url.URL
}

func (t rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = ""
	return http.DefaultTransport.RoundTrip(clone)
}

func serveFile(t *testing.T, w http.ResponseWriter, name string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("definitions", "fixtures", name))
	if err != nil {
		t.Errorf("read fixture %s: %v", name, err)
		http.Error(w, "missing fixture", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(body)
}

func parseDefinition(t *testing.T, id string) *definition.Definition {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("definitions", id+".yaml"))
	if err != nil {
		t.Fatalf("read definition %s: %v", id, err)
	}
	def, err := definition.Parse(body)
	if err != nil {
		t.Fatalf("parse definition %s: %v", id, err)
	}
	return def
}

func nativeConnector(t *testing.T, id, baseURL string) *engine.DefinitionConnector {
	t.Helper()
	def := parseDefinition(t, id)
	def.Meta.URLs[0] = baseURL + "/"
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: id, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector(%s): %v", id, err)
	}
	return connector
}

func legacyClient(server *httptest.Server) *http.Client {
	target, _ := url.Parse(server.URL)
	return &http.Client{Transport: rewriteTransport{target: target}}
}

type releaseKey struct {
	Title string
	Hash  string
}

func normalizeTitle(raw string) string {
	raw = strings.ToLower(raw)
	raw = strings.NewReplacer(
		"-", " ", "_", " ", ".", " ", "'", "", "\u2019", "", "(", " ", ")", " ",
	).Replace(raw)
	return strings.Join(strings.Fields(raw), " ")
}

func hashFromMagnet(magnet string) string {
	lower := strings.ToLower(magnet)
	index := strings.Index(lower, "urn:btih:")
	if index < 0 {
		return ""
	}
	value := magnet[index+len("urn:btih:"):]
	if cut := strings.IndexAny(value, "&?#"); cut >= 0 {
		value = value[:cut]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func assertSameReleases(t *testing.T, legacy []feed.Item, native []engine.Item) {
	t.Helper()
	legacySet := make(map[releaseKey]feed.Item, len(legacy))
	for _, item := range legacy {
		hash := strings.ToLower(item.InfoHash)
		if hash == "" {
			hash = hashFromMagnet(item.Magnet)
		}
		if hash == "" {
			t.Errorf("legacy item %q has no infohash", item.Title)
			continue
		}
		legacySet[releaseKey{normalizeTitle(item.Title), hash}] = item
	}
	nativeSet := make(map[releaseKey]engine.Item, len(native))
	for _, item := range native {
		hash := strings.ToLower(item.InfoHash)
		if hash == "" {
			hash = hashFromMagnet(item.Magnet)
		}
		if hash == "" {
			t.Errorf("native item %q has no infohash", item.Title)
			continue
		}
		nativeSet[releaseKey{normalizeTitle(item.Title), hash}] = item
	}

	if len(legacySet) != len(nativeSet) {
		t.Fatalf("release sets differ: legacy=%d native=%d\nlegacy=%v\nnative=%v",
			len(legacySet), len(nativeSet), legacySet, nativeSet)
	}
	t.Logf("equivalent releases: %d (legacy rows %d, native rows %d)", len(legacySet), len(legacy), len(native))
	for key, legacyItem := range legacySet {
		nativeItem, ok := nativeSet[key]
		if !ok {
			t.Errorf("native definition is missing %+v", key)
			continue
		}
		// Categories are "compatible": when the legacy parser knows a numeric
		// category, the native row must carry it. A legacy empty category (the
		// HTML parsers mostly do not map one) imposes no constraint.
		if strings.TrimSpace(legacyItem.Category) == "" {
			continue
		}
		want, err := strconv.Atoi(legacyItem.Category)
		if err != nil {
			t.Errorf("legacy category %q is not numeric", legacyItem.Category)
			continue
		}
		found := false
		for _, got := range nativeItem.Categories {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%+v: legacy category %d, native categories %v", key, want, nativeItem.Categories)
		}
	}
}

func TestExtToEquivalence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/browse/":
			serveFile(t, w, "extto/listing.html")
		case strings.HasPrefix(r.URL.Path, "/post-detail/"):
			serveFile(t, w, "extto/detail.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	legacy, err := feed.FetchRSS(ctx, legacyClient(server),
		"https://extto.org/browse/?filter=c=TV", feed.RequestOptions{})
	if err != nil {
		t.Fatalf("legacy FetchRSS: %v", err)
	}
	connector := nativeConnector(t, "extto", server.URL)
	native, err := connector.Search(ctx, engine.SearchRequest{Mode: engine.ModeSearch})
	if err != nil {
		t.Fatalf("native Search: %v", err)
	}
	assertSameReleases(t, legacy, native)

	payload, err := connector.Download(ctx, engine.Item{
		Title:      "Yaga S01E02",
		DetailsURL: server.URL + "/post-detail/95cbfa/yaga-s01e02/",
	})
	if err != nil {
		t.Fatalf("Download(detail): %v", err)
	}
	if got := hashFromMagnet(payload.Magnet); got != "ec4e3af4d23a902bb617150cf8c9ff53ea8e01f6" {
		t.Errorf("detail magnet hash = %q", got)
	}
}

func TestTorrentGalaxyEquivalence(t *testing.T) {
	details := map[string]string{
		"/post-detail/95ac45/echo-world-the-arks-ascension-2026-1080p-web-dl-h264-playfuldarkness/": "torrentgalaxy/details-95ac45.html",
		"/post-detail/95b0a7/by-any-means-2026-1080p-web-dl-hevc-x265-5-1-bone/":                    "torrentgalaxy/details-95b0a7.html",
		"/post-detail/957fe9/dracula-eternal-2026-1080p-web-dl-h264-playfuldarkness/":               "torrentgalaxy/details-957fe9.html",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/get-posts/user:anonymous/" {
			serveFile(t, w, "torrentgalaxy/listing.html")
			return
		}
		if file, ok := details[r.URL.Path]; ok {
			serveFile(t, w, file)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	legacy, err := feed.FetchRSS(ctx, legacyClient(server),
		"https://torrentgalaxy.one/get-posts/user:anonymous/", feed.RequestOptions{})
	if err != nil {
		t.Fatalf("legacy FetchRSS: %v", err)
	}
	connector := nativeConnector(t, "torrentgalaxy", server.URL)
	native, err := connector.Search(ctx, engine.SearchRequest{Mode: engine.ModeSearch})
	if err != nil {
		t.Fatalf("native Search: %v", err)
	}
	assertSameReleases(t, legacy, native)

	payload, err := connector.Download(ctx, engine.Item{
		Title:      "Echo World",
		DetailsURL: server.URL + "/post-detail/95ac45/echo-world-the-arks-ascension-2026-1080p-web-dl-h264-playfuldarkness/",
	})
	if err != nil {
		t.Fatalf("Download(detail): %v", err)
	}
	if got := hashFromMagnet(payload.Magnet); got != "c3361fc2a6209be84fef6649ef0c3b67efb1af34" {
		t.Errorf("detail magnet hash = %q", got)
	}
}

// TestTorrentGalaxyNativeFields verifies the fields the UI needs from the WP7
// fixture. The listing has size and seeders/leechers cells; it has no category
// column, so the definition cannot map one without inventing data. That absence
// is documented in torrentgalaxy.yaml and the fixture README.
func TestTorrentGalaxyNativeFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/get-posts/user:anonymous/" {
			serveFile(t, w, "torrentgalaxy/listing.html")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "torrentgalaxy", server.URL)
	native, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch})
	if err != nil {
		t.Fatalf("native Search: %v", err)
	}
	if len(native) == 0 {
		t.Fatal("no rows extracted from the TorrentGalaxy fixture")
	}
	for _, item := range native {
		if item.Size <= 0 {
			t.Errorf("%q: size was not extracted", item.Title)
		}
		if item.Seeders <= 0 {
			t.Errorf("%q: seeders were not extracted", item.Title)
		}
		if item.Leechers < 0 {
			t.Errorf("%q: leechers were not extracted", item.Title)
		}
		// With no category cell the engine falls back to 8000 "Other". That is
		// the generic unknown bucket, not a category read from the fixture.
		if len(item.Categories) != 1 || item.Categories[0] != 8000 {
			t.Errorf("%q: categories = %v, want the 8000 fallback", item.Title, item.Categories)
		}
	}
	if native[0].Seeders != 107 || native[0].Leechers != 35 {
		t.Errorf("first row health = %d/%d, want 107/35", native[0].Seeders, native[0].Leechers)
	}
	if native[0].Size != 1500000000 {
		t.Errorf("first row size = %d, want 1500000000", native[0].Size)
	}
}

func TestCorsaroEquivalence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/index.php":
			serveFile(t, w, "corsaro/listing.html")
		case r.URL.Path == "/Ho_ucciso_Napoleone_-979378.html":
			serveFile(t, w, "corsaro/detail.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	legacy, err := feed.FetchRSS(ctx, legacyClient(server),
		"https://ilcorsaroblu.org/index.php?page=torrents&category=0&active=0", feed.RequestOptions{})
	if err != nil {
		t.Fatalf("legacy FetchRSS: %v", err)
	}
	connector := nativeConnector(t, "corsaro", server.URL)
	native, err := connector.Search(ctx, engine.SearchRequest{Mode: engine.ModeSearch})
	if err != nil {
		t.Fatalf("native Search: %v", err)
	}
	assertSameReleases(t, legacy, native)

	payload, err := connector.Download(ctx, engine.Item{
		Title:      "Ho ucciso Napoleone",
		DetailsURL: server.URL + "/Ho_ucciso_Napoleone_-979378.html",
	})
	if err != nil {
		t.Fatalf("Download(detail): %v", err)
	}
	if got := hashFromMagnet(payload.Magnet); got != "79eeef59fbfd34957df5fa2045fc97e49732b8fa" {
		t.Errorf("detail magnet hash = %q", got)
	}
}

// TestRSSDefinitionsComparison runs the native RSS family connector using the
// settings declared by the catalog definition and compares it with the legacy
// feed parser on the same fixture.
func TestRSSDefinitionsComparison(t *testing.T) {
	tests := []struct {
		id      string
		fixture string
	}{
		{"knaben", "knaben/feed.xml"},
		{"torrentleech-rss", "torrentleech/feed.xml"},
		{"showrss", "showrss/feed.rss"},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("definitions", "fixtures", tc.fixture))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			legacy, err := feed.ParseRSS(body)
			if err != nil {
				t.Fatalf("legacy ParseRSS: %v", err)
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write(body)
			}))
			t.Cleanup(server.Close)

			def := parseDefinition(t, tc.id)
			settings := map[string]string{"url": server.URL + "/feed.xml"}
			for _, setting := range def.Settings {
				if _, set := settings[setting.Name]; !set && setting.Default != "" {
					settings[setting.Name] = setting.Default
				}
			}

			registry := engine.NewRegistry()
			if err := family.Register(registry); err != nil {
				t.Fatalf("family.Register: %v", err)
			}
			connector, err := registry.NewConnector(def.Connector, engine.Config{
				Indexer:   engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
				Settings:  settings,
				Transport: engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
			})
			if err != nil {
				t.Fatalf("NewConnector(%s): %v", def.Connector, err)
			}
			native, err := connector.Latest(context.Background(), engine.LatestRequest{})
			if err != nil {
				t.Fatalf("native Latest: %v", err)
			}
			if len(native) != len(legacy) {
				t.Fatalf("native items = %d, legacy items = %d", len(native), len(legacy))
			}
			for i := range legacy {
				if normalizeTitle(native[i].Title) != normalizeTitle(legacy[i].Title) {
					t.Errorf("item %d title = %q, want %q", i, native[i].Title, legacy[i].Title)
				}
				if native[i].InfoHash != strings.ToLower(legacy[i].InfoHash) {
					t.Errorf("item %d infohash = %q, want %q", i, native[i].InfoHash, legacy[i].InfoHash)
				}
				if legacy[i].TorrentURL != "" && native[i].DownloadURL != legacy[i].TorrentURL {
					t.Errorf("item %d download = %q, want %q", i, native[i].DownloadURL, legacy[i].TorrentURL)
				}
			}
		})
	}
}

func TestCatalogVerifiedDefinitionsExist(t *testing.T) {
	entries, err := catalog.Builtins()
	if err != nil {
		t.Fatalf("catalog.Builtins: %v", err)
	}
	states := make(map[string]string, len(entries))
	for _, entry := range entries {
		states[entry.ID] = entry.State
	}
	for _, id := range []string{"extto", "torrentgalaxy", "corsaro", "knaben"} {
		if states[id] != "verificata" {
			t.Errorf("%s: state = %q, want verificata", id, states[id])
		}
	}
	for _, id := range []string{"torrentleech", "torrentleech-rss", "corsaro-account", "ildragonegro"} {
		if states[id] != "bozza" {
			t.Errorf("%s: state = %q, want bozza (needs the coordinator's credentials)", id, states[id])
		}
	}
}
