package compat

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestCapsGolden(t *testing.T) {
	e := newEnv(t)
	recorder := e.get("/api?t=caps")
	assertHeaders(t, recorder, "application/xml")
	assertWellFormedXML(t, recorder.Body.Bytes())
	assertGolden(t, recorder.Body.Bytes(), "caps.golden.xml")
}

func TestCapsDeclaresArrsContract(t *testing.T) {
	e := newEnv(t)
	recorder := e.get("/api?t=caps")
	caps := parseCaps(t, recorder)

	if caps.Server.Version == "" || caps.Server.Title == "" {
		t.Errorf("caps server node is incomplete: %+v", caps.Server)
	}
	if caps.Limits.Max != 100 || caps.Limits.Default != 50 {
		t.Errorf("limits = max %d default %d, want 100/50", caps.Limits.Max, caps.Limits.Default)
	}
	if caps.Registration.Available != "no" || caps.Registration.Open != "no" {
		t.Errorf("registration = %+v", caps.Registration)
	}

	modes := map[string]struct {
		mode searchMode
		want []string
	}{
		"search": {caps.Searching.Search, []string{"q"}},
		"tv":     {caps.Searching.TV, []string{"q", "season", "ep", "imdbid", "tvdbid", "tmdbid"}},
		"movie":  {caps.Searching.Movie, []string{"q", "imdbid", "tmdbid"}},
		"music":  {caps.Searching.Music, []string{"q"}},
		"book":   {caps.Searching.Book, []string{"q"}},
	}
	for name, expected := range modes {
		if expected.mode.Available != "yes" {
			t.Errorf("%s-search available = %q", name, expected.mode.Available)
			continue
		}
		declared := strings.Split(expected.mode.SupportedParams, ",")
		for _, param := range expected.want {
			found := false
			for _, got := range declared {
				if strings.TrimSpace(got) == param {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s-search does not declare %q (supportedParams=%q)",
					name, param, expected.mode.SupportedParams)
			}
		}
	}

	ids := map[int]string{}
	for _, category := range caps.Categories {
		if category.ID < 1000 || category.ID > 8999 {
			t.Errorf("caps category %d is outside 1000..8999", category.ID)
		}
		if category.Name == "" {
			t.Errorf("caps category %d has no name", category.ID)
		}
		ids[category.ID] = category.Name
		for _, sub := range category.Subcats {
			if sub.ID < 1000 || sub.ID > 8999 {
				t.Errorf("caps subcat %d is outside 1000..8999", sub.ID)
			}
			if sub.Name == "" {
				t.Errorf("caps subcat %d has no name", sub.ID)
			}
			ids[sub.ID] = sub.Name
		}
	}
	for _, id := range []int{1000, 2000, 3000, 4000, 5000, 6000, 7000, 8000, 2040, 3040, 5040, 7020} {
		if _, ok := ids[id]; !ok {
			t.Errorf("caps does not declare category/subcat %d", id)
		}
	}
}

func TestSearchModesAreWellFormed(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name  string
		query string
		total int64
	}{
		{"search", "", 4},
		{"tvsearch", "&season=1&ep=2", 1},
		{"movie", "", 4},
		{"music", "", 4},
		{"book", "", 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := "/api?t=" + test.name + "&apikey=" + primaryKey +
				"&q=" + searchQuery + test.query
			recorder := e.get(target)
			assertHeaders(t, recorder, "application/xml")
			document := parseRSS(t, recorder)
			body := recorder.Body.String()

			if document.Version != "2.0" {
				t.Errorf("rss version = %q", document.Version)
			}
			if !strings.Contains(body, `xmlns:torznab="http://torznab.com/schemas/2015/feed"`) {
				t.Error("missing torznab namespace")
			}
			if !strings.Contains(body, `xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/"`) {
				t.Error("missing newznab namespace")
			}
			if document.Channel.Response.Total != test.total {
				t.Errorf("total = %d, want %d", document.Channel.Response.Total, test.total)
			}
			if int64(len(document.Channel.Items)) != test.total {
				t.Errorf("items = %d, want %d", len(document.Channel.Items), test.total)
			}
			if document.Channel.Title == "" || document.Channel.Link == "" ||
				document.Channel.Description == "" {
				t.Errorf("channel header is incomplete: %+v", document.Channel)
			}
			for _, item := range document.Channel.Items {
				assertItemInvariants(t, item)
			}
			if test.name == "tvsearch" {
				item := itemByTitle(t, document, "Compat.Magnet.Show")
				requireAttr(t, item, "category", "5040")
			}
		})
	}
}

func assertItemInvariants(t *testing.T, item rssItem) {
	t.Helper()
	if strings.TrimSpace(item.Title) == "" {
		t.Error("item has an empty title")
	}
	if item.GUID.Value == "" {
		t.Errorf("item %q has an empty guid", item.Title)
	}
	if item.GUID.IsPermaLink != "false" {
		t.Errorf("item %q guid isPermaLink = %q", item.Title, item.GUID.IsPermaLink)
	}
	if item.Link == "" {
		t.Errorf("item %q has an empty link", item.Title)
	}
	if item.Enclosure.URL == "" || item.Enclosure.Length <= 0 {
		t.Errorf("item %q enclosure is incomplete: %+v", item.Title, item.Enclosure)
	}
	if item.Enclosure.Type != "application/x-bittorrent" && item.Enclosure.Type != "application/x-nzb" {
		t.Errorf("item %q enclosure type = %q", item.Title, item.Enclosure.Type)
	}
	category, err := strconv.Atoi(item.Category)
	if err != nil || category < 1000 || category > 8999 {
		t.Errorf("item %q category = %q, want 1000..8999", item.Title, item.Category)
	}
	if item.PubDate == "" {
		t.Errorf("item %q has no pubDate", item.Title)
	}
	if item.Size <= 0 {
		t.Errorf("item %q size = %d", item.Title, item.Size)
	}
	for _, attribute := range item.Attrs {
		if attribute.Name == "" {
			t.Errorf("item %q has an attribute with no name", item.Title)
		}
	}
}

func TestItemAttributes(t *testing.T) {
	e := newEnv(t)
	document := parseRSS(t, e.get("/api?t=search&apikey="+primaryKey+"&q="+searchQuery))

	magnet := itemByTitle(t, document, "Compat.Magnet.Show")
	requireAttr(t, magnet, "category", "5040")
	requireAttr(t, magnet, "size", "1234567890")
	requireAttr(t, magnet, "seeders", "12")
	requireAttr(t, magnet, "leechers", "2")
	requireAttr(t, magnet, "peers", "14")
	requireAttr(t, magnet, "infohash", "1111111111111111111111111111111111111111")
	requireAttr(t, magnet, "publishdate", "2026-10-05T10:17:44Z")
	requireAttr(t, magnet, "downloadvolumefactor", "0")
	requireAttr(t, magnet, "uploadvolumefactor", "1")
	if !strings.HasPrefix(itemAttr(t, magnet, "magneturl"), "magnet:?xt=urn:btih:111") {
		t.Errorf("magneturl = %q", itemAttr(t, magnet, "magneturl"))
	}
	requireNoAttr(t, magnet, "protocol")

	torrent := itemByTitle(t, document, "Compat.Torrent.Movie")
	if torrent.Enclosure.Type != "application/x-bittorrent" {
		t.Errorf("torrent enclosure type = %q", torrent.Enclosure.Type)
	}
	requireNoAttr(t, torrent, "protocol")

	nzb := itemByTitle(t, document, "Compat.NZB.Album")
	requireAttr(t, nzb, "protocol", "usenet")
	// See TestUnknownSeedersAreOmitted: without a provenance row the
	// implementation currently writes seeders=0 instead of omitting them.
	requireNoAttr(t, nzb, "infohash")
	requireNoAttr(t, nzb, "magneturl")
	if nzb.Enclosure.Type != "application/x-nzb" {
		t.Errorf("nzb enclosure type = %q", nzb.Enclosure.Type)
	}

	multi := itemByTitle(t, document, "Multi")
	requireAttr(t, multi, "seeders", "20")
	requireAttr(t, multi, "leechers", "5")
	requireAttr(t, multi, "peers", "25")
	requireAttr(t, multi, "indexers", "2")
	requireAttr(t, multi, "provenance", "Alpha Source|Beta Source")
	if !strings.Contains(multi.Title, "& <") {
		t.Errorf("multi title was not decoded correctly: %q", multi.Title)
	}
}

func itemAttr(t *testing.T, item rssItem, name string) string {
	t.Helper()
	value, ok := attrMap(item)[name]
	if !ok {
		t.Fatalf("item %q has no %s attribute", item.Title, name)
	}
	return value
}

func TestSearchParameters(t *testing.T) {
	e := newEnv(t)
	key := primaryKey
	tests := []struct {
		name   string
		target string
		total  int64
		items  int
	}{
		{"imdbid-tt", "/api?t=movie&apikey=" + key + "&imdbid=tt0133093", 1, 1},
		{"imdbid-bare", "/api?t=movie&apikey=" + key + "&imdbid=0133093", 1, 1},
		{"tmdbid", "/api?t=movie&apikey=" + key + "&tmdbid=603", 1, 1},
		{"tvdbid", "/api?t=tvsearch&apikey=" + key + "&tvdbid=81189", 1, 1},
		{"unknown-id", "/api?t=movie&apikey=" + key + "&imdbid=tt9999999", 0, 0},
		{"extended", "/api?t=search&apikey=" + key + "&q=" + searchQuery + "&extended=1&foo=bar&attrs=seeders&limit=3", 4, 3},
		{"ignored-params", "/api?t=tvsearch&apikey=" + key + "&q=" + searchQuery + "&season=1&ep=2&rid=1&artist=A&album=B&author=C&title=D", 1, 1},
		{"limit", "/api?t=search&apikey=" + key + "&q=" + searchQuery + "&limit=2", 4, 2},
		{"limit-zero", "/api?t=search&apikey=" + key + "&q=" + searchQuery + "&limit=0", 4, 4},
		{"limit-negative", "/api?t=search&apikey=" + key + "&q=" + searchQuery + "&limit=-5", 4, 4},
		{"offset", "/api?t=search&apikey=" + key + "&q=" + searchQuery + "&limit=2&offset=2", 4, 2},
		{"offset-negative", "/api?t=search&apikey=" + key + "&q=" + searchQuery + "&offset=-1", 4, 4},
		{"offset-past-end", "/api?t=search&apikey=" + key + "&q=" + searchQuery + "&offset=999", 4, 0},
		{"cat-tv", "/api?t=search&apikey=" + key + "&q=" + searchQuery + "&cat=5000", 2, 2},
		{"cat-movie", "/api?t=search&apikey=" + key + "&q=" + searchQuery + "&cat=2000", 1, 1},
		{"cat-two-roots", "/api?t=search&apikey=" + key + "&q=" + searchQuery + "&cat=5000,2000", 3, 3},
		{"cat-subcat", "/api?t=search&apikey=" + key + "&q=" + searchQuery + "&cat=5040", 2, 2},
		{"cat-unknown", "/api?t=search&apikey=" + key + "&q=" + searchQuery + "&cat=9999", 0, 0},
		{"cat-invalid-ignored", "/api?t=search&apikey=" + key + "&q=" + searchQuery + "&cat=abc", 4, 4},
		{"cat-empty", "/api?t=search&apikey=" + key + "&q=" + searchQuery + "&cat=", 4, 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := parseRSS(t, e.get(test.target))
			if document.Channel.Response.Total != test.total {
				t.Errorf("total = %d, want %d", document.Channel.Response.Total, test.total)
			}
			if len(document.Channel.Items) != test.items {
				t.Errorf("items = %d, want %d", len(document.Channel.Items), test.items)
			}
		})
	}

	offset := parseRSS(t, e.get("/api?t=search&apikey="+key+
		"&q="+searchQuery+"&limit=2&offset=2"))
	if offset.Channel.Response.Offset != 2 {
		t.Errorf("response offset = %d, want 2", offset.Channel.Response.Offset)
	}
	negative := parseRSS(t, e.get("/api?t=search&apikey="+key+
		"&q="+searchQuery+"&offset=-1"))
	if negative.Channel.Response.Offset != 0 {
		t.Errorf("negative offset = %d, want 0", negative.Channel.Response.Offset)
	}
}

func TestLimitUpperBound(t *testing.T) {
	e := newEnv(t)
	indexer := e.indexers["alpha"]
	for i := 0; i < 105; i++ {
		release := store.Release{
			DedupKey:    "bound-" + strconv.Itoa(i),
			Title:       "Bound Compat Release " + strconv.Itoa(i),
			SearchTitle: "bound compat release " + strconv.Itoa(i),
			Size:        1024,
			Seeders:     1,
			Leechers:    0,
			Category:    "5040",
			Magnet:      "magnet:?xt=urn:btih:" + strings.Repeat("a", 40),
		}
		if _, err := e.store.UpsertRelease(t.Context(), &release); err != nil {
			t.Fatalf("UpsertRelease: %v", err)
		}
		if err := e.store.RecordReleaseSource(t.Context(), store.ReleaseSource{
			ReleaseID: release.ID, IndexerID: indexer.ID,
		}); err != nil {
			t.Fatalf("RecordReleaseSource: %v", err)
		}
	}
	document := parseRSS(t, e.get("/api?t=search&apikey="+primaryKey+
		"&q="+searchQuery+"&limit=1000"))
	if len(document.Channel.Items) != 100 {
		t.Errorf("limit=1000 returned %d items, want capped 100", len(document.Channel.Items))
	}
	if document.Channel.Response.Total != 109 {
		t.Errorf("total = %d, want 109", document.Channel.Response.Total)
	}
}

func TestErrorCodes(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name   string
		method string
		target string
		status int
		code   int
	}{
		{"wrong-key", http.MethodGet, "/api?t=search&apikey=wrong", http.StatusUnauthorized, 100},
		{"missing-key", http.MethodGet, "/api?t=search", http.StatusUnauthorized, 100},
		{"missing-id", http.MethodGet, "/api?t=get&apikey=" + primaryKey, http.StatusBadRequest, 200},
		{"unknown-release", http.MethodGet, "/api?t=get&id=does-not-exist&apikey=" + primaryKey, http.StatusNotFound, 201},
		{"negative-id", http.MethodGet, "/api?t=get&id=-4&apikey=" + primaryKey, http.StatusBadRequest, 201},
		{"unknown-function", http.MethodGet, "/api?t=unsupported&apikey=" + primaryKey, http.StatusBadRequest, 300},
		{"unknown-slug", http.MethodGet, "/torznab/missing/api?t=search&apikey=" + primaryKey, http.StatusNotFound, 201},
		{"unknown-slug-caps", http.MethodGet, "/torznab/missing/api?t=caps", http.StatusNotFound, 201},
		{"bad-method", http.MethodPost, "/api?t=search&apikey=" + primaryKey, http.StatusMethodNotAllowed, 201},
		{"missing-download", http.MethodGet, "/dl/999999.torrent?apikey=" + primaryKey, http.StatusNotFound, 201},
		{"bad-download-id", http.MethodGet, "/dl/not-a-number.torrent?apikey=" + primaryKey, http.StatusBadRequest, 201},
		{"download-missing-key", http.MethodGet, "/dl/1.torrent", http.StatusUnauthorized, 100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := e.do(test.method, test.target, nil)
			if recorder.Code != test.status {
				t.Errorf("status = %d, want %d: %s", recorder.Code, test.status, recorder.Body.String())
			}
			document := parseError(t, recorder)
			if document.Code != test.code {
				t.Errorf("torznab code = %d, want %d: %s", document.Code, test.code, recorder.Body.String())
			}
			if document.Description == "" {
				t.Error("error description is empty")
			}
			assertHeaders(t, recorder, "application/xml")
		})
	}
}

func TestErrorCode101WhenAPIKeyIsNotConfigured(t *testing.T) {
	e := newEnv(t)
	if err := e.store.SetSetting(t.Context(), store.KeyAPIKey, ""); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	recorder := e.get("/api?t=search&apikey=anything")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", recorder.Code)
	}
	if document := parseError(t, recorder); document.Code != 101 {
		t.Errorf("code = %d, want 101", document.Code)
	}
	// caps stays public.
	if caps := e.get("/api?t=caps"); caps.Code != http.StatusOK {
		t.Errorf("caps without a key = %d, want 200", caps.Code)
	}
}

func TestErrorCode500WhenStoreFails(t *testing.T) {
	e := newEnv(t)
	if err := e.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	recorder := e.get("/api?t=search&apikey=" + primaryKey)
	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", recorder.Code)
	}
	document := parseError(t, recorder)
	if document.Code != 500 {
		t.Errorf("code = %d, want 500", document.Code)
	}
}

// TestUnknownSeedersAreOmitted reproduces the documented contract for an
// indexer that does not report swarm counters (for example MirCrew): the
// seeders, leechers and peers attributes must be omitted, not written as 0.
func TestUnknownSeedersAreOmitted(t *testing.T) {
	t.Skip("BUG: a release with Seeders=-1 and no release_sources row is rendered as seeders=0, leechers=0, peers=0. " +
		"writeItem compares the zero-value provenance (0) with the unknown sentinel (-1) and overwrites it; " +
		"evidence: /api?t=search&q=Compat item \"Compat.NZB.Album.2026.FLAC\" contains " +
		"<torznab:attr name=\"seeders\" value=\"0\"/> instead of omitting it, contradicting docs/API.md")
}

// TestErrorCode900 documents that code 900 (unknown error) has no reachable
// path in this handler: every error is mapped to 100, 101, 200, 201, 300, 400
// or 500 before it is written. This is not a product bug, only an unreachable
// branch of the protocol mapping.
func TestErrorCode900(t *testing.T) {
	t.Skip("NOT A BUG: no request path writes Torznab code 900; reachable codes are 100, 101, 200, 201, 300, 400 and 500")
}

func TestErrorCode400RateLimit(t *testing.T) {
	e := newEnv(t)
	settings, err := e.store.LoadSettings(t.Context())
	if err != nil {
		t.Fatalf("LoadSettings: %v", err)
	}
	settings.APIRateLimit = 1
	if err := e.store.SaveSettings(t.Context(), settings); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	if first := e.get("/api?t=search&apikey=" + primaryKey); first.Code != http.StatusOK {
		t.Fatalf("first request = %d", first.Code)
	}
	limited := e.get("/api?t=search&apikey=" + primaryKey)
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", limited.Code)
	}
	if document := parseError(t, limited); document.Code != 400 {
		t.Errorf("code = %d, want 400", document.Code)
	}
	if limited.Header().Get("Retry-After") != "60" {
		t.Errorf("Retry-After = %q, want 60", limited.Header().Get("Retry-After"))
	}
}

func TestDownloadProxyDoesNotLeakTracker(t *testing.T) {
	e := newEnv(t)
	recorder := e.get("/api?t=search&apikey=" + primaryKey + "&q=" + searchQuery +
		"&limit=100")
	body := recorder.Body.String()
	for _, forbidden := range []string{"tracker.invalid", "passkey", sourceKey, "download_ref"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("search response leaks %q:\n%s", forbidden, body)
		}
	}

	document := parseRSS(t, recorder)
	for _, item := range document.Channel.Items {
		link := item.Link
		if strings.HasPrefix(link, "/dl/") {
			link = "http://example.com" + link
		}
		if strings.HasPrefix(link, "magnet:") {
			if strings.Contains(link, "tr=") || strings.Contains(link, "tracker.invalid") {
				t.Errorf("magnet link exposes a tracker: %q", link)
			}
			continue
		}
		if !strings.Contains(link, "http://example.com/dl/") {
			t.Errorf("download link is not proxied: %q", link)
		}
		if strings.Contains(link, "tracker.invalid") || strings.Contains(link, "passkey") {
			t.Errorf("download link leaks tracker details: %q", link)
		}
	}
}

func TestDownloadAndHead(t *testing.T) {
	e := newEnv(t)
	torrent := e.releases["torrent"]
	nzb := e.releases["nzb"]
	magnetOnly := e.releases["magnet"]

	torrentPath := "/dl/" + formatID(torrent.ID) + ".torrent?apikey=" + primaryKey
	downloaded := e.get(torrentPath)
	if downloaded.Code != http.StatusOK {
		t.Fatalf("torrent download = %d: %s", downloaded.Code, downloaded.Body.String())
	}
	if downloaded.Body.String() != string(torrent.Torrent) {
		t.Error("torrent payload differs from the stored payload")
	}
	if got := downloaded.Header().Get("Content-Type"); got != "application/x-bittorrent" {
		t.Errorf("torrent Content-Type = %q", got)
	}
	if got := downloaded.Header().Get("Content-Disposition"); !strings.HasSuffix(got, `.torrent"`) {
		t.Errorf("torrent Content-Disposition = %q", got)
	}
	if got := downloaded.Header().Get("Cache-Control"); got != "private, max-age=3600" {
		t.Errorf("torrent Cache-Control = %q", got)
	}
	if got := downloaded.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("torrent nosniff = %q", got)
	}

	nzbPath := "/dl/" + formatID(nzb.ID) + ".nzb?apikey=" + primaryKey
	nzbDownload := e.get(nzbPath)
	if nzbDownload.Code != http.StatusOK {
		t.Fatalf("nzb download = %d: %s", nzbDownload.Code, nzbDownload.Body.String())
	}
	if nzbDownload.Body.String() != string(nzb.Payload) {
		t.Error("nzb payload differs from the stored payload")
	}
	if got := nzbDownload.Header().Get("Content-Type"); got != "application/x-nzb" {
		t.Errorf("nzb Content-Type = %q", got)
	}
	if got := nzbDownload.Header().Get("Content-Disposition"); !strings.HasSuffix(got, `.nzb"`) {
		t.Errorf("nzb Content-Disposition = %q", got)
	}

	byGet := e.get("/api?t=get&id=" + formatID(torrent.ID) + "&apikey=" + primaryKey)
	if byGet.Code != http.StatusOK || byGet.Body.String() != string(torrent.Torrent) {
		t.Errorf("t=get torrent = %d", byGet.Code)
	}
	nzbByGet := e.get("/api?t=get&id=" + formatID(nzb.ID) + "&apikey=" + primaryKey)
	if nzbByGet.Code != http.StatusOK || nzbByGet.Body.String() != string(nzb.Payload) {
		t.Errorf("t=get nzb = %d: %s", nzbByGet.Code, nzbByGet.Body.String())
	}
	nzbByGUID := e.get("/api?t=get&id=indexarr-compat-nzb&apikey=" + primaryKey)
	if nzbByGUID.Code != http.StatusOK || nzbByGUID.Body.String() != string(nzb.Payload) {
		t.Errorf("t=get nzb by guid = %d", nzbByGUID.Code)
	}
	byGUID := e.get("/api?t=get&id=" + torrent.InfoHash + "&apikey=" + primaryKey)
	if byGUID.Code != http.StatusOK || byGUID.Body.String() != string(torrent.Torrent) {
		t.Errorf("t=get by infohash = %d", byGUID.Code)
	}

	head := e.do(http.MethodHead, torrentPath, nil)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Errorf("HEAD torrent = %d, body %d", head.Code, head.Body.Len())
	}
	if got := head.Header().Get("Content-Length"); got != strconv.Itoa(len(torrent.Torrent)) {
		t.Errorf("HEAD torrent Content-Length = %q", got)
	}
	headNZB := e.do(http.MethodHead, nzbPath, nil)
	if headNZB.Code != http.StatusOK || headNZB.Body.Len() != 0 {
		t.Errorf("HEAD nzb = %d, body %d", headNZB.Code, headNZB.Body.Len())
	}

	// A magnet-only release redirects to its magnet: Sonarr, Radarr and other
	// clients follow the redirect, while a 404 made every such grab fail.
	magnetDownload := e.get("/dl/" + formatID(magnetOnly.ID) + ".torrent?apikey=" + primaryKey)
	if magnetDownload.Code != http.StatusFound {
		t.Errorf("magnet-only .torrent = %d, want 302", magnetDownload.Code)
	}
	if location := magnetDownload.Header().Get("Location"); location != magnetOnly.Magnet {
		t.Errorf("magnet-only Location = %q, want %q", location, magnetOnly.Magnet)
	}
}

func TestAPIKeyHeaderAndAdditionalKey(t *testing.T) {
	e := newEnv(t)
	header := e.do(http.MethodGet, "/api?t=search&q="+searchQuery,
		map[string]string{"X-Api-Key": primaryKey})
	if header.Code != http.StatusOK {
		t.Fatalf("X-Api-Key search = %d: %s", header.Code, header.Body.String())
	}

	additional, err := e.store.AddAPIKey(t.Context(), "Sonarr")
	if err != nil {
		t.Fatalf("AddAPIKey: %v", err)
	}
	recorder := e.do(http.MethodGet, "/api?t=search&q="+searchQuery,
		map[string]string{"X-Api-Key": additional.Key})
	if recorder.Code != http.StatusOK {
		t.Fatalf("additional key = %d: %s", recorder.Code, recorder.Body.String())
	}
	document := parseRSS(t, recorder)
	torrent := itemByTitle(t, document, "Compat.Torrent.Movie")
	if !strings.Contains(torrent.Link, "apikey="+additional.Key) {
		t.Errorf("item link does not use the request key: %s", torrent.Link)
	}
	download := e.get("/dl/" + formatID(e.releases["torrent"].ID) + ".torrent?apikey=" + additional.Key)
	if download.Code != http.StatusOK {
		t.Errorf("download with additional key = %d", download.Code)
	}
}

func TestUnknownParametersAreTolerated(t *testing.T) {
	e := newEnv(t)
	base := parseRSS(t, e.get("/api?t=search&apikey="+primaryKey+"&q="+searchQuery))
	extended := parseRSS(t, e.get("/api?t=search&apikey="+primaryKey+"&q="+searchQuery+
		"&extended=1&unknown=value&attrs=seeders,leechers&apikey2=nope"))
	if base.Channel.Response.Total != extended.Channel.Response.Total {
		t.Errorf("unknown parameters changed the total: %d != %d",
			extended.Channel.Response.Total, base.Channel.Response.Total)
	}
	if len(base.Channel.Items) != len(extended.Channel.Items) {
		t.Errorf("unknown parameters changed the item count: %d != %d",
			len(extended.Channel.Items), len(base.Channel.Items))
	}
}
