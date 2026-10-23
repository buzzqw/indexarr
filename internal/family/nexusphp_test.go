package family

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

const nexusEmptyTable = `<!DOCTYPE html><html><body><table class="torrents"><tr class="colhead"><td>Type</td><td>Name</td></tr></table></body></html>`

type nexusFake struct {
	mu            sync.Mutex
	loginGET      int
	loginPOST     int
	searches      []url.Values
	loginForm     url.Values
	searchCookies []string
	postCookies   []string
}

func (f *nexusFake) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loginGET, f.loginPOST
}

func TestNexusPHPLoginSearchPaginationAndParsing(t *testing.T) {
	fixturePage1 := readFixture(t, "nexusphp_search.html")
	fixturePage2 := readFixture(t, "nexusphp_search_page2.html")
	fixtureLogin := readFixture(t, "nexusphp_login.html")
	fake := &nexusFake{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login.php":
			fake.mu.Lock()
			fake.loginGET++
			fake.mu.Unlock()
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(fixtureLogin)
		case "/takelogin.php":
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			fake.mu.Lock()
			fake.loginPOST++
			fake.loginForm = r.PostForm
			fake.postCookies = append(fake.postCookies, r.Header.Get("Cookie"))
			fake.mu.Unlock()
			if r.PostForm.Get("csrf_token") != "csrf-value-123" {
				http.Error(w, "missing csrf", http.StatusForbidden)
				return
			}
			if r.PostForm.Get("username") != "nexus-user" || r.PostForm.Get("password") != "nexus-pass" {
				_, _ = io.WriteString(w, `<html><body><p class="error">Invalid username or password</p></body></html>`)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "session-abc", Path: "/"})
			_, _ = io.WriteString(w, `<html><body><a href="logout.php">Logout</a></body></html>`)
		case "/torrents.php":
			if !strings.Contains(r.Header.Get("Cookie"), "session=session-abc") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fake.mu.Lock()
			fake.searches = append(fake.searches, r.URL.Query())
			fake.searchCookies = append(fake.searchCookies, r.Header.Get("Cookie"))
			fake.mu.Unlock()
			switch r.URL.Query().Get("page") {
			case "", "1":
				_, _ = w.Write(fixturePage1)
			case "2":
				_, _ = w.Write(fixturePage2)
			default:
				_, _ = io.WriteString(w, nexusEmptyTable)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector, err := NewNexusPHP(NexusPHPConfig{
		Indexer:    engine.IndexerRef{Slug: "nexus"},
		BaseURL:    server.URL,
		Username:   "nexus-user",
		Password:   "nexus-pass",
		Passkey:    "pk-123",
		Transport:  newTestTransport(),
		Categories: []int{401, 402},
		UserAgent:  "nexus-test-agent",
	})
	if err != nil {
		t.Fatalf("NewNexusPHP: %v", err)
	}

	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode:    engine.ModeTV,
		Query:   "Show",
		Season:  1,
		Episode: 2,
		Limit:   100,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 (duplicate page-2 row must be dropped)", len(items))
	}
	loginGET, loginPOST := fake.counts()
	if loginGET != 1 || loginPOST != 1 {
		t.Errorf("login requests = GET %d POST %d, want one login", loginGET, loginPOST)
	}
	fake.mu.Lock()
	form := fake.loginForm
	searches := append([]url.Values(nil), fake.searches...)
	cookies := append([]string(nil), fake.searchCookies...)
	fake.mu.Unlock()
	if form.Get("returnto") != "torrents.php" || form.Get("csrf_token") != "csrf-value-123" {
		t.Errorf("hidden login fields = %v", form)
	}
	if len(searches) != 3 {
		t.Fatalf("search pages = %d, want 3", len(searches))
	}
	for index, query := range searches {
		if got := query.Get("search"); got != "Show S01E02" {
			t.Errorf("page %d search = %q", index+1, got)
		}
		if got := query.Get("cat"); got != "401,402" {
			t.Errorf("page %d cat = %q", index+1, got)
		}
	}
	if searches[0].Get("page") != "1" || searches[1].Get("page") != "2" || searches[2].Get("page") != "3" {
		t.Errorf("page params = %q/%q/%q", searches[0].Get("page"), searches[1].Get("page"), searches[2].Get("page"))
	}
	for _, cookie := range cookies {
		if !strings.Contains(cookie, "session=session-abc") {
			t.Errorf("search cookie = %q", cookie)
		}
	}

	first := items[0]
	if first.Title != "Example.Movie.2024.1080p.BluRay" || first.GUID != "nexusphp:42" {
		t.Errorf("first title/guid = %q/%q", first.Title, first.GUID)
	}
	if first.Size != 1610612736 || first.Seeders != 12 || first.Leechers != 3 {
		t.Errorf("first size/seeds/leech = %d/%d/%d", first.Size, first.Seeders, first.Leechers)
	}
	if len(first.Categories) != 1 || first.Categories[0] != 2000 {
		t.Errorf("first categories = %v, want [2000]", first.Categories)
	}
	if first.Published.Format("2006-01-02 15:04:05") != "2024-01-02 15:04:05" {
		t.Errorf("first published = %v", first.Published)
	}
	wantDownload := server.URL + "/download.php?id=42&passkey=pk-123"
	if first.DownloadURL != wantDownload {
		t.Errorf("first download = %q, want %q", first.DownloadURL, wantDownload)
	}
	if first.DetailsURL != server.URL+"/details.php?id=42&hit=1" {
		t.Errorf("first details = %q", first.DetailsURL)
	}

	second := items[1]
	if second.InfoHash != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("second infohash = %q", second.InfoHash)
	}
	if len(second.Categories) != 1 || second.Categories[0] != 5000 {
		t.Errorf("second categories = %v, want [5000]", second.Categories)
	}
	if second.Size != 734003200 || second.Seeders != 1234 || second.Leechers != 5 {
		t.Errorf("second size/seeds/leech = %d/%d/%d", second.Size, second.Seeders, second.Leechers)
	}
	if second.Published.Format("2006-01-02 15:04") != "2024-02-03 10:30" {
		t.Errorf("second published = %v", second.Published)
	}

	third := items[2]
	if third.GUID != "nexusphp:44" || third.Title != "Album.2020.FLAC" {
		t.Errorf("third guid/title = %q/%q", third.GUID, third.Title)
	}
	if len(third.Categories) != 1 || third.Categories[0] != 3000 {
		t.Errorf("third categories = %v, want [3000]", third.Categories)
	}
	if third.Size != 2048<<20 {
		t.Errorf("third size = %d", third.Size)
	}
	if third.Published.Year() != 2024 || third.Published.Month() != time.January {
		t.Errorf("third published = %v", third.Published)
	}

	for _, item := range items {
		for key, value := range item.Attributes {
			if strings.Contains(value, "pk-123") {
				t.Fatalf("attribute %q leaked the passkey", key)
			}
		}
		if strings.Contains(item.DownloadURL, "authkey=") {
			t.Errorf("download leaked an authkey: %s", item.DownloadURL)
		}
	}
}

func TestNexusPHPLoginFailureRedaction(t *testing.T) {
	const password = "nexus-super-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login.php":
			_, _ = io.WriteString(w, `<html><body><form><input type="hidden" name="csrf" value="x"></form></body></html>`)
		case "/takelogin.php":
			_, _ = io.WriteString(w, `<html><body><p class="error">Invalid password `+password+`</p></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector, err := NewNexusPHP(NexusPHPConfig{
		BaseURL:   server.URL,
		Username:  "user",
		Password:  password,
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewNexusPHP: %v", err)
	}
	_, err = connector.Search(context.Background(), engine.SearchRequest{Query: "test"})
	if err == nil {
		t.Fatal("Search succeeded with a failed login, want error")
	}
	if !strings.Contains(err.Error(), "login failed") {
		t.Errorf("error = %v, want a login failure", err)
	}
	if strings.Contains(err.Error(), password) {
		t.Fatalf("error leaked the password: %v", err)
	}
}

func TestNexusPHPReloginOnUnauthorized(t *testing.T) {
	fake := &nexusFake{}
	searches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login.php":
			fake.mu.Lock()
			fake.loginGET++
			fake.mu.Unlock()
			_, _ = io.WriteString(w, `<html><body><form><input type="hidden" name="csrf" value="x"></form></body></html>`)
		case "/takelogin.php":
			fake.mu.Lock()
			fake.loginPOST++
			fake.mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "fresh", Path: "/"})
			_, _ = io.WriteString(w, `<html><body><a href="logout.php">Logout</a></body></html>`)
		case "/torrents.php":
			fake.mu.Lock()
			searches++
			current := searches
			fake.mu.Unlock()
			if current == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, nexusEmptyTable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector, err := NewNexusPHP(NexusPHPConfig{
		BaseURL:   server.URL,
		Username:  "user",
		Password:  "pass",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewNexusPHP: %v", err)
	}
	if _, err := connector.Search(context.Background(), engine.SearchRequest{Query: "test", Limit: 1}); err != nil {
		t.Fatalf("Search after re-login: %v", err)
	}
	loginGET, loginPOST := fake.counts()
	if loginGET != 2 || loginPOST != 2 {
		t.Errorf("login requests = GET %d POST %d, want a re-login", loginGET, loginPOST)
	}
}

func TestNexusPHPDownloadDirectMagnetAndRedaction(t *testing.T) {
	const passkey = "download-passkey"
	var mu sync.Mutex
	var downloadURLs []*url.URL
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/download.php" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		clone := *r.URL
		downloadURLs = append(downloadURLs, &clone)
		mu.Unlock()
		id := r.URL.Query().Get("id")
		if r.URL.Query().Get("passkey") != passkey {
			http.Error(w, "missing passkey", http.StatusForbidden)
			return
		}
		switch id {
		case "7":
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = io.WriteString(w, "d8:announce5:https")
		case "8":
			w.Header().Set("Location", "magnet:?xt=urn:btih:0123456789abcdef&dn=Example")
			w.WriteHeader(http.StatusFound)
		default:
			http.Error(w, "server exploded", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	connector, err := NewNexusPHP(NexusPHPConfig{
		BaseURL:   server.URL,
		Passkey:   passkey,
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewNexusPHP: %v", err)
	}

	payload, err := connector.Download(context.Background(), engine.Item{
		Title:       "Example.Release",
		DownloadURL: server.URL + "/download.php?id=7",
	})
	if err != nil {
		t.Fatalf("direct Download: %v", err)
	}
	if string(payload.Body) != "d8:announce5:https" || payload.ContentType != "application/x-bittorrent" {
		t.Errorf("payload = %q/%q", payload.Body, payload.ContentType)
	}
	if payload.Filename != "Example.Release.torrent" {
		t.Errorf("filename = %q", payload.Filename)
	}

	magnet, err := connector.Download(context.Background(), engine.Item{
		Title:       "Example.Release",
		DownloadURL: server.URL + "/download.php?id=8",
	})
	if err != nil {
		t.Fatalf("magnet Download: %v", err)
	}
	if magnet.Magnet != "magnet:?xt=urn:btih:0123456789abcdef&dn=Example" || len(magnet.Body) != 0 {
		t.Errorf("magnet payload = %+v", magnet)
	}

	_, err = connector.Download(context.Background(), engine.Item{
		Title:       "Example.Release",
		DownloadURL: server.URL + "/download.php?id=9",
	})
	if err == nil {
		t.Fatal("server error produced no download error")
	}
	if strings.Contains(err.Error(), passkey) {
		t.Fatalf("download error leaked the passkey: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(downloadURLs) != 3 {
		t.Fatalf("download requests = %d, want 3", len(downloadURLs))
	}
	for _, target := range downloadURLs {
		if target.Query().Get("passkey") != passkey {
			t.Errorf("download target %s has no passkey", target)
		}
	}
}

func TestNexusPHPLatestAndCapabilities(t *testing.T) {
	recorder := &queryRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(r.URL.Query())
		_, _ = io.WriteString(w, nexusEmptyTable)
	}))
	defer server.Close()

	connector, err := NewNexusPHP(NexusPHPConfig{
		BaseURL:    server.URL,
		Transport:  newTestTransport(),
		Categories: []int{401, 402},
	})
	if err != nil {
		t.Fatalf("NewNexusPHP: %v", err)
	}

	if _, err := connector.Latest(context.Background(), engine.LatestRequest{Limit: 10}); err != nil {
		t.Fatalf("Latest: %v", err)
	}
	latest := recorder.last()
	if latest.Get("search") != "" {
		t.Errorf("latest sent a search term: %v", latest)
	}
	if latest.Get("page") != "1" || latest.Get("cat") != "401,402" {
		t.Errorf("latest query = %v", latest)
	}

	caps := connector.Capabilities()
	if caps.Protocol != engine.ProtocolTorrent {
		t.Errorf("protocol = %q", caps.Protocol)
	}
	for _, mode := range []engine.Mode{engine.ModeSearch, engine.ModeTV, engine.ModeMovie} {
		if !caps.Supports(mode) {
			t.Errorf("mode %q not declared", mode)
		}
	}
	if caps.Supports(engine.ModeMusic) {
		t.Error("music mode must not be declared")
	}
	if !caps.SupportsParam(engine.ModeTV, engine.ParamSeason) ||
		!caps.SupportsParam(engine.ModeTV, engine.ParamEpisode) ||
		!caps.SupportsParam(engine.ModeMovie, engine.ParamIMDbID) {
		t.Errorf("params = %v", caps.Params)
	}
	if len(caps.Categories) != 2 || caps.Categories[0] != 401 || caps.Categories[1] != 402 {
		t.Errorf("categories = %v", caps.Categories)
	}
}

func TestNexusPHPTestPhases(t *testing.T) {
	fixtureLogin := readFixture(t, "nexusphp_login.html")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login.php":
			_, _ = w.Write(fixtureLogin)
		case "/takelogin.php":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
			_, _ = io.WriteString(w, `<html><body><a href="logout.php">Logout</a></body></html>`)
		case "/torrents.php":
			_, _ = io.WriteString(w, nexusEmptyTable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector, err := NewNexusPHP(NexusPHPConfig{
		BaseURL:   server.URL,
		Username:  "user",
		Password:  "secret-password",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewNexusPHP: %v", err)
	}
	result, err := connector.Test(context.Background())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !result.OK {
		t.Fatalf("Test not OK: %+v", result.Phases)
	}
	names := make([]string, 0, len(result.Phases))
	for _, phase := range result.Phases {
		names = append(names, phase.Name)
		if strings.Contains(phase.Message, "secret-password") {
			t.Fatalf("phase %q leaked the password: %s", phase.Name, phase.Message)
		}
	}
	if strings.Join(names, ",") != "reach,login,search" {
		t.Errorf("phases = %v", names)
	}
}

func TestNexusPHPLoginRedirectCapturesCookies(t *testing.T) {
	var mu sync.Mutex
	logins := 0
	searches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login.php":
			_, _ = io.WriteString(w, `<html><body><form><input type="hidden" name="csrf" value="x"></form></body></html>`)
		case "/takelogin.php":
			mu.Lock()
			logins++
			mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "redirected", Path: "/"})
			w.Header().Set("Location", "/home.php")
			w.WriteHeader(http.StatusFound)
		case "/home.php":
			if cookie, err := r.Cookie("sid"); err != nil || cookie.Value != "redirected" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `<html><body><a href="logout.php">Logout</a></body></html>`)
		case "/torrents.php":
			if cookie, err := r.Cookie("sid"); err != nil || cookie.Value != "redirected" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mu.Lock()
			searches++
			mu.Unlock()
			_, _ = io.WriteString(w, nexusEmptyTable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	connector, err := NewNexusPHP(NexusPHPConfig{
		BaseURL:   server.URL,
		Username:  "user",
		Password:  "pass",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewNexusPHP: %v", err)
	}
	if _, err := connector.Search(context.Background(), engine.SearchRequest{Query: "test", Limit: 1}); err != nil {
		t.Fatalf("Search after redirecting login: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if logins != 1 || searches != 1 {
		t.Errorf("logins/searches = %d/%d, want 1/1", logins, searches)
	}
}

func TestNexusPHPCustomProfileParsing(t *testing.T) {
	const page = `<!DOCTYPE html><html><body>
<table class="results">
  <tr class="row">
    <td class="cat"><a href="browse.php?cat=999">Documentaries</a></td>
    <td class="name"><a href="/custom/details.php?id=55">Doc.2022.1080p</a></td>
    <td class="dl"><a href="/custom/download.php?id=55">Get</a></td>
    <td class="size">1.25 GB</td>
    <td class="seed">9</td>
    <td class="leech">2</td>
    <td class="date">2022-05-06</td>
  </tr>
</table>
</body></html>`

	recorder := &queryRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/custom/search.php" {
			http.NotFound(w, r)
			return
		}
		recorder.add(r.URL.Query())
		_, _ = io.WriteString(w, page)
	}))
	defer server.Close()

	connector, err := NewNexusPHP(NexusPHPConfig{
		BaseURL:    server.URL,
		Transport:  newTestTransport(),
		Categories: []int{401},
		Profile: NexusPHPProfile{
			SearchPath:       "/custom/search.php",
			RowsSelector:     "table.results tr.row",
			TitleSelector:    "td.name a",
			DetailsSelector:  "td.name a",
			DownloadSelector: "td.dl a",
			SizeSelector:     "td.size",
			SeedersSelector:  "td.seed",
			LeechersSelector: "td.leech",
			DateSelector:     "td.date",
			CategorySelector: "td.cat a",
			PageParam:        "p",
		},
	})
	if err != nil {
		t.Fatalf("NewNexusPHP: %v", err)
	}
	items, err := connector.Search(context.Background(), engine.SearchRequest{Query: "Doc", Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	query := recorder.last()
	if query.Get("search") != "Doc" || query.Get("p") != "1" || query.Get("cat") != "401" {
		t.Errorf("query = %v", query)
	}
	item := items[0]
	if item.Title != "Doc.2022.1080p" || item.GUID != "nexusphp:55" {
		t.Errorf("title/guid = %q/%q", item.Title, item.GUID)
	}
	if len(item.Categories) != 1 || item.Categories[0] != 5080 {
		t.Errorf("categories = %v, want [5080] from the category name", item.Categories)
	}
	if item.Size != 1342177280 || item.Seeders != 9 || item.Leechers != 2 {
		t.Errorf("size/seeds/leech = %d/%d/%d", item.Size, item.Seeders, item.Leechers)
	}
	if item.DetailsURL != server.URL+"/custom/details.php?id=55" {
		t.Errorf("details = %q", item.DetailsURL)
	}
	if item.DownloadURL != server.URL+"/custom/download.php?id=55" {
		t.Errorf("download = %q", item.DownloadURL)
	}
	if item.Published.Format("2006-01-02") != "2022-05-06" {
		t.Errorf("published = %v", item.Published)
	}
}

func TestNexusPHPParseHelpers(t *testing.T) {
	sizes := []struct {
		raw  string
		want int64
	}{
		{"1.5 GB", 1610612736},
		{"700 MiB", 734003200},
		{"2,048 MB", 2048 << 20},
		{"1234567890", 1234567890},
		{"1,5 GB", 1610612736},
		{"1 KB", 1024},
		{"1 TB", 1 << 40},
		{"bad", 0},
	}
	for _, tc := range sizes {
		if got := nexusParseSize(tc.raw); got != tc.want {
			t.Errorf("nexusParseSize(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}

	counts := map[string]int64{"1,234": 1234, "12": 12, "—": 0, " 5 ": 5}
	for raw, want := range counts {
		if got := nexusParseCount(raw); got != want {
			t.Errorf("nexusParseCount(%q) = %d, want %d", raw, got, want)
		}
	}

	dates := []struct {
		raw  string
		want string
	}{
		{"2006-01-02 15:04:05", "2006-01-02 15:04:05"},
		{"2006-01-02", "2006-01-02 00:00:00"},
		{"02-01-2006 15:04", "2006-01-02 15:04:00"},
		{"Mon, 02 Jan 2006 15:04:05 +0000", "2006-01-02 15:04:05"},
	}
	for _, tc := range dates {
		if got := nexusParseDate(tc.raw).Format("2006-01-02 15:04:05"); got != tc.want {
			t.Errorf("nexusParseDate(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
	if !nexusParseDate("not a date").IsZero() {
		t.Error("invalid date did not return the zero time")
	}
}

func TestNewNexusPHPValidationAndProfileDefaults(t *testing.T) {
	transport := newTestTransport()
	if _, err := NewNexusPHP(NexusPHPConfig{Transport: transport}); err == nil {
		t.Error("missing base URL did not fail")
	}
	if _, err := NewNexusPHP(NexusPHPConfig{BaseURL: "ftp://tracker.example", Transport: transport}); err == nil {
		t.Error("non-HTTP base URL did not fail")
	}

	connector, err := NewNexusPHP(NexusPHPConfig{
		BaseURL:   "https://tracker.example/nexus",
		Transport: transport,
		Profile: NexusPHPProfile{
			SearchPath:   "/custom.php",
			RowsSelector: "table.custom tr",
			SizeSelector: "td.size",
		},
	})
	if err != nil {
		t.Fatalf("NewNexusPHP: %v", err)
	}
	profile := connector.cfg.Profile
	if profile.SearchPath != "/custom.php" || profile.RowsSelector != "table.custom tr" || profile.SizeSelector != "td.size" {
		t.Errorf("custom profile fields were not kept: %+v", profile)
	}
	if profile.LoginPath != "/login.php" || profile.PageParam != "page" {
		t.Errorf("defaults were not merged: %+v", profile)
	}
	if connector.loginPostPath != "/takelogin.php" {
		t.Errorf("login POST path = %q", connector.loginPostPath)
	}
	if connector.downloadPS != "/download.php" {
		t.Errorf("download path = %q", connector.downloadPS)
	}
}
