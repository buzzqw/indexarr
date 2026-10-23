// Package compat is the compatibility test suite for indexarr 2. It exercises
// the public Torznab/Newznab surface and the Servarr indexer payloads the way
// Sonarr, Radarr, Lidarr, Readarr and Whisparr consume them.
//
// The suite only depends on exported APIs and on testdata fixtures. It never
// modifies production packages and never performs network calls: every HTTP
// exchange uses httptest.
package compat

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/textutil"
	"github.com/buzzqw/indexarr/internal/torznabapi"
)

const (
	primaryKey  = "compat-secret-key"
	sourceKey   = "source-secret"
	trackerURL  = "http://tracker.invalid/api?apikey=" + sourceKey + "&passkey=deadbeef"
	searchQuery = "Compat"
)

// TestMain pins the timezone so golden XML containing pubDate is stable. Each
// package runs in its own test binary, so this does not leak into other tests.
func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

type fixtureRelease struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Category    string `json:"category"`
	Size        int64  `json:"size"`
	Seeders     int64  `json:"seeders"`
	Leechers    int64  `json:"leechers"`
	Published   string `json:"published"`
	Source      string `json:"source"`
	InfoHash    string `json:"infohash"`
	Magnet      string `json:"magnet"`
	Torrent     string `json:"torrent"`
	Protocol    string `json:"protocol"`
	Payload     string `json:"payload"`
	PayloadType string `json:"payload_type"`
	GUID        string `json:"guid"`
	IMDbID      string `json:"imdbid"`
	TMDbID      string `json:"tmdbid"`
	TVDbID      string `json:"tvdbid"`
	Multi       bool   `json:"multi"`
}

type fixtureFile struct {
	Releases []fixtureRelease `json:"releases"`
}

// env is one isolated indexarr store plus its Torznab handler.
type env struct {
	t        *testing.T
	store    *store.Store
	server   *torznabapi.Server
	sources  map[string]store.Source
	indexers map[string]store.Indexer
	releases map[string]store.Release
}

func loadFixtures(t *testing.T) fixtureFile {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "canonical.json"))
	if err != nil {
		t.Fatalf("read canonical fixture: %v", err)
	}
	var fixtures fixtureFile
	if err := json.Unmarshal(body, &fixtures); err != nil {
		t.Fatalf("decode canonical fixture: %v", err)
	}
	if len(fixtures.Releases) != 4 {
		t.Fatalf("canonical fixture has %d releases, want 4", len(fixtures.Releases))
	}
	return fixtures
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newEnv builds the canonical environment: two legacy sources, the matching
// native indexers and the four fixture releases (magnet, torrent, nzb,
// multi-indexer).
func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "compat.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SetSetting(ctx, store.KeyAPIKey, primaryKey); err != nil {
		t.Fatalf("set API key: %v", err)
	}
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID:            "compat-def",
		SchemaVersion: 1,
		Family:        "torznab",
		State:         "verificata",
		Origin:        "local",
		Author:        "compat",
		Body:          "schema: 1\nconnector: torznab\n",
		Hash:          strings.Repeat("c", 64),
	}); err != nil {
		t.Fatalf("UpsertDefinition: %v", err)
	}

	e := &env{
		t:        t,
		store:    st,
		sources:  map[string]store.Source{},
		indexers: map[string]store.Indexer{},
		releases: map[string]store.Release{},
	}
	for _, name := range []string{"Alpha Source", "Beta Source"} {
		suffix := strings.ToLower(strings.Fields(name)[0])
		id, err := st.CreateSource(ctx, store.Source{
			Name:    name,
			Kind:    "torznab",
			Manager: "torznab",
			URL:     trackerURL,
			APIKey:  sourceKey,
			Enabled: true,
		})
		if err != nil {
			t.Fatalf("CreateSource(%s): %v", name, err)
		}
		source, err := st.GetSource(ctx, id)
		if err != nil {
			t.Fatalf("GetSource(%s): %v", name, err)
		}
		e.sources[suffix] = source
	}

	indexerSpecs := []struct {
		slug     string
		name     string
		protocol string
		tags     string
		source   string
	}{
		{"alpha", "Alpha Source", "torrent", "movies,hd", "alpha"},
		{"beta", "Beta Source", "usenet", "tv", "beta"},
	}
	for _, spec := range indexerSpecs {
		id, err := st.CreateIndexer(ctx, store.Indexer{
			DefinitionID:   "compat-def",
			Protocol:       spec.protocol,
			Slug:           spec.slug,
			Name:           spec.name,
			Enabled:        true,
			Priority:       10,
			Tags:           spec.tags,
			LegacySourceID: e.sources[spec.source].ID,
		})
		if err != nil {
			t.Fatalf("CreateIndexer(%s): %v", spec.slug, err)
		}
		indexer, err := st.GetIndexer(ctx, id)
		if err != nil {
			t.Fatalf("GetIndexer(%s): %v", spec.slug, err)
		}
		e.indexers[spec.slug] = indexer
	}

	fixtures := loadFixtures(t)
	for _, fixture := range fixtures.Releases {
		source := e.sources[fixture.Source]
		published, err := time.Parse(time.RFC3339, fixture.Published)
		if err != nil {
			t.Fatalf("fixture %s published: %v", fixture.Key, err)
		}
		protocol := fixture.Protocol
		if protocol == "" {
			protocol = "torrent"
		}
		release := store.Release{
			DedupKey:    "compat-" + fixture.Key,
			InfoHash:    fixture.InfoHash,
			IMDbID:      textutil.NormalizeID(fixture.IMDbID),
			TMDbID:      textutil.NormalizeID(fixture.TMDbID),
			TVDbID:      textutil.NormalizeID(fixture.TVDbID),
			Title:       fixture.Title,
			SearchTitle: store.NormalizeSearch(fixture.Title),
			Size:        fixture.Size,
			Seeders:     fixture.Seeders,
			Leechers:    fixture.Leechers,
			Category:    fixture.Category,
			PublishedAt: published,
			SourceID:    source.ID,
			SourceName:  source.Name,
			GUID:        fixture.GUID,
			Magnet:      fixture.Magnet,
			Torrent:     []byte(fixture.Torrent),
			Protocol:    protocol,
			Payload:     []byte(fixture.Payload),
			PayloadType: fixture.PayloadType,
			FirstSeenAt: published,
			LastSeenAt:  published,
		}
		if _, err := st.UpsertRelease(ctx, &release); err != nil {
			t.Fatalf("UpsertRelease(%s): %v", fixture.Key, err)
		}
		e.releases[fixture.Key] = release
	}

	// The multi-indexer fixture is seen by both indexers. Alpha carries a
	// tracker download reference with a passkey: it must never reach the XML.
	multi := e.releases["multi"]
	for _, spec := range []struct {
		indexer  store.Indexer
		seeders  int64
		leechers int64
		ref      string
	}{
		{e.indexers["alpha"], 20, 5, "http://tracker.invalid/dl/42?passkey=deadbeef"},
		{e.indexers["beta"], 14, 2, ""},
	} {
		if err := st.RecordReleaseSource(ctx, store.ReleaseSource{
			ReleaseID:   multi.ID,
			IndexerID:   spec.indexer.ID,
			FirstSeenAt: multi.PublishedAt,
			LastSeenAt:  multi.PublishedAt,
			Seeders:     spec.seeders,
			Leechers:    spec.leechers,
			DownloadRef: spec.ref,
		}); err != nil {
			t.Fatalf("RecordReleaseSource(%s): %v", spec.indexer.Slug, err)
		}
	}

	e.server = torznabapi.New(st, discardLogger(), "test")
	return e
}

func (e *env) do(method, target string, headers map[string]string) *httptest.ResponseRecorder {
	e.t.Helper()
	request := httptest.NewRequest(method, target, nil)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	e.server.ServeHTTP(recorder, request)
	return recorder
}

func (e *env) get(target string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodGet, target, nil)
}

type rssDocument struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel channel  `xml:"channel"`
}

type channel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Response    response  `xml:"response"`
	Items       []rssItem `xml:"item"`
}

type response struct {
	Offset int   `xml:"offset,attr"`
	Total  int64 `xml:"total,attr"`
}

type rssItem struct {
	Title     string    `xml:"title"`
	GUID      guid      `xml:"guid"`
	Link      string    `xml:"link"`
	Comments  string    `xml:"comments"`
	PubDate   string    `xml:"pubDate"`
	Size      int64     `xml:"size"`
	Category  string    `xml:"category"`
	Enclosure enclosure `xml:"enclosure"`
	Attrs     []attr    `xml:"attr"`
}

type guid struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type enclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type attr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type capsDocument struct {
	XMLName xml.Name `xml:"caps"`
	Server  struct {
		Version string `xml:"version,attr"`
		Title   string `xml:"title,attr"`
	} `xml:"server"`
	Limits struct {
		Max     int `xml:"max,attr"`
		Default int `xml:"default,attr"`
	} `xml:"limits"`
	Registration struct {
		Available string `xml:"available,attr"`
		Open      string `xml:"open,attr"`
	} `xml:"registration"`
	Searching struct {
		Search searchMode `xml:"search"`
		TV     searchMode `xml:"tv-search"`
		Movie  searchMode `xml:"movie-search"`
		Music  searchMode `xml:"music-search"`
		Book   searchMode `xml:"book-search"`
	} `xml:"searching"`
	Categories []capsCategory `xml:"categories>category"`
}

type searchMode struct {
	Available       string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr"`
}

type capsCategory struct {
	ID      int          `xml:"id,attr"`
	Name    string       `xml:"name,attr"`
	Subcats []capsSubcat `xml:"subcat"`
}

type capsSubcat struct {
	ID   int    `xml:"id,attr"`
	Name string `xml:"name,attr"`
}

func parseRSS(t *testing.T, recorder *httptest.ResponseRecorder) rssDocument {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var document rssDocument
	if err := xml.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("response is not well-formed XML: %v\n%s", err, recorder.Body.String())
	}
	return document
}

func parseCaps(t *testing.T, recorder *httptest.ResponseRecorder) capsDocument {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("caps status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var document capsDocument
	if err := xml.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("caps is not well-formed XML: %v\n%s", err, recorder.Body.String())
	}
	return document
}

type errorDocument struct {
	XMLName     xml.Name `xml:"error"`
	Code        int      `xml:"code,attr"`
	Description string   `xml:"description,attr"`
}

func parseError(t *testing.T, recorder *httptest.ResponseRecorder) errorDocument {
	t.Helper()
	var document errorDocument
	if err := xml.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		t.Fatalf("error response is not well-formed XML: %v\n%s", err, recorder.Body.String())
	}
	return document
}

func attrMap(item rssItem) map[string]string {
	out := make(map[string]string, len(item.Attrs))
	for _, attribute := range item.Attrs {
		out[attribute.Name] = attribute.Value
	}
	return out
}

func requireAttr(t *testing.T, item rssItem, name, want string) {
	t.Helper()
	got, ok := attrMap(item)[name]
	if !ok {
		t.Errorf("item %q has no %s attribute", item.Title, name)
		return
	}
	if got != want {
		t.Errorf("item %q %s = %q, want %q", item.Title, name, got, want)
	}
}

func requireNoAttr(t *testing.T, item rssItem, name string) {
	t.Helper()
	if value, ok := attrMap(item)[name]; ok {
		t.Errorf("item %q unexpectedly has %s=%q", item.Title, name, value)
	}
}

func xmlEscape(value string) string {
	var builder strings.Builder
	_ = xml.EscapeText(&builder, []byte(value))
	return builder.String()
}

func formatID(id int64) string {
	return strconv.FormatInt(id, 10)
}

func assertHeaders(t *testing.T, recorder *httptest.ResponseRecorder, contentType string) {
	t.Helper()
	got := recorder.Header().Get("Content-Type")
	if !strings.Contains(got, contentType) {
		t.Errorf("Content-Type = %q, want %q", got, contentType)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func assertWellFormedXML(t *testing.T, body []byte) {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(string(body)))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("not well-formed XML: %v\n%s", err, body)
		}
	}
}

func assertGolden(t *testing.T, got []byte, name string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", name, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run UPDATE_GOLDEN=1 go test ./internal/compat -run TestGolden)", name, err)
	}
	if string(got) != string(want) {
		t.Errorf("%s mismatch:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func itemByTitle(t *testing.T, document rssDocument, fragment string) rssItem {
	t.Helper()
	for _, item := range document.Channel.Items {
		if strings.Contains(item.Title, fragment) {
			return item
		}
	}
	t.Fatalf("no item contains %q", fragment)
	return rssItem{}
}
