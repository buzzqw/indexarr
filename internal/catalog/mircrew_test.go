package catalog_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

// TestMirCrewFixtures exercises the native MirCrew selectors on the sanitized
// fixtures. It disables the persistent session on purpose: the FlareSolverr
// mechanics have their own tests in internal/engine. The topic fixture is a
// first post that was already thanked, and the locked one is derived from it,
// so this test never performs an engagement action against the real forum.
func TestMirCrewFixtures(t *testing.T) {
	var (
		topicGets   atomic.Int32
		thanksGets  atomic.Int32
		topicLocked = true
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/search.php":
			serveFile(t, w, "mircrew/search.html")
		case r.URL.Path == "/topic/1":
			topicGets.Add(1)
			if topicLocked {
				serveFile(t, w, "mircrew/topic-locked.html")
				return
			}
			serveFile(t, w, "mircrew/topic.html")
		case r.URL.Path == "/thanks.php":
			thanksGets.Add(1)
			topicLocked = false
			_, _ = w.Write([]byte(`<html><body>Grazie</body></html>`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	def := parseDefinition(t, "mircrew")
	def.Meta.URLs[0] = server.URL + "/"
	def.Session = nil // exercised separately by internal/engine
	def.Auth = nil
	def.Search.Request.Params = map[string]string{
		"keywords": "{{ query.keywords_raw }}",
		"sf":       "titleonly",
		"sr":       "topics",
	}
	connector, err := engine.NewDefinitionConnector(engine.DefinitionConfig{
		Indexer:    engine.IndexerRef{Slug: "mircrew", Name: "MirCrew"},
		Definition: def,
		Settings: map[string]string{
			"username":     "synthetic-user",
			"password":     "synthetic-password",
			"flaresolverr": server.URL,
		},
		Transport: engine.NewTransport(engine.TransportConfig{AllowPrivate: true}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}

	items, err := connector.Search(context.Background(), engine.SearchRequest{
		Mode: engine.ModeTV, Query: "Reacher", Season: 3, Episode: 2,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) < 2 {
		t.Fatalf("items = %d, want at least 2", len(items))
	}
	for _, item := range items {
		if item.Title == "" || !strings.Contains(item.DetailsURL, "viewtopic.php") {
			t.Errorf("incomplete row: %+v", item)
		}
		if len(item.Categories) != 1 || item.Categories[0] != 5040 {
			t.Errorf("%q: categories = %v, want [5040]", item.Title, item.Categories)
		}
		if item.Published.IsZero() {
			t.Errorf("%q: date was not parsed", item.Title)
		}
	}
	if topicGets.Load() != 0 || thanksGets.Load() != 0 {
		t.Fatalf("search touched a topic (%d) or thanked (%d)", topicGets.Load(), thanksGets.Load())
	}

	items[0].DetailsURL = server.URL + "/topic/1"
	payload, err := connector.Download(context.Background(), items[0])
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if !strings.Contains(payload.Magnet, "d7b4a968c0f851017becb69e3e1e1e9d03f35275") {
		t.Errorf("magnet = %q, want the S03E02 hash", payload.Magnet)
	}
	if thanksGets.Load() != 1 {
		t.Errorf("thanks requests = %d, want 1", thanksGets.Load())
	}
	if topicGets.Load() != 2 {
		t.Errorf("topic requests = %d, want 2 (locked then unlocked)", topicGets.Load())
	}
}
