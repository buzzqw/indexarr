package catalog_test

// Offline checks for DDUnlimited. The search fixture is a sanitized capture of
// the phpBB search results, the topic fixture is synthetic (valid MD4 hashes,
// no real links) and contains three files so the expansion path is exercised
// end to end: one topic must become three eD2k releases.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
)

func TestDDUnlimitedFixtures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ucp.php":
			if r.Method == http.MethodPost {
				serveFile(t, w, "ddunlimited/login-ok.html")
				return
			}
			serveFile(t, w, "ddunlimited/login.html")
		case "/search.php":
			serveFile(t, w, "ddunlimited/search.html")
		case "/viewtopic.php":
			switch r.URL.Query().Get("t") {
			case "61463", "3941485":
				serveFile(t, w, "ddunlimited/index.html")
			default:
				serveFile(t, w, "ddunlimited/topic.html")
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "ddunlimited")
	// The fixture server speaks plain HTTP: the FlareSolverr session is not
	// part of this offline extraction test.
	def.Session = nil
	def.Meta.URLs[0] = server.URL + "/"
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "bones", Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	wantHashes := []string{
		"0123456789abcdef0123456789abcdef",
		"11111111111111111111111111111111",
		"22222222222222222222222222222222",
	}
	for i, item := range items {
		if item.Ed2k == "" || item.Ed2kHash != wantHashes[i] {
			t.Errorf("item %d = %+v", i, item)
		}
		if item.Size != int64(1234567890+i) {
			t.Errorf("item %d size = %d", i, item.Size)
		}
		if !strings.Contains(item.Title, "Bones") {
			t.Errorf("item %d title = %q", i, item.Title)
		}
	}
	payload, err := connector.Download(context.Background(), items[2])
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if payload.Ed2k != items[2].Ed2k {
		t.Errorf("grab returned %q, want %q", payload.Ed2k, items[2].Ed2k)
	}
}

func TestDDUnlimitedTorrentFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ucp.php":
			if r.Method == http.MethodPost {
				serveFile(t, w, "ddunlimited/login-ok.html")
				return
			}
			serveFile(t, w, "ddunlimited/login.html")
		case "/search.php":
			serveFile(t, w, "ddunlimited/search.html")
		case "/viewtopic.php":
			switch r.URL.Query().Get("t") {
			case "61463", "3941485":
				serveFile(t, w, "ddunlimited/index.html")
			default:
				serveFile(t, w, "ddunlimited/topic-torrent.html")
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "ddunlimited")
	def.Session = nil
	def.Meta.URLs[0] = server.URL + "/"
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "souls", Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].Magnet == "" {
		t.Errorf("magnet missing: %+v", items[0])
	}
	if strings.Contains(items[0].DownloadURL, ".torrent") {
		t.Errorf("the .torrent should not win over the magnet: %+v", items[0])
	}
}

// The forum keeps one session per account: a login from another browser drops
// ours, and a topic then answers 200 as a guest page without links. The
// logout marker (auth.logged_in) is missing, so the engine logs in once more
// and reads the topic again instead of losing every result.
func TestDDUnlimitedLogsInAgainWhenTheForumDropsTheSession(t *testing.T) {
	var logins, guestPages atomic.Int32
	dropped := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ucp.php":
			if r.Method == http.MethodPost {
				if logins.Add(1) == 1 {
					// Another browser logs in with the same account right after.
					dropped.Store(true)
				} else {
					dropped.Store(false)
				}
				serveFile(t, w, "ddunlimited/login-ok.html")
				return
			}
			serveFile(t, w, "ddunlimited/login.html")
		case "/viewtopic.php":
			switch r.URL.Query().Get("t") {
			case "61463", "3941485":
				serveFile(t, w, "ddunlimited/index.html")
			default:
				if dropped.Load() {
					guestPages.Add(1)
					_, _ = w.Write([]byte(`<html><body><p>Devi essere registrato per vedere i link.</p></body></html>`))
					return
				}
				serveFile(t, w, "ddunlimited/topic.html")
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "ddunlimited")
	def.Session = nil
	def.Meta.URLs[0] = server.URL + "/"
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "bones", Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 || items[0].Ed2k == "" {
		t.Fatalf("items = %d (%+v), want the three eD2k files after a new login", len(items), items)
	}
	if logins.Load() != 2 || guestPages.Load() != 1 {
		t.Errorf("logins = %d, guest pages = %d; want 2 and 1", logins.Load(), guestPages.Load())
	}
}

// The forum search has a flood control: one search every few minutes, then a
// 200 "Informazione" box. Gextto asks the same series once per episode, so the
// result pages are reused (search.cache) and a refusal is an error instead of
// an empty result. A search answered as a guest (the account logged in from
// another browser) logs in again and is read once more.
func TestDDUnlimitedSearchCacheFloodControlAndGuestSearch(t *testing.T) {
	var searches, logins atomic.Int32
	guest := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ucp.php":
			if r.Method == http.MethodPost {
				logins.Add(1)
				guest.Store(false)
				serveFile(t, w, "ddunlimited/login-ok.html")
				return
			}
			serveFile(t, w, "ddunlimited/login.html")
		case "/search.php":
			searches.Add(1)
			switch {
			case guest.Load():
				_, _ = w.Write([]byte(`<html><body><div id="message"><div class="inner"><h2>Informazione</h2><p>Spiacente ma non ti è permesso di utilizzare il sistema di ricerca.</p></div></div><a href="./ucp.php?mode=login">Login</a></body></html>`))
			case strings.Contains(r.URL.Query().Get("keywords"), "S13"):
				// The forum never finds a marker in a topic title.
				t.Errorf("keywords = %q, want the title without the episode marker", r.URL.Query().Get("keywords"))
			case r.URL.Query().Get("keywords") == "nothing" || r.URL.Query().Get("keywords") == "nothing marker":
				_, _ = w.Write([]byte(`<html><body><a href="./ucp.php?mode=logout&amp;sid=0">Esci</a><div id="message"><div class="inner"><h2>Informazione</h2><p>Nessun argomento o messaggio con questo criterio di ricerca.</p></div></div></body></html>`))
			case r.URL.Query().Get("keywords") == "flood":
				_, _ = w.Write([]byte(`<html><body><a href="./ucp.php?mode=logout&amp;sid=0">Esci</a><div id="message"><div class="inner"><h2>Informazione</h2><p>Al momento non ti è permesso fare ricerche. Attendi qualche minuto oppure naviga nelle liste presenti in ogni sezione.</p></div></div></body></html>`))
			default:
				serveFile(t, w, "ddunlimited/search.html")
			}
		case "/viewtopic.php":
			switch r.URL.Query().Get("t") {
			case "61463", "3941485", "3941474", "3941473", "3941480":
				serveFile(t, w, "ddunlimited/index.html")
			default:
				serveFile(t, w, "ddunlimited/topic.html")
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "ddunlimited")
	def.Session = nil
	def.Meta.URLs[0] = server.URL + "/"
	// The flood interval has its own test: here the searches go one after
	// the other.
	def.Search.Interval = 0
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	ctx := context.Background()
	request := engine.SearchRequest{Mode: engine.ModeSearch, Query: "marvel", Limit: 1, SkipDetails: true}

	first, err := connector.Search(ctx, request)
	if err != nil || len(first) == 0 {
		t.Fatalf("first search: %d items, err %v", len(first), err)
	}
	before := searches.Load()
	second, err := connector.Search(ctx, request)
	if err != nil || len(second) != len(first) {
		t.Fatalf("second search: %d items, err %v; want %d", len(second), err, len(first))
	}
	if searches.Load() != before {
		t.Errorf("the same search asked the forum again (%d -> %d requests); want the cached pages", before, searches.Load())
	}

	_, err = connector.Search(ctx, engine.SearchRequest{Mode: engine.ModeSearch, Query: "flood", Limit: 1, SkipDetails: true})
	if err == nil || !strings.Contains(err.Error(), "Attendi qualche minuto") {
		t.Fatalf("flood control err = %v, want the forum message", err)
	}

	// The same box with "no results" is an empty answer, not a refusal.
	items, err := connector.Search(ctx, engine.SearchRequest{Mode: engine.ModeSearch, Query: "nothing", Limit: 1, SkipDetails: true})
	if err != nil || len(items) != 0 {
		t.Fatalf("no-results page: %d items, err %v; want none and no error", len(items), err)
	}

	// A free-text episode search sends the title alone: "S13E01" in the
	// keywords matched no topic at all.
	asked := searches.Load()
	if _, err := connector.Search(ctx, engine.SearchRequest{Mode: engine.ModeSearch, Query: "nothing marker S13E01", Limit: 1, SkipDetails: true}); err != nil {
		t.Fatalf("episode search: %v", err)
	}
	if searches.Load() != asked+1 {
		t.Fatalf("episode search asked the forum %d times, want once", searches.Load()-asked)
	}

	loginsBefore := logins.Load()
	guest.Store(true)
	items, err = connector.Search(ctx, engine.SearchRequest{Mode: engine.ModeSearch, Query: "guest", Limit: 1, SkipDetails: true})
	if err != nil || len(items) == 0 {
		t.Fatalf("guest search: %d items, err %v; want the results after a new login", len(items), err)
	}
	if logins.Load() != loginsBefore+1 {
		t.Errorf("logins = %d, want one more than %d", logins.Load(), loginsBefore)
	}
}

// The forum accepts one search per account every 57-58 s. search.interval
// keeps the requests apart; a search without a slot before its deadline is not
// sent, it is queued, the background worker sends it at its turn, and the next
// identical request reads it from the cache.
func TestDDUnlimitedFloodIntervalQueuesTheSearchesWithoutASlot(t *testing.T) {
	def := parseDefinition(t, "ddunlimited")
	if got := def.Search.Interval.Duration(); got != 65*time.Second {
		t.Fatalf("ddunlimited search.interval = %s, want 65s", got)
	}
	const interval = 300 * time.Millisecond
	var mu sync.Mutex
	var sent []time.Time
	var eventsMu sync.Mutex
	var events []engine.DeferredSearchEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ucp.php":
			if r.Method == http.MethodPost {
				serveFile(t, w, "ddunlimited/login-ok.html")
				return
			}
			serveFile(t, w, "ddunlimited/login.html")
		case "/search.php":
			mu.Lock()
			sent = append(sent, time.Now())
			mu.Unlock()
			serveFile(t, w, "ddunlimited/search.html")
		case "/viewtopic.php":
			serveFile(t, w, "ddunlimited/index.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	def.Session = nil
	def.Meta.URLs[0] = server.URL + "/"
	def.Search.Interval = definition.Duration(interval)
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
		DeferredSearch: func(event engine.DeferredSearchEvent) {
			eventsMu.Lock()
			events = append(events, event)
			eventsMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(sent)
	}
	search := func(ctx context.Context, query string) ([]engine.Item, error) {
		return connector.Search(ctx, engine.SearchRequest{Mode: engine.ModeSearch, Query: query, Limit: 1, FirstPageOnly: true, SkipDetails: true})
	}

	// A fresh connector (no search sent yet) with five searches at once: the
	// lane takes the ones that fit in two intervals, the others are queued
	// at once instead of waiting until their deadline.
	var wg sync.WaitGroup
	var deferredNow atomic.Int32
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := search(context.Background(), fmt.Sprintf("burst%d", i)); errors.Is(err, engine.ErrSearchDeferred) {
				deferredNow.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := deferredNow.Load(); got != 2 {
		t.Fatalf("burst of five on a fresh connector: %d deferred, want 2 (three fit in two intervals)", got)
	}
	// Let the background worker finish the two queued ones.
	deadline0 := time.Now().Add(5 * time.Second)
	for count() < 5 && time.Now().Before(deadline0) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(interval + 100*time.Millisecond)
	mu.Lock()
	sent = sent[:0]
	mu.Unlock()

	if items, err := search(context.Background(), "first"); err != nil || len(items) == 0 {
		t.Fatalf("first search: %d items, err %v", len(items), err)
	}
	// Without a deadline the second search waits for its slot.
	if items, err := search(context.Background(), "second"); err != nil || len(items) == 0 {
		t.Fatalf("second search: %d items, err %v", len(items), err)
	}
	// A caller that cannot wait (its deadline is closer than the slot plus
	// the margin to read the page) gets the search deferred, not refused.
	short, cancel := context.WithTimeout(context.Background(), time.Second)
	_, err = search(short, "third")
	cancel()
	if !errors.Is(err, engine.ErrSearchDeferred) || !strings.Contains(err.Error(), "queued") {
		t.Fatalf("third search err = %v, want it deferred and queued", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for count() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if count() != 3 {
		t.Fatalf("search.php requests = %d, want 3 (the deferred one sent in the background)", count())
	}
	// The server counts the request before the worker has read the answer
	// and stored it.
	time.Sleep(300 * time.Millisecond)
	if items, err := search(context.Background(), "third"); err != nil || len(items) == 0 {
		t.Fatalf("third search again: %d items, err %v; want the page the worker kept", len(items), err)
	}
	if count() != 3 {
		t.Errorf("search.php requests = %d after the repeat, want 3: the repeat reads the cache", count())
	}
	// Page two of a search would need a slot of its own: the first page is
	// returned now and page two is fetched in the background, so the repeat
	// gets both pages without asking the forum again.
	time.Sleep(interval)
	full := engine.SearchRequest{Mode: engine.ModeSearch, Query: "pages", Limit: 100, SkipDetails: true}
	firstPages, err := connector.Search(context.Background(), full)
	if err != nil || len(firstPages) == 0 {
		t.Fatalf("two-page search: %d items, err %v", len(firstPages), err)
	}
	if count() != 4 {
		t.Fatalf("search.php requests = %d after the two-page search, want 4 (page two deferred)", count())
	}
	deadline = time.Now().Add(5 * time.Second)
	for count() < 5 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if count() != 5 {
		t.Fatalf("search.php requests = %d, want 5 (page two sent in the background)", count())
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := connector.Search(context.Background(), full); err != nil || count() != 5 {
		t.Fatalf("two-page repeat: err %v, requests %d; want both pages from the cache", err, count())
	}

	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(sent); i++ {
		if gap := sent[i].Sub(sent[i-1]); gap < interval-20*time.Millisecond {
			t.Errorf("requests %d and %d are %s apart, want at least %s", i, i+1, gap, interval)
		}
	}
	// Every send the background worker made is reported, so the Manager can
	// write it in the history: the caller only saw the deferred row.
	eventsMu.Lock()
	defer eventsMu.Unlock()
	seen := map[string]bool{}
	for _, event := range events {
		if event.Query != "third" && event.Query != "pages" {
			continue
		}
		if event.Err != nil {
			t.Errorf("background send of %q failed: %v", event.Query, event.Err)
			continue
		}
		if event.Elapsed <= 0 || event.Attempt < 1 {
			t.Errorf("background send of %q: elapsed %s, attempt %d", event.Query, event.Elapsed, event.Attempt)
		}
		seen[event.Query] = true
	}
	for _, query := range []string{"third", "pages"} {
		if !seen[query] {
			t.Errorf("no background send event for %q: events %+v", query, events)
		}
	}
}

// A background send can meet the forum while a burst block is on. The worker
// retries the refusal after a longer pause, and the page lands in the cache
// for the next identical request.
// Dropping a connector (indexer disabled, deleted or definition changed) must
// stop its background queue: otherwise it can keep searching for hours.
func TestDDUnlimitedCloseStopsTheDeferredQueue(t *testing.T) {
	const interval = 200 * time.Millisecond
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ucp.php":
			if r.Method == http.MethodPost {
				serveFile(t, w, "ddunlimited/login-ok.html")
				return
			}
			serveFile(t, w, "ddunlimited/login.html")
		case "/search.php":
			mu.Lock()
			requests++
			mu.Unlock()
			serveFile(t, w, "ddunlimited/search.html")
		case "/viewtopic.php":
			serveFile(t, w, "ddunlimited/index.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "ddunlimited")
	def.Session = nil
	def.Meta.URLs[0] = server.URL + "/"
	def.Search.Interval = definition.Duration(interval)
	def.Search.Cache = definition.Duration(time.Minute)
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	request := engine.SearchRequest{Mode: engine.ModeSearch, Query: "close", Limit: 1, FirstPageOnly: true, SkipDetails: true}
	if _, err := connector.Search(context.Background(), request); err != nil {
		t.Fatalf("first search: %v", err)
	}
	queued := request
	queued.Query = "close-queued"
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err = connector.Search(short, queued)
	cancel()
	if !errors.Is(err, engine.ErrSearchDeferred) {
		t.Fatalf("second search err = %v, want ErrSearchDeferred", err)
	}

	connector.Close()
	// Wait past the flood interval: without Close the worker would spend the
	// turn and send a second search.php request.
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("search.php requests after Close = %d, want 1", requests)
	}
}

// The daily query limit is checked again for background sends: pages queued
// before the limit was reached must not bypass it.
func TestDDUnlimitedDeferredQueueStopsWhenTheQueryBudgetIsGone(t *testing.T) {
	const interval = 200 * time.Millisecond
	var mu sync.Mutex
	requests := 0
	var checked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ucp.php":
			if r.Method == http.MethodPost {
				serveFile(t, w, "ddunlimited/login-ok.html")
				return
			}
			serveFile(t, w, "ddunlimited/login.html")
		case "/search.php":
			mu.Lock()
			requests++
			mu.Unlock()
			serveFile(t, w, "ddunlimited/search.html")
		case "/viewtopic.php":
			serveFile(t, w, "ddunlimited/index.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "ddunlimited")
	def.Session = nil
	def.Meta.URLs[0] = server.URL + "/"
	def.Search.Interval = definition.Duration(interval)
	def.Search.Cache = definition.Duration(time.Minute)
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
		DeferredSearchAllowed: func(context.Context) error {
			checked.Add(1)
			return errors.New("daily query limit reached")
		},
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	request := engine.SearchRequest{Mode: engine.ModeSearch, Query: "budget", Limit: 1, FirstPageOnly: true, SkipDetails: true}
	if _, err := connector.Search(context.Background(), request); err != nil {
		t.Fatalf("first search: %v", err)
	}
	queued := request
	queued.Query = "budget-queued"
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err = connector.Search(short, queued)
	cancel()
	if !errors.Is(err, engine.ErrSearchDeferred) {
		t.Fatalf("second search err = %v, want ErrSearchDeferred", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for checked.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if checked.Load() == 0 {
		t.Fatal("the background worker did not check the query budget")
	}
	if requests != 1 {
		t.Fatalf("search.php requests = %d, want 1: the budget dropped the queued page", requests)
	}
}

func TestDDUnlimitedBackgroundSendRetriesPastABurstBlock(t *testing.T) {
	const interval = 200 * time.Millisecond
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ucp.php":
			if r.Method == http.MethodPost {
				serveFile(t, w, "ddunlimited/login-ok.html")
				return
			}
			serveFile(t, w, "ddunlimited/login.html")
		case "/search.php":
			mu.Lock()
			requests++
			n := requests
			mu.Unlock()
			if n <= 1 {
				_, _ = w.Write([]byte(`<html><body><a href="./ucp.php?mode=logout&amp;sid=0">Esci</a><div id="message"><div class="inner"><h2>Informazione</h2><p>Al momento non ti è permesso fare ricerche. Attendi qualche minuto.</p></div></div></body></html>`))
				return
			}
			serveFile(t, w, "ddunlimited/search.html")
		case "/viewtopic.php":
			serveFile(t, w, "ddunlimited/index.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "ddunlimited")
	def.Session = nil
	def.Meta.URLs[0] = server.URL + "/"
	def.Search.Interval = definition.Duration(interval)
	def.Search.Cache = definition.Duration(time.Minute)
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
		Transport:  engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	request := engine.SearchRequest{Mode: engine.ModeSearch, Query: "retry", Limit: 1, FirstPageOnly: true, SkipDetails: true}

	// A deadline this short leaves no turn: the search goes to the queue.
	short, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err = connector.Search(short, request)
	cancel()
	if !errors.Is(err, engine.ErrSearchDeferred) {
		t.Fatalf("first search err = %v, want ErrSearchDeferred", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		done := requests >= 2
		mu.Unlock()
		if done {
			break
		}
		if time.Now().After(deadline) {
			mu.Lock()
			t.Fatalf("search.php requests = %d, want 4 retries past the block", requests)
			mu.Unlock()
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	items, err := connector.Search(context.Background(), request)
	if err != nil || len(items) == 0 {
		t.Fatalf("cached search after the retries: %d items, err %v", len(items), err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 {
		t.Errorf("search.php requests = %d, want 2: the cached page must not ask again", requests)
	}
}
