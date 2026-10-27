package indexer

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

// fakeSessionConnector implements engine.Connector through the embedded (nil)
// interface and only records the session calls the manager makes.
type fakeSessionConnector struct {
	engine.Connector
	name    string
	idle    bool
	closes  atomic.Int32
	closed  chan struct{}
	sweptAt atomic.Int32
}

func newFakeSessionConnector(name string, idle bool) *fakeSessionConnector {
	return &fakeSessionConnector{name: name, idle: idle, closed: make(chan struct{})}
}

func (f *fakeSessionConnector) CloseSession(context.Context) error {
	if f.closes.Add(1) == 1 && f.closed != nil {
		close(f.closed)
	}
	return nil
}

func (f *fakeSessionConnector) SweepSession(context.Context) bool {
	f.sweptAt.Add(1)
	return f.idle
}

func newSessionTestManager(connectors map[int64]engine.Connector) *Manager {
	cache := make(map[int64]cachedConnector, len(connectors))
	for id, connector := range connectors {
		cache[id] = cachedConnector{connector: connector}
	}
	return &Manager{
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		cache:     cache,
		liveCache: make(map[string]liveCacheEntry),
	}
}

func waitClosed(t *testing.T, connector *fakeSessionConnector) {
	t.Helper()
	select {
	case <-connector.closed:
	case <-time.After(3 * time.Second):
		t.Fatalf("connector %q was not closed", connector.name)
	}
}

func TestInvalidateClosesTheBrowserSession(t *testing.T) {
	connector := newFakeSessionConnector("indexer-1", false)
	m := newSessionTestManager(map[int64]engine.Connector{1: connector})

	m.Invalidate(1)
	waitClosed(t, connector)
	if _, ok := m.cache[1]; ok {
		t.Error("the connector is still cached after Invalidate")
	}
}

func TestInvalidateAllClosesEveryBrowserSession(t *testing.T) {
	first := newFakeSessionConnector("indexer-1", false)
	second := newFakeSessionConnector("indexer-2", false)
	m := newSessionTestManager(map[int64]engine.Connector{1: first, 2: second})

	m.InvalidateAll()
	waitClosed(t, first)
	waitClosed(t, second)
	if len(m.cache) != 0 {
		t.Errorf("cache entries = %d, want 0", len(m.cache))
	}
}

func TestCloseAllClosesEveryBrowserSession(t *testing.T) {
	first := newFakeSessionConnector("indexer-1", false)
	second := newFakeSessionConnector("indexer-2", false)
	m := newSessionTestManager(map[int64]engine.Connector{1: first, 2: second})

	m.CloseAll(context.Background())
	waitClosed(t, first)
	waitClosed(t, second)
	if len(m.cache) != 0 {
		t.Errorf("cache entries = %d, want 0", len(m.cache))
	}
}

func TestSweepSessionsClosesOnlyIdleConnectors(t *testing.T) {
	idle := newFakeSessionConnector("idle", true)
	busy := newFakeSessionConnector("busy", false)
	m := newSessionTestManager(map[int64]engine.Connector{1: idle, 2: busy})

	if closed := m.SweepSessions(context.Background()); closed != 1 {
		t.Errorf("closed = %d, want 1", closed)
	}
	if idle.sweptAt.Load() != 1 || busy.sweptAt.Load() != 1 {
		t.Errorf("sweeps = %d/%d, want 1/1", idle.sweptAt.Load(), busy.sweptAt.Load())
	}
	if len(m.cache) != 2 {
		t.Error("the sweep removed a connector from the cache")
	}
}

// A connector without a persistent session must be ignored by every lifecycle
// call instead of failing.
func TestSessionLifecycleIgnoresPlainConnectors(t *testing.T) {
	m := newSessionTestManager(map[int64]engine.Connector{1: plainConnector{}})
	if closed := m.SweepSessions(context.Background()); closed != 0 {
		t.Errorf("closed = %d, want 0", closed)
	}
	m.Invalidate(1)
	m.InvalidateAll()
	m.CloseAll(context.Background())
	if len(m.cache) != 0 {
		t.Errorf("cache entries = %d, want 0", len(m.cache))
	}
}

type plainConnector struct{}

func (plainConnector) Capabilities() engine.Capabilities { return engine.Capabilities{} }
func (plainConnector) Search(context.Context, engine.SearchRequest) ([]engine.Item, error) {
	return nil, nil
}
func (plainConnector) Latest(context.Context, engine.LatestRequest) ([]engine.Item, error) {
	return nil, nil
}
func (plainConnector) Download(context.Context, engine.Item) (engine.Payload, error) {
	return engine.Payload{}, nil
}
func (plainConnector) Test(context.Context) (engine.TestResult, error) {
	return engine.TestResult{}, nil
}
