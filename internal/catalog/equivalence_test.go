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
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	return nativeConnectorFromDefinition(t, parseDefinition(t, id), baseURL)
}

func nativeConnectorFromDefinition(t *testing.T, def *definition.Definition, baseURL string) *engine.DefinitionConnector {
	t.Helper()
	// Fixtures are served by httptest, not by the real site: a session would
	// require FlareSolverr and has its own test.
	def.Session = nil
	def.Meta.URLs[0] = baseURL + "/"
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector(%s): %v", def.Meta.ID, err)
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

func TestTorrentGalaxyUploaderResolvesMagnet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/get-posts/user:anonymous/" {
			serveFile(t, w, "torrentgalaxy/listing-user.html")
			return
		}
		if strings.Contains(r.URL.Path, "/post-detail/") {
			serveFile(t, w, "torrentgalaxy/details-uploader.html")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	ctx := context.Background()
	connector := nativeConnector(t, "torrentgalaxy", server.URL)
	native, err := connector.Latest(ctx, engine.LatestRequest{})
	if err != nil {
		t.Fatalf("native Latest: %v", err)
	}
	if len(native) < 40 {
		t.Fatalf("latest rows = %d, want the 50 uploader rows", len(native))
	}
	withMagnet := 0
	for _, item := range native {
		if item.Magnet != "" {
			withMagnet++
		}
	}
	if withMagnet < 40 {
		t.Errorf("rows with a resolved magnet = %d, want the whole listing", withMagnet)
	}
}

// TestTorrentGalaxyNativeFields verifies the fields the UI needs from the native
// fixture. The listing has size and seeders/leechers cells; it has no category
// column, so the definition cannot map one without inventing data. That absence
// is documented in torrentgalaxy.yaml and the fixture README.
func TestTorrentGalaxyNativeFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/get-posts/user:anonymous/" {
			serveFile(t, w, "torrentgalaxy/listing-user.html")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "torrentgalaxy", server.URL)
	native, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, SkipDetails: true})
	if err != nil {
		t.Fatalf("native Search: %v", err)
	}
	if len(native) != 50 {
		t.Fatalf("rows extracted from the uploader fixture = %d, want 50", len(native))
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
		if len(item.Categories) == 0 {
			t.Errorf("%q: category was not read from the row link", item.Title)
		}
	}
	if native[0].Seeders != 21 || native[0].Leechers != 38 {
		t.Errorf("first row health = %d/%d, want 21/38", native[0].Seeders, native[0].Leechers)
	}
	if native[0].Size != 3600000000 {
		t.Errorf("first row size = %d, want 3600000000", native[0].Size)
	}
	if len(native[0].Categories) != 1 || native[0].Categories[0] != 6000 {
		t.Errorf("first row category = %v, want 6000 XXX", native[0].Categories)
	}
}

func TestExtToCategoryMappingAndLatestFilters(t *testing.T) {
	var mu sync.Mutex
	var filters []string
	var queries []string
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		filters = append(filters, r.URL.Query()["filter"]...)
		queries = append(queries, r.URL.Query().Get("q"))
		pages++
		page := r.URL.Query().Get("page")
		mu.Unlock()
		if page != "" && page != "1" {
			_, _ = w.Write([]byte(`<html><body><table class="search-table"><tbody></tbody></table></body></html>`))
			return
		}
		serveFile(t, w, "extto/listing.html")
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "extto")
	def.Meta.URLs[0] = server.URL + "/"
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: "extto", Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"filter": "c=Movies", "filter-extra": "tg=Italian"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatal(err)
	}
	// A movie request without an explicit cat still maps to c=Movies.
	if _, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "Dune", Categories: []int{2000},
	}); err != nil {
		t.Fatalf("movie search: %v", err)
	}
	mu.Lock()
	gotFilters := append([]string(nil), filters...)
	gotQueries := append([]string(nil), queries...)
	mu.Unlock()
	if len(gotFilters) == 0 || gotFilters[0] != "c=Movies" {
		t.Fatalf("movie filters = %v, want c=Movies", gotFilters)
	}
	if len(gotQueries) == 0 || gotQueries[0] != "Dune" {
		t.Fatalf("q params = %v, want Dune", gotQueries)
	}

	mu.Lock()
	filters, queries, pages = nil, nil, 0
	mu.Unlock()
	if _, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "Show", Categories: []int{5000},
	}); err != nil {
		t.Fatalf("tv search: %v", err)
	}
	mu.Lock()
	gotFilters = append([]string(nil), filters...)
	mu.Unlock()
	if len(gotFilters) == 0 || gotFilters[0] != "c=TV" {
		t.Fatalf("tv filters = %v, want c=TV", gotFilters)
	}

	mu.Lock()
	filters, queries, pages = nil, nil, 0
	mu.Unlock()
	if _, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "Anything",
	}); err != nil {
		t.Fatalf("generic search: %v", err)
	}
	mu.Lock()
	gotFilters = append([]string(nil), filters...)
	mu.Unlock()
	if len(gotFilters) != 0 {
		t.Fatalf("generic search filters = %v, want none", gotFilters)
	}

	mu.Lock()
	filters, queries, pages = nil, nil, 0
	mu.Unlock()
	if _, err := connector.Latest(context.Background(), engine.LatestRequest{
		Known: func(engine.Item) bool { return true },
	}); err != nil {
		t.Fatalf("latest: %v", err)
	}
	mu.Lock()
	gotFilters = append([]string(nil), filters...)
	mu.Unlock()
	if len(gotFilters) < 2 || gotFilters[0] != "c=Movies" || gotFilters[1] != "tg=Italian" {
		t.Fatalf("latest filters = %v, want the configured pair", gotFilters)
	}
	mu.Lock()
	gotPages := pages
	mu.Unlock()
	if gotPages != 2 {
		t.Fatalf("latest read %d pages with stop_when_known false, want page 1 + empty page 2", gotPages)
	}
}

func TestFeedOnlyDetection(t *testing.T) {
	if parseDefinition(t, "torrentgalaxy").SearchUsesQuery() {
		t.Error("torrentgalaxy uploader must be feed-only")
	}
	if !parseDefinition(t, "extto").SearchUsesQuery() {
		t.Error("extto has a real query search")
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
	for _, id := range []string{
		"extto", "torrentgalaxy", "corsaro", "knaben",
		"1337x", "animetosho", "kickasstorrents", "nyaa",
		"subsplease", "tokyotosho", "torrentdownloads", "yts",
		"acgnx", "bangumimoe", "mikan", "iptorrents", "ildragonegro",
	} {
		if states[id] != "verificata" {
			t.Errorf("%s: state = %q, want verificata", id, states[id])
		}
	}
	for _, id := range []string{"torrentleech", "torrentleech-rss", "corsaro-account"} {
		if states[id] != "bozza" {
			t.Errorf("%s: state = %q, want bozza (needs an account to verify)", id, states[id])
		}
	}
}

func TestTGxCapabilitiesAreLocalOnly(t *testing.T) {
	tgx := nativeConnector(t, "torrentgalaxy", "http://127.0.0.1:1")
	if !tgx.Capabilities().LocalOnly {
		t.Error("TorrentGalaxy uploader capabilities are not LocalOnly")
	}
	extto := nativeConnector(t, "extto", "http://127.0.0.1:1")
	if extto.Capabilities().LocalOnly {
		t.Error("ExtTo is a query search and must not be LocalOnly")
	}
}

func TestCorsaroUsesAFlareSolverrSession(t *testing.T) {
	// Since 2026-10-29 a plain HTTP client gets 429/503 from Cloudflare while a
	// real browser opens the listing. The definition must route every request
	// through FlareSolverr, otherwise a run stores zero rows and still reports
	// success.
	for _, id := range []string{"corsaro", "corsaro-user", "corsaro-account"} {
		def := parseDefinition(t, id)
		if def.Session == nil {
			t.Fatalf("%s: missing session", id)
		}
		if mode := strings.ToLower(strings.TrimSpace(def.Session.Mode)); mode != "flaresolverr" {
			t.Errorf("%s: session mode = %q, want flaresolverr", id, def.Session.Mode)
		}
	}
}

func TestCorsaroUserLatestList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.php" && r.URL.Query().Get("uploader") == "82400" {
			serveFile(t, w, "corsaro/user-listing.html")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	def := parseDefinition(t, "corsaro-user")
	def.Session = nil
	def.Meta.URLs[0] = server.URL + "/"
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: "corsaro-user", Name: "Corsaro user"},
		Definition: def,
		Settings:   map[string]string{"uploader": "82400"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := connector.Latest(context.Background(), engine.LatestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) < 3 {
		t.Fatalf("Corsaro user items = %d, want at least 3", len(items))
	}
}

func TestKnabenTruncatedFeed(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("definitions", "fixtures", "knaben", "truncated.xml"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := feed.ParseRSS(body)
	if err != nil {
		t.Fatalf("Knaben truncated feed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("Knaben truncated items = %d, want 2", len(items))
	}
}

func TestCorsaroUserStopsPaginationAtMaxAge(t *testing.T) {
	var requests atomic.Int64
	oldRow, err := os.ReadFile(filepath.Join("definitions", "fixtures", "corsaro", "old-page.html"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write(oldRow)
	}))
	t.Cleanup(server.Close)
	def := parseDefinition(t, "corsaro-user")
	def.Session = nil
	def.Meta.URLs[0] = server.URL + "/"
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: "corsaro-user", Name: "Corsaro user"},
		Definition: def,
		Settings:   map[string]string{"uploader": "82400"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := connector.Latest(context.Background(), engine.LatestRequest{
		MaxAge:      24 * time.Hour,
		SkipDetails: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1: old page did not stop the walk", got)
	}
}

func TestRuTorNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("search") != "matrix" {
			t.Errorf("search param = %q", r.URL.Query().Get("search"))
		}
		serveFile(t, w, "rutor/search.html")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "rutor", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	first := items[0]
	if !strings.Contains(first.Title, "The Doors") {
		t.Errorf("title = %q", first.Title)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:") {
		t.Errorf("magnet = %q", first.Magnet)
	}
	if first.Size <= 0 || first.Seeders <= 0 {
		t.Errorf("size/seeders = %d/%d", first.Size, first.Seeders)
	}
}

func TestMegaPeerNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("search") != "matrix" {
			t.Errorf("search param = %q", r.URL.Query().Get("search"))
		}
		serveFile(t, w, "megapeer/search.html")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "megapeer", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	first := items[0]
	if !strings.Contains(first.Title, "The Doors") {
		t.Errorf("title = %q", first.Title)
	}
	if !strings.Contains(first.DownloadURL, "/download/") {
		t.Errorf("download = %q", first.DownloadURL)
	}
	if first.Size <= 0 || first.Seeders <= 0 {
		t.Errorf("size/seeders = %d/%d", first.Size, first.Seeders)
	}
}

func TestDivxTotalNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			if got := r.URL.Query().Get("s"); got != "matrix" {
				t.Errorf("s param = %q", got)
			}
			serveFile(t, w, "divxtotal/search.html")
			return
		}
		serveFile(t, w, "divxtotal/detail.html")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "divxtotal", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	first := items[0]
	if first.Title != "Matrix" {
		t.Errorf("title = %q", first.Title)
	}
	if first.Size <= 0 {
		t.Errorf("size = %d", first.Size)
	}
	if !strings.Contains(first.DownloadURL, "download_tt.php") {
		t.Errorf("download = %q", first.DownloadURL)
	}
}

func TestTorrentMacNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			if got := r.URL.Query().Get("s"); got != "matrix" {
				t.Errorf("s param = %q", got)
			}
			serveFile(t, w, "torrentmac/search.html")
			return
		}
		serveFile(t, w, "torrentmac/detail.html")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "torrentmac", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	first := items[0]
	if !strings.Contains(first.Title, "Focus Matrix") {
		t.Errorf("title = %q", first.Title)
	}
	if first.Size <= 0 {
		t.Errorf("size = %d", first.Size)
	}
	if !strings.HasSuffix(first.DownloadURL, ".torrent") {
		t.Errorf("download = %q", first.DownloadURL)
	}
}

func TestNekoBTNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("q"); got != "matrix" {
			t.Errorf("q param = %q", got)
		}
		serveFile(t, w, "nekobt/search.html")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "nekobt", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	first := items[0]
	if !strings.Contains(first.Title, "Soar High") {
		t.Errorf("title = %q", first.Title)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:") {
		t.Errorf("magnet = %q", first.Magnet)
	}
	if first.Size <= 0 || first.Seeders <= 0 {
		t.Errorf("size/seeders = %d/%d", first.Size, first.Seeders)
	}
}

func TestTheRARBGNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/get-posts/" {
			if got := r.URL.Query().Get("keywords"); got != "matrix" {
				t.Errorf("keywords = %q", got)
			}
			serveFile(t, w, "therarbg/search.html")
			return
		}
		serveFile(t, w, "therarbg/detail.html")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "therarbg", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	first := items[0]
	if !strings.Contains(first.Title, "Matrix Revolutions") {
		t.Errorf("title = %q", first.Title)
	}
	if first.Size <= 0 || first.Seeders <= 0 {
		t.Errorf("size/seeders = %d/%d", first.Size, first.Seeders)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:") {
		t.Errorf("magnet = %q", first.Magnet)
	}
}

func TestAniLibertyNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/app/search/releases" {
			if got := r.URL.Query().Get("query"); got != "naruto" {
				t.Errorf("query = %q", got)
			}
			serveFile(t, w, "aniliberty/search.json")
			return
		}
		serveFile(t, w, "aniliberty/torrents.json")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "aniliberty", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "naruto", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	first := items[0]
	if !strings.Contains(first.Title, "Наруто") {
		t.Errorf("title = %q", first.Title)
	}
	if first.Size <= 0 {
		t.Errorf("size = %d", first.Size)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:") {
		t.Errorf("magnet = %q", first.Magnet)
	}
}

func Test0MagnetNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search" {
			if got := r.URL.Query().Get("q"); got != "matrix" {
				t.Errorf("q param = %q", got)
			}
			serveFile(t, w, "0magnet/search.html")
			return
		}
		serveFile(t, w, "0magnet/detail.html")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "0magnet", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	first := items[0]
	if first.Size <= 0 {
		t.Errorf("size = %d", first.Size)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:") {
		t.Errorf("magnet = %q", first.Magnet)
	}
}

func TestTorrentKittyNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search/matrix" {
			http.NotFound(w, r)
			return
		}
		serveFile(t, w, "torrentkitty/search.html")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "torrentkitty", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	first := items[0]
	if first.Size <= 0 {
		t.Errorf("size = %d", first.Size)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:") {
		t.Errorf("magnet = %q", first.Magnet)
	}
}

func TestTorrentProject2NativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			if got := r.URL.Query().Get("t"); got != "matrix" {
				t.Errorf("t param = %q", got)
			}
			serveFile(t, w, "torrentproject2/search.html")
			return
		}
		serveFile(t, w, "torrentproject2/detail.html")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "torrentproject2", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	first := items[0]
	if first.Size <= 0 || first.Seeders <= 0 {
		t.Errorf("size/seeders = %d/%d", first.Size, first.Seeders)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:") {
		t.Errorf("magnet = %q", first.Magnet)
	}
}

func TestRuTrackerNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tracker.php" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("nm"); got != "matrix" {
			t.Errorf("nm param = %q", got)
		}
		serveFile(t, w, "rutracker/search.html")
	}))
	t.Cleanup(server.Close)

	// The definition runs through a FlareSolverr session in production; this
	// test only checks the fixture extraction, so the session is dropped.
	def := parseDefinition(t, "rutracker")
	def.Session = nil
	connector := nativeConnectorFromDefinition(t, def, server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	first := items[0]
	if first.Size <= 0 || first.Seeders < 0 {
		t.Errorf("size/seeders = %d/%d", first.Size, first.Seeders)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:") {
		t.Errorf("magnet = %q", first.Magnet)
	}
}

func TestAniBTRSSFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("definitions", "fixtures", "anibt", "feed.xml"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	items, err := feed.ParseRSS(body)
	if err != nil {
		t.Fatalf("ParseRSS: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	for _, item := range items {
		if !strings.HasSuffix(item.TorrentURL, ".torrent") {
			t.Errorf("%q: torrent = %q", item.Title, item.TorrentURL)
		}
		if item.Size <= 0 {
			t.Errorf("%q: size = %d", item.Title, item.Size)
		}
	}
}

func TestTorrentsomeNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			serveFile(t, w, "torrentsome/home.html")
		case r.URL.Path == "/topic/index":
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			if got := r.PostFormValue("_csrf"); got == "" {
				t.Error("missing _csrf in search form")
			}
			if got := r.PostFormValue("keywords"); got != "matrix" {
				t.Errorf("keywords = %q", got)
			}
			serveFile(t, w, "torrentsome/search.html")
		default:
			serveFile(t, w, "torrentsome/detail.html")
		}
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "torrentsome", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	first := items[0]
	if first.Size <= 0 {
		t.Errorf("size = %d", first.Size)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:") {
		t.Errorf("magnet = %q", first.Magnet)
	}
}

func TestTorrenttipNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			serveFile(t, w, "torrenttip/home.html")
		case r.URL.Path == "/search":
			if got := r.URL.Query().Get("_token"); got == "" {
				t.Error("missing _token in search query")
			}
			if got := r.URL.Query().Get("q"); got != "matrix" {
				t.Errorf("q = %q", got)
			}
			serveFile(t, w, "torrenttip/search.html")
		default:
			serveFile(t, w, "torrenttip/detail.html")
		}
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "torrenttip", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	first := items[0]
	if first.Size <= 0 {
		t.Errorf("size = %d", first.Size)
	}
	if !strings.Contains(first.DownloadURL, "filesbest.xyz/download") {
		t.Errorf("download = %q", first.DownloadURL)
	}
}

func TestUIndexNativeFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search.php" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("search"); got != "matrix" {
			t.Errorf("search param = %q", got)
		}
		serveFile(t, w, "uindex/search.html")
	}))
	t.Cleanup(server.Close)

	connector := nativeConnector(t, "uindex", server.URL)
	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeSearch, Query: "matrix", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	first := items[0]
	if first.Size <= 0 || first.Seeders <= 0 {
		t.Errorf("size/seeders = %d/%d", first.Size, first.Seeders)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:") {
		t.Errorf("magnet = %q", first.Magnet)
	}
}
