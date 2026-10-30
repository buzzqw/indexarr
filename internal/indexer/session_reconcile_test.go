package indexer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

// fakeSessionSolver is the smallest FlareSolverr session API: list and
// destroy. It records the sessions it was asked to close.
type fakeSessionSolver struct {
	mu       sync.Mutex
	sessions map[string]bool
	destroy  []string
}

func newFakeSessionSolver(t *testing.T, names ...string) (*fakeSessionSolver, *httptest.Server) {
	t.Helper()
	fake := &fakeSessionSolver{sessions: make(map[string]bool, len(names))}
	for _, name := range names {
		fake.sessions[name] = true
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Cmd     string `json:"cmd"`
			Session string `json:"session"`
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
		case "sessions.destroy":
			delete(fake.sessions, payload.Session)
			fake.destroy = append(fake.destroy, payload.Session)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			t.Errorf("unexpected FlareSolverr command %q", payload.Cmd)
		}
	}))
	t.Cleanup(server.Close)
	return fake, server
}

func (f *fakeSessionSolver) left() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.sessions))
	for name := range f.sessions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

const reconcileDefaultDefinitionYAML = `schema: 1
meta:
  id: sess-default
  name: Session default
  type: private
  protocol: torrent
  urls: ["https://site.invalid/"]
session:
  mode: flaresolverr
capabilities:
  search: [q]
search:
  request:
    get: /search.php
  response:
    type: html
    rows: { selector: "li.row" }
    fields:
      title: { selector: "a" }
      details: { selector: "a", attr: href, type: url }
    required: [title, details]
`

const reconcileCustomDefinitionYAML = `schema: 1
meta:
  id: sess-custom
  name: Session custom
  type: private
  protocol: torrent
  urls: ["https://site.invalid/"]
session:
  mode: flaresolverr
  name_prefix: custom-idx
capabilities:
  search: [q]
search:
  request:
    get: /search.php
  response:
    type: html
    rows: { selector: "li.row" }
    fields:
      title: { selector: "a" }
      details: { selector: "a", attr: href, type: url }
    required: [title, details]
`

// The global reconcile closes the sessions of a previous run even when the
// definition was never queried since the restart, and leaves the live session
// and another program's browsers alone.
func TestReconcileSessionsClosesOrphansFromAPreviousRun(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	fake, server := newFakeSessionSolver(t,
		"indexarr-sess-default-aaaaaa", // default prefix of a stored definition
		"custom-idx-bbbbbb",            // custom name_prefix of a stored definition
		"gextto-foreign-dddddd",        // another program on the same FlareSolverr
	)
	if err := st.SetSetting(ctx, store.KeyFlareSolverr, server.URL); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
	seedDefinition(t, st, "sess-default", "", reconcileDefaultDefinitionYAML)
	seedDefinition(t, st, "sess-custom", "", reconcileCustomDefinitionYAML)

	m := newTestManager(t, st, nil)

	if closed := m.ReconcileSessions(ctx); closed != 2 {
		t.Fatalf("closed = %d, want the two orphaned indexarr sessions", closed)
	}
	want := []string{"gextto-foreign-dddddd"}
	if got := fake.left(); len(got) != len(want) || got[0] != want[0] {
		t.Errorf("sessions left = %v, want %v", got, want)
	}
	if closed := m.ReconcileSessions(ctx); closed != 0 {
		t.Errorf("second pass closed = %d, want 0", closed)
	}
}

// Without a configured FlareSolverr the reconcile is a no-op instead of an
// error.
func TestReconcileSessionsWithoutFlareSolverrDoesNothing(t *testing.T) {
	m := newTestManager(t, newTestStore(t), nil)
	if closed := m.ReconcileSessions(context.Background()); closed != 0 {
		t.Errorf("closed = %d, want 0", closed)
	}
}
