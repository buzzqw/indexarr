package engine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
)

type definitionClock struct {
	mu  sync.Mutex
	now time.Time
}

func newDefinitionClock() *definitionClock {
	return &definitionClock{now: time.Date(2026, time.October, 23, 12, 0, 0, 0, time.UTC)}
}

func (c *definitionClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *definitionClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "fixtures", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

func loadTestDefinition(t *testing.T, name, baseURL string) *definition.Definition {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", "definitions", name))
	if err != nil {
		t.Fatalf("read definition %s: %v", name, err)
	}
	source := strings.ReplaceAll(string(body), "http://REPLACE", baseURL)
	parsed, err := definition.Parse([]byte(source))
	if err != nil {
		t.Fatalf("parse definition %s: %v", name, err)
	}
	return parsed
}

func newDefinitionConnector(t *testing.T, name, baseURL string, settings map[string]string, clock Clock) *DefinitionConnector {
	t.Helper()
	def := loadTestDefinition(t, name, baseURL)
	transport := NewTransport(TransportConfig{AllowPrivate: true, Clock: clock})
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   settings,
		Transport:  transport,
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	return connector
}

func writeBody(t *testing.T, w http.ResponseWriter, body []byte) {
	t.Helper()
	if _, err := w.Write(body); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func TestDefinitionConnectorFormLoginCookiesAndSearch(t *testing.T) {
	loginPage := fixture(t, "login.html")
	loginOK := fixture(t, "login_ok.html")
	loginFailed := fixture(t, "login_failed.html")
	searchPage := fixture(t, "search.html")
	var loginPosts atomic.Int32
	var searchCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			if r.Method == http.MethodGet {
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "prelogin", Path: "/"})
				writeBody(t, w, loginPage)
				return
			}
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			if cookie, err := r.Cookie("session"); err != nil || cookie.Value != "prelogin" {
				t.Errorf("login POST missing session cookie: %v", err)
			}
			if r.FormValue("csrf_token") != "csrf-value-123" {
				t.Errorf("csrf_token = %q", r.FormValue("csrf_token"))
			}
			if r.FormValue("username") != "alice" || r.FormValue("password") != "secret" {
				w.WriteHeader(http.StatusOK)
				writeBody(t, w, loginFailed)
				return
			}
			loginPosts.Add(1)
			http.SetCookie(w, &http.Cookie{Name: "auth", Value: "yes", Path: "/"})
			writeBody(t, w, loginOK)
		case "/search":
			if cookie, err := r.Cookie("auth"); err != nil || cookie.Value != "yes" {
				http.Error(w, "not logged in", http.StatusForbidden)
				return
			}
			if searchCalls.Add(1) == 1 {
				if got := r.URL.Query().Get("cat"); got != "5" {
					t.Errorf("first cat = %q, want 5", got)
				}
				if got := r.URL.Query().Get("page"); got != "1" {
					t.Errorf("first page = %q, want 1", got)
				}
				if got := r.URL.Query().Get("limit"); got != "30" {
					t.Errorf("first limit = %q, want 30", got)
				}
			}
			writeBody(t, w, searchPage)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	clock := newDefinitionClock()
	connector := newDefinitionConnector(t, "form_login.yaml", server.URL, map[string]string{
		"username": "alice",
		"password": "secret",
	}, clock)

	items, err := connector.Search(context.Background(), SearchRequest{
		Mode:       ModeSearch,
		Query:      "ubuntu",
		Categories: []int{5040},
		Limit:      30,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	item := items[0]
	if item.Title != "Ubuntu 24.04" {
		t.Errorf("title = %q", item.Title)
	}
	if item.Size != 1_500_000_000 {
		t.Errorf("size = %d", item.Size)
	}
	if item.Seeders != 1234 {
		t.Errorf("seeders = %d", item.Seeders)
	}
	if len(item.Categories) != 1 || item.Categories[0] != 5040 {
		t.Errorf("categories = %v", item.Categories)
	}
	if item.DownloadURL != server.URL+"/download/1.torrent" {
		t.Errorf("download = %q", item.DownloadURL)
	}
	if item.DetailsURL != server.URL+"/details/1" {
		t.Errorf("details = %q", item.DetailsURL)
	}
	if item.Published.UTC().Format("2006-01-02 15:04") != "2026-01-02 15:04" {
		t.Errorf("published = %s", item.Published)
	}
	if item.Protocol != ProtocolTorrent {
		t.Errorf("protocol = %q", item.Protocol)
	}

	// Session cookies must be reused: the second search logs in only once.
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "ubuntu"}); err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if got := loginPosts.Load(); got != 1 {
		t.Errorf("login posts = %d, want 1", got)
	}

	// refresh_after is 1h: after two hours a new login is required.
	clock.Advance(2 * time.Hour)
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "ubuntu"}); err != nil {
		t.Fatalf("third Search: %v", err)
	}
	if got := loginPosts.Load(); got != 2 {
		t.Errorf("login posts after expiry = %d, want 2", got)
	}
}

func TestDefinitionConnectorPastedCookieSkipsLoginAndSendsHeader(t *testing.T) {
	searchPage := fixture(t, "search.html")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("unexpected request %s %s; a cookie strategy must not visit a login page", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		if got := r.Header.Get("Cookie"); got != "sid=abc; uid=42" {
			t.Errorf("Cookie header = %q", got)
		}
		writeBody(t, w, searchPage)
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "cookie_login.yaml", server.URL,
		map[string]string{"cookie": " Cookie: sid=abc; uid=42 \n"}, newDefinitionClock())
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "ubuntu"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) == 0 {
		t.Fatal("search returned no items")
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestDefinitionConnectorLoginFailureHidesCredentials(t *testing.T) {
	loginPage := fixture(t, "login.html")
	failedPage := fixture(t, "login_failed.html")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			if r.Method == http.MethodGet {
				writeBody(t, w, loginPage)
				return
			}
			writeBody(t, w, failedPage)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "form_login.yaml", server.URL, map[string]string{
		"username": "alice",
		"password": "super-secret-password",
	}, newDefinitionClock())

	_, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"})
	if err == nil {
		t.Fatal("Search succeeded, want login failure")
	}
	if !strings.Contains(err.Error(), "login failed: Invalid credentials") {
		t.Errorf("error = %q", err)
	}
	if strings.Contains(err.Error(), "super-secret-password") {
		t.Errorf("error leaked the password: %q", err)
	}
}

func TestDefinitionConnectorPaginationDedupAndMaxPages(t *testing.T) {
	pages := map[string][]byte{
		"1": fixture(t, "pagination_1.html"),
		"2": fixture(t, "pagination_2.html"),
		"3": fixture(t, "pagination_3.html"),
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		page := r.URL.Query().Get("p")
		body, ok := pages[page]
		if !ok {
			writeBody(t, w, pages["3"])
			return
		}
		writeBody(t, w, body)
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "pagination.yaml", server.URL, nil, newDefinitionClock())
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3 (one duplicate removed)", len(items))
	}
	if items[0].Title != "Alpha" || items[1].Title != "Bravo" || items[2].Title != "Charlie" {
		t.Errorf("titles = %q, %q, %q", items[0].Title, items[1].Title, items[2].Title)
	}
	if got := requests.Load(); got != 3 {
		t.Errorf("page requests = %d, want 3 (empty page stops)", got)
	}

	// max_pages caps the walk even when pages keep returning rows.
	def := loadTestDefinition(t, "pagination.yaml", server.URL)
	def.Search.Pagination.MaxPages = 2
	transport := NewTransport(TransportConfig{AllowPrivate: true})
	capped, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: "capped"},
		Definition: def,
		Transport:  transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	requests.Store(0)
	items, err = capped.Search(context.Background(), SearchRequest{Mode: ModeSearch})
	if err != nil {
		t.Fatalf("capped Search: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("capped items = %d, want 3", len(items))
	}
	if got := requests.Load(); got != 2 {
		t.Errorf("capped page requests = %d, want 2", got)
	}
}

func TestDefinitionConnectorHTMLFallbacksAndTypes(t *testing.T) {
	fieldsPage := fixture(t, "fields.html")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeBody(t, w, fieldsPage)
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "html_fields.yaml", server.URL, nil, newDefinitionClock())
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (required rows skipped)", len(items))
	}
	item := items[0]
	if item.Title != "Valid Release" {
		t.Errorf("title = %q", item.Title)
	}
	if item.Size != 1_500_000_000 {
		t.Errorf("size = %d", item.Size)
	}
	if item.Seeders != 1234 {
		t.Errorf("seeders = %d", item.Seeders)
	}
	if len(item.Categories) != 1 || item.Categories[0] != 5040 {
		t.Errorf("categories = %v", item.Categories)
	}
	if item.InfoHash != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("infohash = %q", item.InfoHash)
	}
	if item.Published.UTC().Format("2006-01-02 15:04") != "2026-01-02 15:04" {
		t.Errorf("published = %s", item.Published)
	}
}

func TestDefinitionConnectorJSONExtraction(t *testing.T) {
	apiBody := fixture(t, "api.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeBody(t, w, apiBody)
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "json_api.yaml", server.URL, nil, newDefinitionClock())
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	item := items[0]
	if item.Title != "JSON Release" {
		t.Errorf("title = %q", item.Title)
	}
	if item.Size != 1048576 || item.Seeders != 42 {
		t.Errorf("size/seeders = %d/%d", item.Size, item.Seeders)
	}
	if item.DownloadURL != server.URL+"/files/1.torrent" {
		t.Errorf("download = %q", item.DownloadURL)
	}
	if item.DetailsURL != server.URL+"/files/1.nfo" {
		t.Errorf("details = %q", item.DetailsURL)
	}
	if len(item.Categories) != 1 || item.Categories[0] != 5040 {
		t.Errorf("categories = %v", item.Categories)
	}
	if item.Published.IsZero() {
		t.Error("published is zero")
	}
}

func TestDefinitionConnectorXMLExtraction(t *testing.T) {
	rssBody := fixture(t, "rss.xml")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		writeBody(t, w, rssBody)
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "xml_rss.yaml", server.URL, nil, newDefinitionClock())
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	item := items[0]
	if item.Title != "XML Release" {
		t.Errorf("title = %q", item.Title)
	}
	if item.DownloadURL != server.URL+"/download/xml-1.torrent" {
		t.Errorf("download = %q", item.DownloadURL)
	}
	if item.DetailsURL != server.URL+"/details/xml-1" {
		t.Errorf("details = %q", item.DetailsURL)
	}
	if item.Size != 1048576 {
		t.Errorf("size = %d", item.Size)
	}
	if len(item.Categories) != 1 || item.Categories[0] != 5040 {
		t.Errorf("categories = %v", item.Categories)
	}
	if item.InfoHash != "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" {
		t.Errorf("infohash = %q", item.InfoHash)
	}
	if item.Published.IsZero() {
		t.Error("published is zero")
	}
}

func TestTemplateRendering(t *testing.T) {
	values := newTemplateValues()
	values.settings["username"] = "alice"
	values.declaredSettings["username"] = struct{}{}
	values.query = queryTemplateMap(SearchRequest{Query: "ubuntu", Year: 2026})
	values.categories = []string{"5", "12"}
	values.scalars["page"] = "2"

	rendered, err := values.render(`{{ settings.username }}|{{ query.keywords }}|{{ query.season }}|{{ query.year }}|{{ categories }}|{{ categories | join(",") }}|{{ page }}`)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	want := `alice|ubuntu||2026|5,12|5,12|2`
	if rendered != want {
		t.Errorf("rendered = %q, want %q", rendered, want)
	}

	for _, source := range []string{
		`{{ nope }}`,
		`{{ settings.missing }}`,
		`{{ query.missing }}`,
		`{{ categories | upper }}`,
		`{{ query.keywords | join(",") }}`,
	} {
		if _, err := values.render(source); err == nil {
			t.Errorf("render(%q) succeeded, want error", source)
		}
	}
}

func TestDefinitionConnectorPostFormAndURLPrefix(t *testing.T) {
	searchPage := fixture(t, "search.html")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/base/search" {
			t.Errorf("path = %q, want /base/search", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if got := r.URL.Query().Get("q"); got != "ubuntu" {
			t.Errorf("q = %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got := r.FormValue("sort"); got != "seeders" {
			t.Errorf("sort = %q (default setting not applied)", got)
		}
		if got := r.FormValue("page"); got != "1" {
			t.Errorf("page = %q", got)
		}
		if got := r.Header.Get("X-Indexarr-Test"); got != "seeders" {
			t.Errorf("header = %q", got)
		}
		writeBody(t, w, searchPage)
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "prefix_post.yaml", server.URL, nil, newDefinitionClock())
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "ubuntu"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("items = %d, want 1", len(items))
	}
}

func TestDefinitionConnectorOffsetPagination(t *testing.T) {
	pages := map[string][]byte{
		"0": fixture(t, "pagination_1.html"),
		"2": fixture(t, "pagination_2.html"),
		"4": fixture(t, "pagination_3.html"),
	}
	var offsets []string
	var pagesSeen []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		offsets = append(offsets, r.URL.Query().Get("offset"))
		pagesSeen = append(pagesSeen, r.URL.Query().Get("page"))
		mu.Unlock()
		if got := r.URL.Query().Get("limit"); got != "10" {
			t.Errorf("limit = %q, want 10", got)
		}
		writeBody(t, w, pages[r.URL.Query().Get("offset")])
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "offset.yaml", server.URL, nil, newDefinitionClock())
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x", Limit: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("items = %d, want 3", len(items))
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(offsets, ",") != "0,2,4" {
		t.Errorf("offsets = %v, want [0 2 4]", offsets)
	}
	if strings.Join(pagesSeen, ",") != "1,2,3" {
		t.Errorf("pages = %v, want [1 2 3]", pagesSeen)
	}
}

func TestDefinitionConnectorReloginOnUnauthorized(t *testing.T) {
	loginPage := fixture(t, "login.html")
	loginOK := fixture(t, "login_ok.html")
	searchPage := fixture(t, "search.html")
	var loginPosts atomic.Int32
	var searches atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			if r.Method == http.MethodGet {
				writeBody(t, w, loginPage)
				return
			}
			loginPosts.Add(1)
			writeBody(t, w, loginOK)
		case "/search":
			if searches.Add(1) == 1 {
				http.Error(w, "session expired", http.StatusUnauthorized)
				return
			}
			writeBody(t, w, searchPage)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "form_login.yaml", server.URL, map[string]string{
		"username": "alice",
		"password": "secret",
	}, newDefinitionClock())
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("items = %d, want 1", len(items))
	}
	if got := loginPosts.Load(); got != 2 {
		t.Errorf("logins = %d, want 2 (initial + re-login)", got)
	}
}

func TestDefinitionConnectorMagnetFromInfoHash(t *testing.T) {
	def := loadTestDefinition(t, "pagination.yaml", "http://127.0.0.1:1")
	def.Search.Response.Required = nil
	connector, err := NewDefinitionConnector(DefinitionConfig{Definition: def})
	if err != nil {
		t.Fatal(err)
	}

	hex := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	item, ok, err := connector.normalizeRow(map[string]string{
		"title":    "  Derived   Magnet ",
		"infohash": "urn:btih:" + hex,
		"details":  "/details/1",
	}, nil)
	if err != nil || !ok {
		t.Fatalf("normalizeRow = %v, %v", ok, err)
	}
	if item.InfoHash != hex {
		t.Errorf("infohash = %q", item.InfoHash)
	}
	if !strings.Contains(item.Magnet, "urn:btih:"+hex) || !strings.Contains(item.Magnet, "dn=Derived") {
		t.Errorf("magnet = %q", item.Magnet)
	}
	if item.Title != "Derived Magnet" {
		t.Errorf("title = %q", item.Title)
	}

	base32 := "MFRGGZDFMZTWQ2LKNNWG23TPOBYXE43U"
	item, ok, err = connector.normalizeRow(map[string]string{
		"title":    "Base32",
		"infohash": base32,
		"download": "/download/1.torrent",
	}, nil)
	if err != nil || !ok {
		t.Fatalf("normalizeRow base32 = %v, %v", ok, err)
	}
	if item.InfoHash != base32 {
		t.Errorf("base32 infohash = %q", item.InfoHash)
	}
	if item.Magnet != "" {
		t.Errorf("magnet derived despite download URL: %q", item.Magnet)
	}
}

func TestDefinitionConnectorInvalidSelectorAndFilter(t *testing.T) {
	if _, err := extractHTMLRows([]byte("<html></html>"), definition.Response{
		Rows: definition.RowSelector{Selector: definition.Selector{"div["}},
	}, nil); err == nil {
		t.Error("invalid row selector accepted")
	}

	if _, err := applyFilters("x", []definition.Filter{{Name: "bogus"}}); err == nil {
		t.Error("unknown filter accepted")
	}
	if _, err := applyFilters("abc123", []definition.Filter{{Name: "regex", Arg: `(\d+)`}}); err != nil {
		t.Errorf("regex filter: %v", err)
	}
}

func TestDefinitionConnectorLatest(t *testing.T) {
	searchPage := fixture(t, "search.html")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "" {
			t.Errorf("latest q = %q, want empty", got)
		}
		writeBody(t, w, searchPage)
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "html_fields.yaml", server.URL, nil, newDefinitionClock())
	items, err := connector.Latest(context.Background(), LatestRequest{Categories: []int{5040}, Limit: 10})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("items = %d, want 1", len(items))
	}
}

func TestDefinitionConnectorTestAuthPhase(t *testing.T) {
	home := fixture(t, "home.html")
	loginPage := fixture(t, "login.html")
	loginOK := fixture(t, "login_ok.html")
	searchPage := fixture(t, "search.html")
	var loginPosts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			writeBody(t, w, home)
		case "/login":
			if r.Method == http.MethodGet {
				writeBody(t, w, loginPage)
				return
			}
			loginPosts.Add(1)
			writeBody(t, w, loginOK)
		case "/search":
			writeBody(t, w, searchPage)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "form_login.yaml", server.URL, map[string]string{
		"username": "alice",
		"password": "secret",
	}, newDefinitionClock())
	result, err := connector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK {
		t.Fatalf("Test failed: %+v", result.Phases)
	}
	if loginPosts.Load() != 1 {
		t.Errorf("logins = %d, want 1", loginPosts.Load())
	}
	for _, phase := range result.Phases {
		if phase.Name == "auth" && phase.Message != "login ok" {
			t.Errorf("auth message = %q", phase.Message)
		}
	}
}

func TestDefinitionConnectorBasicAuth(t *testing.T) {
	searchPage := fixture(t, "search.html")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "alice" || password != "secret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("q") == "fail" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		writeBody(t, w, searchPage)
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "basic_auth.yaml", server.URL, map[string]string{
		"username": "alice",
		"password": "secret",
	}, newDefinitionClock())
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "ok"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}

	_, err = connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "fail"})
	if err == nil {
		t.Fatal("Search succeeded, want error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Errorf("error leaked the password: %q", err)
	}
}

func TestDefinitionConnectorTokenAuth(t *testing.T) {
	searchPage := fixture(t, "search.html")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer top-secret-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.URL.Query().Get("q") == "fail" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		writeBody(t, w, searchPage)
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "token_auth.yaml", server.URL, map[string]string{
		"token": "top-secret-token",
	}, newDefinitionClock())
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "ok"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	_, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "fail"})
	if err == nil || strings.Contains(err.Error(), "top-secret-token") {
		t.Errorf("error = %v, want error without token", err)
	}
}

func TestDefinitionConnectorAPIKey(t *testing.T) {
	searchPage := fixture(t, "search.html")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()["apikey"]
		if len(values) != 1 || values[0] != "key-123" {
			t.Errorf("apikey = %v, want exactly [key-123]", values)
		}
		if r.URL.Query().Get("q") == "fail" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		writeBody(t, w, searchPage)
	}))
	defer server.Close()

	for _, definitionName := range []string{"api_key.yaml", "api_key_referenced.yaml"} {
		connector := newDefinitionConnector(t, definitionName, server.URL, map[string]string{"api_key": "key-123"}, newDefinitionClock())
		if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "ok"}); err != nil {
			t.Fatalf("%s Search: %v", definitionName, err)
		}
		_, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "fail"})
		if err == nil {
			t.Fatalf("%s Search succeeded, want error", definitionName)
		}
		if strings.Contains(err.Error(), "key-123") {
			t.Errorf("%s error leaked the API key: %q", definitionName, err)
		}
	}
}

func TestDefinitionConnectorDownload(t *testing.T) {
	torrent := []byte("torrent-bytes")
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	detailsPage := fixture(t, "details.html")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/direct.torrent", "/files/real.torrent":
			w.Header().Set("Content-Type", "application/x-bittorrent")
			writeBody(t, w, torrent)
		case "/redirect.torrent":
			w.Header().Set("Location", "magnet:?xt=urn:btih:"+hash)
			w.WriteHeader(http.StatusFound)
		case "/details/1":
			writeBody(t, w, detailsPage)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "download.yaml", server.URL, nil, newDefinitionClock())

	direct, err := connector.Download(context.Background(), Item{
		DownloadURL: server.URL + "/direct.torrent",
		Title:       "My Release",
	})
	if err != nil {
		t.Fatalf("direct Download: %v", err)
	}
	if string(direct.Body) != string(torrent) {
		t.Errorf("body = %q", direct.Body)
	}
	if direct.ContentType != "application/x-bittorrent" {
		t.Errorf("content type = %q", direct.ContentType)
	}
	if direct.Filename != "My Release.torrent" {
		t.Errorf("filename = %q", direct.Filename)
	}

	redirected, err := connector.Download(context.Background(), Item{
		DownloadURL: server.URL + "/redirect.torrent?passkey=secret",
		Title:       "Redirect",
	})
	if err != nil {
		t.Fatalf("redirect Download: %v", err)
	}
	if redirected.Magnet != "magnet:?xt=urn:btih:"+hash {
		t.Errorf("magnet = %q", redirected.Magnet)
	}

	fromDetails, err := connector.Download(context.Background(), Item{
		DetailsURL: server.URL + "/details/1",
		Title:      "Details Release",
	})
	if err != nil {
		t.Fatalf("details Download: %v", err)
	}
	if string(fromDetails.Body) != string(torrent) {
		t.Errorf("details body = %q", fromDetails.Body)
	}

	magnet, err := connector.Download(context.Background(), Item{Magnet: "magnet:?xt=urn:btih:" + hash})
	if err != nil || magnet.Magnet != "magnet:?xt=urn:btih:"+hash {
		t.Errorf("magnet Download = %+v, %v", magnet, err)
	}

	_, err = connector.Download(context.Background(), Item{
		DownloadURL: server.URL + "/missing.torrent?passkey=super-secret-passkey",
	})
	if err == nil {
		t.Fatal("missing Download succeeded, want error")
	}
	if strings.Contains(err.Error(), "super-secret-passkey") {
		t.Errorf("error leaked the passkey: %q", err)
	}
}

func TestDefinitionConnectorDownloadUsesSessionAuth(t *testing.T) {
	torrent := []byte("authenticated-torrent")
	searchPage := fixture(t, "search.html")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			if r.Method == http.MethodGet {
				writeBody(t, w, []byte(`<form><input name="csrf_token" value="tok-1"></form>`))
				return
			}
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			if r.FormValue("csrf_token") != "tok-1" || r.FormValue("username") != "alice" ||
				r.FormValue("password") != "secret" {
				http.Error(w, "bad credentials", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "auth", Value: "yes", Path: "/"})
			writeBody(t, w, []byte(`<a class="logout" href="/logout">out</a>`))
		case "/search":
			writeBody(t, w, searchPage)
		case "/download/1.torrent":
			if cookie, err := r.Cookie("auth"); err != nil || cookie.Value != "yes" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/x-bittorrent")
			writeBody(t, w, torrent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "form_login.yaml", server.URL,
		map[string]string{"username": "alice", "password": "secret"}, newDefinitionClock())

	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "ubuntu"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	payload, err := connector.Download(context.Background(), items[0])
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if string(payload.Body) != string(torrent) {
		t.Errorf("body = %q", payload.Body)
	}
}

func TestDefinitionConnectorTestPhases(t *testing.T) {
	home := fixture(t, "home.html")
	searchPage := fixture(t, "search.html")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			writeBody(t, w, home)
		case "/search":
			if got := r.URL.Query().Get("q"); got != "probe" {
				t.Errorf("health q = %q, want probe", got)
			}
			writeBody(t, w, searchPage)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "test_phases.yaml", server.URL, nil, newDefinitionClock())
	result, err := connector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK {
		t.Errorf("Test not OK: %+v", result.Phases)
	}
	if len(result.Phases) != 4 {
		t.Fatalf("phases = %d, want 4", len(result.Phases))
	}
	names := []string{"reach", "auth", "search", "download"}
	for i, phase := range result.Phases {
		if phase.Name != names[i] {
			t.Errorf("phase %d = %q, want %q", i, phase.Name, names[i])
		}
		if phase.Duration < 0 {
			t.Errorf("phase %q has negative duration", phase.Name)
		}
	}
	downloadPhase := result.Phases[3]
	if !downloadPhase.OK || downloadPhase.Message != "skipped" {
		t.Errorf("download phase = %+v", downloadPhase)
	}
}

func TestDefinitionConnectorTestFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer server.Close()

	connector := newDefinitionConnector(t, "test_phases.yaml", server.URL, nil, newDefinitionClock())
	result, err := connector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if result.OK {
		t.Errorf("Test OK, want failure: %+v", result.Phases)
	}
	if result.Phases[0].OK {
		t.Error("reach phase succeeded, want failure")
	}
	if result.Phases[2].OK {
		t.Error("search phase succeeded, want failure")
	}
}

func TestDefinitionConnectorConstructorValidation(t *testing.T) {
	if _, err := NewDefinitionConnector(DefinitionConfig{}); err == nil {
		t.Error("nil definition accepted")
	}

	def := loadTestDefinition(t, "form_login.yaml", "http://127.0.0.1:1")
	if _, err := NewDefinitionConnector(DefinitionConfig{Definition: def, Settings: map[string]string{"username": "alice"}}); err == nil ||
		!strings.Contains(err.Error(), "password") {
		t.Errorf("missing setting error = %v", err)
	}

	apiDef := loadTestDefinition(t, "api_key.yaml", "http://127.0.0.1:1")
	if _, err := NewDefinitionConnector(DefinitionConfig{Definition: apiDef}); err == nil ||
		!strings.Contains(err.Error(), "api") {
		t.Errorf("missing api key error = %v", err)
	}
}

func TestDefinitionConnectorRejectsUndeclaredMode(t *testing.T) {
	connector := newDefinitionConnector(t, "html_fields.yaml", "http://127.0.0.1:1", nil, newDefinitionClock())
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeTV}); err == nil ||
		!strings.Contains(err.Error(), "tvsearch") {
		t.Errorf("Search(tvsearch) = %v", err)
	}
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: "bogus"}); err == nil {
		t.Error("Search(bogus) succeeded")
	}
}

func TestDefinitionConnectorCapabilities(t *testing.T) {
	connector := newDefinitionConnector(t, "form_login.yaml", "http://127.0.0.1:1", map[string]string{
		"username": "alice",
		"password": "secret",
	}, newDefinitionClock())
	caps := connector.Capabilities()
	if caps.Protocol != ProtocolTorrent {
		t.Errorf("protocol = %q", caps.Protocol)
	}
	if !caps.Supports(ModeSearch) {
		t.Error("search mode is not supported")
	}
	if !caps.SupportsParam(ModeSearch, ParamQuery) {
		t.Error("q parameter is not supported")
	}
	if len(caps.Categories) != 1 || caps.Categories[0] != 5040 {
		t.Errorf("categories = %v", caps.Categories)
	}
}

func TestDefinitionConnectorCursorPaginationUnsupported(t *testing.T) {
	def := loadTestDefinition(t, "pagination.yaml", "http://127.0.0.1:1")
	def.Search.Pagination.Type = "cursor"
	connector, err := NewDefinitionConnector(DefinitionConfig{Definition: def})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch}); err == nil ||
		!strings.Contains(err.Error(), "cursor") {
		t.Errorf("Search with cursor = %v", err)
	}
}

func TestDefinitionConnectorContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<html><body></body></html>`)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	connector := newDefinitionConnector(t, "pagination.yaml", server.URL, nil, newDefinitionClock())
	if _, err := connector.Search(ctx, SearchRequest{Mode: ModeSearch}); err == nil {
		t.Fatal("Search with canceled context succeeded")
	}
}
