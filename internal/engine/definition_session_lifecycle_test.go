package engine

import (
	"context"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
)

// sessionPages registers the smallest set of pages a persistent-session search
// needs: the login form, the logged-in page and one search result.
func sessionPages(fake *fakeFlareSolverr) {
	fake.pages["https://site.invalid/ucp.php?mode=login"] = `<html><form action="./ucp.php"><input type="hidden" name="form_token" value="t"><input type="hidden" name="sid" value="s"></form></html>`
	fake.postPage = `<html><a class="logout" href="/logout">Logout</a></html>`
	fake.pages["https://site.invalid/search.php?keywords=x"] = `<html><ul><li class="row"><a class="topictitle" href="/t/1">One</a></li></ul></html>`
}

func TestDefinitionPersistentSessionCloseDestroysBrowser(t *testing.T) {
	fake := newFakeFlareSolverr(t)
	sessionPages(fake)
	registry := NewSessionRegistry()
	connector := newSessionConnectorWithRegistry(t, fake, newDefinitionClock(), registry)
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	name := connector.flare.name
	if name == "" {
		t.Fatal("no session was created")
	}
	if !registry.has(name) {
		t.Fatal("the live session is not registered")
	}

	if err := connector.CloseSession(context.Background()); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	fake.mu.Lock()
	if fake.sessions[name] {
		t.Errorf("session %q is still alive after CloseSession", name)
	}
	fake.mu.Unlock()
	if registry.has(name) {
		t.Error("the closed session is still registered")
	}

	// The connector stays usable: the next request creates a new browser.
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"}); err != nil {
		t.Fatalf("Search after CloseSession: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.created != 2 || !fake.sessions[connector.flare.name] {
		t.Errorf("created = %d, session = %q, want a new browser", fake.created, connector.flare.name)
	}
}

func TestDefinitionPersistentSessionSweepClosesOnlyIdleBrowser(t *testing.T) {
	fake := newFakeFlareSolverr(t)
	sessionPages(fake)
	clock := newDefinitionClock()
	connector := newSessionConnectorWithRegistry(t, fake, clock, NewSessionRegistry())
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"}); err != nil {
		t.Fatalf("Search: %v", err)
	}

	clock.Advance(5 * time.Minute)
	if connector.SweepSession(context.Background()) {
		t.Fatal("a session used five minutes ago was closed")
	}
	fake.mu.Lock()
	if len(fake.sessions) != 1 {
		t.Errorf("sessions = %d, want the live one", len(fake.sessions))
	}
	fake.mu.Unlock()

	clock.Advance(26 * time.Minute)
	if !connector.SweepSession(context.Background()) {
		t.Fatal("an idle session was not closed")
	}
	fake.mu.Lock()
	if len(fake.sessions) != 0 {
		t.Errorf("sessions = %d after the sweep, want 0", len(fake.sessions))
	}
	fake.mu.Unlock()

	if connector.SweepSession(context.Background()) {
		t.Error("the sweep reported a second closed browser")
	}
	// A request after the sweep recreates the browser.
	if _, err := connector.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"}); err != nil {
		t.Fatalf("Search after the sweep: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.created != 2 {
		t.Errorf("created = %d, want 2", fake.created)
	}
}

func TestDefinitionPersistentSessionOrphanCleanupKeepsLiveSession(t *testing.T) {
	fake := newFakeFlareSolverr(t)
	sessionPages(fake)
	registry := NewSessionRegistry()
	clock := newDefinitionClock()

	first := newSessionConnectorWithRegistry(t, fake, clock, registry)
	if _, err := first.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"}); err != nil {
		t.Fatalf("first Search: %v", err)
	}
	firstName := first.flare.name

	// A second connector for the same definition shares the ownership prefix.
	// Its orphan cleanup must leave the session of the first one alone.
	second := newSessionConnectorWithRegistry(t, fake, clock, registry)
	if _, err := second.Search(context.Background(), SearchRequest{Mode: ModeSearch, Query: "x"}); err != nil {
		t.Fatalf("second Search: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !fake.sessions[firstName] {
		t.Error("the live session of the first connector was destroyed as an orphan")
	}
	if fake.created != 2 {
		t.Errorf("created = %d, want 2 (one browser per connector)", fake.created)
	}
	if !registry.has(firstName) || !registry.has(second.flare.name) {
		t.Error("the registry does not track both live sessions")
	}
}

func TestDefinitionPersistentSessionIdleTimeoutFromDefinition(t *testing.T) {
	def, err := definition.Parse([]byte(sessionDefinitionYAML))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	def.Session = &definition.Session{
		Mode:        "flaresolverr",
		Endpoint:    "http://127.0.0.1:8191/v1",
		IdleTimeout: definition.Duration(45 * time.Minute),
	}
	connector, err := NewDefinitionConnector(DefinitionConfig{
		Indexer:    IndexerRef{Slug: def.Meta.ID, Name: def.Meta.Name},
		Definition: def,
		Settings:   map[string]string{"username": "u", "password": "p"},
		Transport:  NewTransport(TransportConfig{Clock: newDefinitionClock()}),
	})
	if err != nil {
		t.Fatalf("NewDefinitionConnector: %v", err)
	}
	if connector.flare.idle != 45*time.Minute {
		t.Errorf("idle = %v, want 45m", connector.flare.idle)
	}
}
