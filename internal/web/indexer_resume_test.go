package web

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/logbuffer"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

// resumeEnv is one paused source served by a real router: the breaker is open
// (the site answers 500), so the Overview tab must offer to resume it and the
// action must clear the pause without touching anything else.
type resumeEnv struct {
	store   *store.Store
	manager *indexer.Manager
	handler http.Handler
	id      int64
}

func newResumeEnv(t *testing.T) *resumeEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "resume.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(site.Close)

	body := fmt.Sprintf(`schema: 1
meta:
  id: resume-site
  name: Resume Site
  type: public
  protocol: torrent
  urls: ["%s/"]
capabilities:
  search: [q]
search:
  request:
    get: /search
  response:
    type: html
    rows: "table.results tr"
    fields:
      title: { selector: "td.name a" }
    required: [title]
`, site.URL)
	if err := st.UpsertDefinition(ctx, store.Definition{
		ID: "resume-site", Body: body, State: "verificata", Origin: "local",
	}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateIndexer(ctx, store.Indexer{
		DefinitionID: "resume-site",
		Protocol:     "torrent",
		Slug:         "resume-site",
		Name:         "Resume Site",
		Enabled:      true,
		ConfigJSON:   fmt.Sprintf(`{"url":%q}`, site.URL),
	})
	if err != nil {
		t.Fatal(err)
	}

	key := make([]byte, 32)
	key[0] = 1
	v, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	logs := logbuffer.New(100)
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	logger := slog.New(logbuffer.NewHandler(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level}), logs))
	polite := engine.NewPoliteness(0, 1, 5, time.Minute, engine.RealClock{})
	manager, err := indexer.New(indexer.Config{
		Store:     st,
		Vault:     v,
		Transport: engine.NewTransport(engine.TransportConfig{AllowPrivate: true, FailureThreshold: 1, Cooldown: time.Hour}),
		Logger:    logger,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := New(Config{
		Store: st, Logger: logger, Logs: logs, Level: level,
		Version: "test", Started: time.Now(), Politeness: polite,
		Vault: v, Native: manager,
	})
	return &resumeEnv{store: st, manager: manager, handler: server.Handler(), id: id}
}

// pause runs the source twice: the first run fails and opens the breaker
// (threshold 1), the second one is refused before any request is sent.
func (e *resumeEnv) pause(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.manager.Run(ctx, e.id); err == nil {
		t.Fatal("the first run did not fail")
	}
	if _, err := e.manager.Run(ctx, e.id); err == nil {
		t.Fatal("the second run was not refused by the breaker")
	}
	if state := e.manager.CircuitState(e.id, "resume-site"); state != engine.BreakerOpen {
		t.Fatalf("circuit = %q, want open", state)
	}
}

// TestPausedSourceOffersResume is the visible side: while the breaker is open
// the Overview tab names the pause and offers the action, with a tooltip.
func TestPausedSourceOffersResume(t *testing.T) {
	env := newResumeEnv(t)
	env.pause(t)

	recorder := httptest.NewRecorder()
	env.handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/indexers/%d", env.id), nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	page := recorder.Body.String()
	if !strings.Contains(page, fmt.Sprintf(`action="/indexers/%d/resume"`, env.id)) {
		t.Error("the paused source does not offer the resume action")
	}
	if !strings.Contains(page, "Resume now") {
		t.Error("the resume action has no visible label")
	}
	if !strings.Contains(page, "without editing or recreating it") {
		t.Error("the resume action has no tooltip")
	}
	row, err := env.store.GetIndexer(context.Background(), env.id)
	if err != nil {
		t.Fatal(err)
	}
	until, ok := env.manager.CircuitOpenUntil(row)
	if !ok {
		t.Fatal("the live breaker reports no next retry")
	}
	if !strings.Contains(page, "Next automatic retry") {
		t.Error("the paused source does not name the next automatic retry")
	}
	if !strings.Contains(page, until.Local().Format("2006-01-02 15:04")) {
		t.Errorf("the page does not show the next retry at %s", until.Local())
	}
}

// TestResumeActionClearsThePause covers the action itself: the redirect carries
// the confirmation and the live breaker is closed, so the next request is sent.
func TestResumeActionClearsThePause(t *testing.T) {
	env := newResumeEnv(t)
	env.pause(t)

	path := fmt.Sprintf("/indexers/%d", env.id)
	token, cookie := csrfFrom(t, env.handler, path)
	recorder := postForm(t, env.handler, fmt.Sprintf("/indexers/%d/resume", env.id), url.Values{
		"csrf": {token},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	location := recorder.Header().Get("Location")
	if !strings.Contains(location, "resumed") {
		t.Errorf("location = %q, want the resume confirmation", location)
	}
	if location != path && !strings.HasPrefix(location, path+"?") {
		t.Errorf("location = %q, want back to %q", location, path)
	}
	if state := env.manager.CircuitState(env.id, "resume-site"); state != engine.BreakerClosed {
		t.Fatalf("circuit after resume = %q, want closed", state)
	}

	// The page no longer shows the pause or the action.
	page := httptest.NewRecorder()
	env.handler.ServeHTTP(page, httptest.NewRequest(http.MethodGet, path, nil))
	if strings.Contains(page.Body.String(), fmt.Sprintf(`action="/indexers/%d/resume"`, env.id)) {
		t.Error("the resume action is still offered after the reset")
	}
}

// TestResumeActionFromTheListKeepsTheList is the problem row of the indexers
// page: its action posts with the list as the return path, so the operator
// stays where they were.
func TestResumeActionFromTheListKeepsTheList(t *testing.T) {
	env := newResumeEnv(t)
	env.pause(t)

	token, cookie := csrfFrom(t, env.handler, "/indexers")
	recorder := postForm(t, env.handler, fmt.Sprintf("/indexers/%d/resume", env.id), url.Values{
		"csrf":   {token},
		"return": {"/indexers"},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); !strings.HasPrefix(got, "/indexers?") {
		t.Errorf("location = %q, want the list with the confirmation", got)
	}
	if state := env.manager.CircuitState(env.id, "resume-site"); state != engine.BreakerClosed {
		t.Fatalf("circuit after resume = %q, want closed", state)
	}
}

// TestResumeWithoutNativeFailsCleanly keeps the button safe when the runtime
// manager is missing (a page rendered by a build without the engine).
func TestResumeWithoutNativeFailsCleanly(t *testing.T) {
	env := newResumeEnv(t)
	server := New(Config{Store: env.store, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Version: "test", Started: time.Now()})
	handler := server.Handler()
	token, cookie := csrfFrom(t, handler, "/indexers")
	recorder := postForm(t, handler, fmt.Sprintf("/indexers/%d/resume", env.id), url.Values{
		"csrf": {token},
	}, cookie)
	if recorder.Code != http.StatusSeeOther {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Location"); !strings.Contains(got, "not+configured") {
		t.Errorf("location = %q, want the unavailable message", got)
	}
}
