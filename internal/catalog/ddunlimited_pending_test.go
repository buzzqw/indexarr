package catalog_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
)

// pendingRecorder keeps the last queue a connector saved and how many saves
// it made.
type pendingRecorder struct {
	mu    sync.Mutex
	queue []engine.PendingSearch
	saves int
}

func (r *pendingRecorder) save(queue []engine.PendingSearch) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queue = append([]engine.PendingSearch(nil), queue...)
	r.saves++
}

func (r *pendingRecorder) last() ([]engine.PendingSearch, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]engine.PendingSearch(nil), r.queue...), r.saves
}

// TestDDUnlimitedDeferredQueueSurvivesARestart: on 2026-10-29, 43 of the 374
// searches DDUnlimited deferred in three days were lost because the service
// restarted before their turn. The queue is saved on every change, Close (it
// runs at shutdown too) keeps the saved copy, and the next connector resumes
// it: the page is sent once and the next identical request reads the cache.
func TestDDUnlimitedDeferredQueueSurvivesARestart(t *testing.T) {
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
	sent := func() int {
		mu.Lock()
		defer mu.Unlock()
		return requests
	}

	build := func(saved *pendingRecorder) *engine.DefinitionConnector {
		t.Helper()
		def := parseDefinition(t, "ddunlimited")
		def.Session = nil
		def.Meta.URLs[0] = server.URL + "/"
		def.Search.Interval = definition.Duration(interval)
		def.Search.Cache = definition.Duration(time.Minute)
		connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
			Indexer:             engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
			Definition:          def,
			Settings:            map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
			Transport:           engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
			SavePendingSearches: saved.save,
		})
		if err != nil {
			t.Fatalf("NewDefinitionConnector: %v", err)
		}
		return connector
	}

	before := &pendingRecorder{}
	first := build(before)
	request := engine.SearchRequest{Mode: engine.ModeSearch, Query: "restart", Limit: 1, FirstPageOnly: true, SkipDetails: true}
	if _, err := first.Search(context.Background(), request); err != nil {
		t.Fatalf("first search: %v", err)
	}
	queued := request
	queued.Query = "restart-queued"
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err := first.Search(short, queued)
	cancel()
	if !errors.Is(err, engine.ErrSearchDeferred) {
		t.Fatalf("second search err = %v, want ErrSearchDeferred", err)
	}
	// Shut down before the page's turn: the saved queue must still hold it.
	first.Close()
	time.Sleep(2 * interval)
	saved, _ := before.last()
	if len(saved) != 1 || saved[0].Request.Query != "restart-queued" || saved[0].QueuedAt.IsZero() {
		t.Fatalf("saved queue after Close = %+v, want the deferred page", saved)
	}
	if got := sent(); got != 1 {
		t.Fatalf("search.php requests after Close = %d, want 1", got)
	}

	after := &pendingRecorder{}
	second := build(after)
	t.Cleanup(second.Close)
	second.ResumePendingSearches(saved)
	deadline := time.Now().Add(5 * time.Second)
	for {
		queue, saves := after.last()
		if sent() == 2 && saves > 0 && len(queue) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resumed queue not sent: requests=%d saved=%+v", sent(), queue)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The resumed page filled the cache: the client's next try is free.
	if _, err := second.Search(context.Background(), queued); err != nil {
		t.Fatalf("search after resume: %v", err)
	}
	if got := sent(); got != 2 {
		t.Fatalf("search.php requests after the cached repeat = %d, want 2", got)
	}
}

// A page older than search.cache is not resumed: its answer would already
// have expired from the cache it was meant to fill.
func TestDDUnlimitedResumeDropsStalePages(t *testing.T) {
	def := parseDefinition(t, "ddunlimited")
	def.Session = nil
	def.Meta.URLs[0] = "http://127.0.0.1:1/"
	def.Search.Interval = definition.Duration(time.Hour)
	def.Search.Cache = definition.Duration(time.Minute)
	saved := &pendingRecorder{}
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:             engine.IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition:          def,
		Settings:            map[string]string{"username": "synthetic-user", "password": "synthetic-password"},
		Transport:           engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
		SavePendingSearches: saved.save,
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	t.Cleanup(connector.Close)
	connector.ResumePendingSearches([]engine.PendingSearch{{
		Request:  engine.SearchRequest{Mode: engine.ModeSearch, Query: "stale"},
		Page:     1,
		Param:    1,
		QueuedAt: time.Now().Add(-2 * time.Minute),
	}})
	queue, saves := saved.last()
	if saves != 1 || len(queue) != 0 {
		t.Fatalf("after resuming a stale page: saves=%d queue=%+v, want one empty save", saves, queue)
	}
}
