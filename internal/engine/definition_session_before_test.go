package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
)

const sessionDefinitionYAML = `schema: 1
meta:
  id: mircrew-session-test
  name: MirCrew session test
  type: private
  protocol: torrent
  urls: ["https://site.invalid/"]
settings:
  - { name: username, type: text, required: true }
  - { name: password, type: password, required: true }
capabilities:
  search: [q]
auth:
  strategy: form
  steps:
    - get: /ucp.php?mode=login
      extract:
        form_token: { selector: 'input[name="form_token"]', attr: value }
        sid: { selector: 'input[name="sid"]', attr: value }
    - post: /ucp.php?mode=login
      form:
        username: "{{ settings.username }}"
        password: "{{ settings.password }}"
        form_token: "{{ form_token }}"
        sid: "{{ sid }}"
        login: Login
  success: { selector: "a.logout" }
  refresh_after: 6h
search:
  request:
    get: /search.php
    params: { keywords: "{{ query.keywords }}" }
  response:
    type: html
    rows: { selector: "li.row" }
    fields:
      title: { selector: "a.topictitle" }
      details: { selector: "a.topictitle", attr: href, type: url }
    required: [title, details]
download:
  resolve_on: grab
  from: details
  extract:
    - magnet: { selector: 'a[href^="magnet:"]', attr: href }
`

type fakeFlareSolverr struct {
	mu       sync.Mutex
	sessions map[string]bool
	created  int
	destroy  []string
	lastPost url.Values
	pages    map[string]string
	postPage string
	dead     map[string]bool
	server   *httptest.Server
}

func newFakeFlareSolverr(t *testing.T) *fakeFlareSolverr {
	t.Helper()
	fake := &fakeFlareSolverr{sessions: make(map[string]bool), pages: make(map[string]string)}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Cmd      string            `json:"cmd"`
			Session  string            `json:"session"`
			URL      string            `json:"url"`
			PostData string            `json:"postData"`
			Headers  map[string]string `json:"headers"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode FlareSolverr command: %v", err)
			return
		}
		fake.mu.Lock()
		defer fake.mu.Unlock()
		switch payload.Cmd {
		case "sessions.list":
			names := make([]string, 0, len(fake.sessions))
			for name := range fake.sessions {
				names = append(names, name)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "sessions": names})
		case "sessions.create":
			if fake.sessions[payload.Session] {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "message": "Session already exists."})
				return
			}
			fake.sessions[payload.Session] = true
			fake.created++
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "sessions.destroy":
			delete(fake.sessions, payload.Session)
			fake.destroy = append(fake.destroy, payload.Session)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "request.get", "request.post":
			if !fake.sessions[payload.Session] {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "message": "unknown session"})
				return
			}
			if target, err := url.Parse(payload.URL); err == nil && fake.dead[target.Host] {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "message": "Error: Error solving the challenge. Message: unknown error: net::ERR_NAME_NOT_RESOLVED"})
				return
			}
			if payload.Cmd == "request.post" {
				values, _ := url.ParseQuery(payload.PostData)
				fake.lastPost = values
			}
			body := ""
			if payload.Cmd == "request.post" {
				body = fake.postPage
			}
			if body == "" {
				body = fake.pages[payload.URL]
			}
			if body == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "solution": map[string]any{"status": 404, "response": "not found"}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "solution": map[string]any{"status": 200, "response": body}})
		default:
			t.Errorf("unexpected FlareSolverr command %q", payload.Cmd)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func newSessionConnector(t *testing.T, fake *fakeFlareSolverr, clock Clock) *DefinitionConnector {
	t.Helper()
	return newSessionConnectorWithRegistry(t, fake, clock, NewSessionRegistry())
}

func newSessionConnectorWithRegistry(t *testing.T, fake *fakeFlareSolverr, clock Clock, registry *SessionRegistry) *DefinitionConnector {
	t.Helper()
	def, err := definition.Parse([]byte(sessionDefinitionYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	def.Session = &definition.Session{
		Mode:        "flaresolverr",
		Endpoint:    fake.server.URL,
		NamePrefix:  "mircrew-idx",
		MaxAge:      definition.Duration(12 * time.Hour),
		IdleTimeout: definition.Duration(30 * time.Minute),
	}
	transport := NewTransport(TransportConfig{AllowPrivate: true, Clock: clock})
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic", "password": "synthetic-password"},
		Transport:  transport,
		Sessions:   registry,
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	return connector
}

func TestDefinitionPersistentSessionLoginAndSearch(t *testing.T) {
	fake := newFakeFlareSolverr(t)
	fake.sessions["mircrew-idx-orphan"] = true
	fake.sessions["other-tool"] = true
	fake.pages["https://site.invalid/ucp.php?mode=login"] = `<html><form action="./ucp.php?mode=login"><input type="hidden" name="form_token" value="synthetic-token"><input type="hidden" name="sid" value="synthetic-sid"></form></html>`
	fake.postPage = `<html><a class="logout" href="/ucp.php?mode=logout">Logout</a></html>`
	fake.pages["https://site.invalid/search.php?keywords=Reacher"] = `<html><ul><li class="row"><a class="topictitle" href="/viewtopic.php?t=1">Reacher - Stagione 3</a></li></ul></html>`

	connector := newSessionConnector(t, fake, newDefinitionClock())
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "Reacher"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 || items[0].DetailsURL != "https://site.invalid/viewtopic.php?t=1" {
		t.Fatalf("items = %+v", items)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.created != 1 {
		t.Errorf("sessions created = %d, want 1", fake.created)
	}
	if fake.sessions["mircrew-idx-orphan"] {
		t.Error("an orphan with the ownership prefix was not destroyed")
	}
	if !fake.sessions["other-tool"] {
		t.Error("a session owned by another tool was destroyed")
	}
	if fake.lastPost.Get("username") != "synthetic" || fake.lastPost.Get("password") != "synthetic-password" ||
		fake.lastPost.Get("form_token") != "synthetic-token" || fake.lastPost.Get("sid") != "synthetic-sid" {
		t.Errorf("login POST = %v", fake.lastPost)
	}
}

func TestDefinitionPersistentSessionRecycles(t *testing.T) {
	fake := newFakeFlareSolverr(t)
	fake.pages["https://site.invalid/ucp.php?mode=login"] = `<html><form action="./ucp.php"><input type="hidden" name="form_token" value="t"><input type="hidden" name="sid" value="s"></form></html>`
	fake.postPage = `<html><a class="logout" href="/logout">Logout</a></html>`
	fake.pages["https://site.invalid/search.php?keywords=x"] = `<html><ul><li class="row"><a class="topictitle" href="/t/1">One</a></li></ul></html>`

	clock := newDefinitionClock()
	connector := newSessionConnector(t, fake, clock)
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"}); err != nil {
		t.Fatalf("first Search: %v", err)
	}
	clock.Advance(12*time.Hour + time.Second)
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"}); err != nil {
		t.Fatalf("second Search: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.created != 2 {
		t.Errorf("sessions created = %d, want 2 (12h recycle)", fake.created)
	}
	if len(fake.destroy) < 1 {
		t.Error("the expired session was not destroyed")
	}
}

func TestDownloadBeforeThanksOnlyOnGrabAndSelectsEpisode(t *testing.T) {
	const (
		hashS01E01 = "1111111111111111111111111111111111111111"
		hashS01E02 = "2222222222222222222222222222222222222222"
		hashPack   = "3333333333333333333333333333333333333333"
	)
	var (
		searchCalls atomic.Int32
		topicGets   atomic.Int32
		thanksPosts atomic.Int32
		formSeen    url.Values
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/search":
			searchCalls.Add(1)
			_, _ = w.Write([]byte(`<html><table class="results"><tbody>
<tr><td><a class="title" href="/topic/1">FBI Most Wanted - Stagione 1</a></td></tr>
</tbody></table></html>`))
		case "/topic/1":
			topicGets.Add(1)
			if thanksPosts.Load() == 0 {
				_, _ = w.Write([]byte(`<html><body>
<input type="hidden" name="form_token" value="synthetic-token">
<a href="/thanks.php?t=1&amp;thanks=9">Grazie</a>
<p>I magnet sono nascosti finché non ringrazi.</p>
</body></html>`))
				return
			}
			fmt.Fprintf(w, `<html><body>
<a href="magnet:?xt=urn:btih:%s&amp;dn=FBI.Most.Wanted.S01E01.ITA.1080p">S01E01</a>
<a href="magnet:?xt=urn:btih:%s&amp;dn=FBI.Most.Wanted.S01E02.ITA.1080p">S01E02</a>
<a href="magnet:?xt=urn:btih:%s&amp;dn=FBI.Most.Wanted.S01E01-E08.ITA.1080p">Pack</a>
</body></html>`, hashS01E01, hashS01E02, hashPack)
		case "/thanks.php":
			thanksPosts.Add(1)
			_ = r.ParseForm()
			formSeen = r.PostForm
			_, _ = w.Write([]byte(`<html><body>Grazie inviato</body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	def, err := definition.Parse([]byte(`schema: 1
meta:
  id: before-test
  name: Before test
  type: private
  protocol: torrent
  urls: ["` + server.URL + `/"]
capabilities:
  tv-search: [q, season, ep]
search:
  request:
    get: /search
    params: { q: "{{ query.keywords }}" }
  response:
    type: html
    rows: { selector: "table.results tbody tr" }
    fields:
      title: { selector: "a.title" }
      details: { selector: "a.title", attr: href, type: url }
    required: [title, details]
download:
  resolve_on: grab
  from: details
  select: season_episode
  before:
    extract:
      token: { selector: 'input[name="form_token"]', attr: value }
    action:
      post: /thanks.php
      form:
        t: "1"
        thanks: "9"
        form_token: "{{ token }}"
  extract:
    - magnet: { selector: 'a[href^="magnet:"]', attr: href }
      all: true
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	transport := NewTransport(TransportConfig{AllowPrivate: true, Clock: newDefinitionClock()})
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Transport:  transport,
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}

	items, err := connector.Search(context.Background(), SearchRequest{
		Mode: ModeTV, Query: "FBI Most Wanted", Season: 1, Episode: 2,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	if got := topicGets.Load(); got != 0 {
		t.Fatalf("search opened the topic %d times, want 0", got)
	}
	if got := thanksPosts.Load(); got != 0 {
		t.Fatalf("search thanked %d times, want 0", got)
	}
	if items[0].Attributes["season"] != "1" || items[0].Attributes["episode"] != "2" {
		t.Errorf("attributes = %v", items[0].Attributes)
	}

	payload, err := connector.Download(context.Background(), items[0])
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !strings.Contains(payload.Magnet, hashS01E02) {
		t.Errorf("magnet = %q, want the S01E02 hash", payload.Magnet)
	}
	if got := thanksPosts.Load(); got != 1 {
		t.Errorf("thanks POSTs = %d, want 1", got)
	}
	if formSeen.Get("form_token") != "synthetic-token" || formSeen.Get("thanks") != "9" {
		t.Errorf("thank form = %v", formSeen)
	}

	// The topic now shows its magnets: a second grab must not thank again.
	payload, err = connector.Download(context.Background(), items[0])
	if err != nil {
		t.Fatalf("second Download: %v", err)
	}
	if !strings.Contains(payload.Magnet, hashS01E02) {
		t.Errorf("second magnet = %q", payload.Magnet)
	}
	if got := thanksPosts.Load(); got != 1 {
		t.Errorf("thanks POSTs after the unlocked grab = %d, want 1", got)
	}
}

func TestParseReleaseNameSelectsSeasonAndEpisode(t *testing.T) {
	cases := []struct {
		name    string
		season  int
		episode int
	}{
		{"Show.S04E01.1080p.mkv", 4, 1},
		{"Show-1x02-Conseguenze.mkv", 1, 2},
		{"Show - Stagione 5 Ep. 01-08 1080p", 5, 8},
		{"Show S03 [COMPLETA] 2160p", 3, 0},
	}
	for _, tc := range cases {
		match := parseReleaseName(tc.name)
		if !match.valid || match.season != tc.season {
			t.Errorf("%q: match = %+v", tc.name, match)
		}
		if tc.episode > 0 && (match.episodeStart > tc.episode || (match.episodeEnd > 0 && match.episodeEnd < tc.episode)) {
			t.Errorf("%q: episode range %d-%d does not contain %d", tc.name, match.episodeStart, match.episodeEnd, tc.episode)
		}
	}
}

// A site read through a FlareSolverr session moves to its mirror too: the
// browser reports a dead domain as net::ERR_NAME_NOT_RESOLVED inside an
// HTTP 500 from FlareSolverr.
func TestPersistentSessionMovesToTheMirrorWhenTheDomainIsGone(t *testing.T) {
	fake := newFakeFlareSolverr(t)
	fake.dead = map[string]bool{"site.invalid": true}
	fake.pages["https://mirror.invalid/ucp.php?mode=login"] = `<html><form action="./ucp.php"><input type="hidden" name="form_token" value="t"><input type="hidden" name="sid" value="s"></form></html>`
	fake.postPage = `<html><a class="logout" href="/logout">Logout</a></html>`
	fake.pages["https://mirror.invalid/search.php?keywords=Reacher"] = `<html><ul><li class="row"><a class="topictitle" href="/viewtopic.php?t=1">Reacher - Stagione 3</a></li></ul></html>`

	def, err := definition.Parse([]byte(strings.Replace(sessionDefinitionYAML,
		`urls: ["https://site.invalid/"]`, `urls: ["https://site.invalid/", "https://mirror.invalid/"]`, 1)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	def.Session = &definition.Session{Mode: "flaresolverr", Endpoint: fake.server.URL, NamePrefix: "mirror-idx"}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic", "password": "synthetic-password"},
		Transport:  NewTransport(TransportConfig{AllowPrivate: true, Clock: newDefinitionClock()}),
		Sessions:   NewSessionRegistry(),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	items, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "Reacher"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 || !strings.HasPrefix(items[0].DetailsURL, "https://mirror.invalid/") {
		t.Fatalf("items = %+v, want the mirror", items)
	}
	if connector.mirror.Load() != 1 {
		t.Errorf("mirror = %d, want 1", connector.mirror.Load())
	}
}
