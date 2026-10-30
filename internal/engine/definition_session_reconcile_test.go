package engine

import (
	"context"
	"testing"
	"time"
)

// The global reconcile must close only the sessions whose name carries a
// prefix this process registered, and never a session that is still live.
func TestSessionRegistryReconcileClosesOnlyOwnedOrphans(t *testing.T) {
	fake := newFakeFlareSolverr(t)
	registry := NewSessionRegistry()
	registry.RegisterPrefix(fake.server.URL, "indexarr")
	registry.RegisterPrefix(fake.server.URL, "mircrew-idx")

	live := "indexarr-live-aaaaaa"
	registry.add(live)
	want := []string{
		live,
		"indexarr-default-111111", // owned through the wildcard
		"mircrew-idx-222222",      // owned through the definition prefix
		"gextto-foreign-333333",   // another program
		"unrelated-session",       // no known prefix
	}
	fake.mu.Lock()
	for _, name := range want {
		fake.sessions[name] = true
	}
	fake.mu.Unlock()

	closed := registry.ReconcileSessions(context.Background(), fake.server.URL, 5*time.Second)
	if closed != 2 {
		t.Fatalf("closed = %d, want the two orphaned indexarr sessions", closed)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, name := range []string{live, "gextto-foreign-333333", "unrelated-session"} {
		if !fake.sessions[name] {
			t.Errorf("session %q was destroyed, want it left alone", name)
		}
	}
	if len(fake.sessions) != 3 {
		t.Errorf("sessions left = %d (%v), want 3", len(fake.sessions), fake.sessions)
	}
}

// A second pass finds nothing to close: the reconcile does not keep calling
// sessions.destroy on the same names.
func TestSessionRegistryReconcileIsIdempotent(t *testing.T) {
	fake := newFakeFlareSolverr(t)
	registry := NewSessionRegistry()
	registry.RegisterPrefix(fake.server.URL, "indexarr")
	fake.mu.Lock()
	fake.sessions["indexarr-old-444444"] = true
	fake.mu.Unlock()

	if closed := registry.ReconcileSessions(context.Background(), fake.server.URL, 5*time.Second); closed != 1 {
		t.Fatalf("first pass closed = %d, want 1", closed)
	}
	if closed := registry.ReconcileSessions(context.Background(), fake.server.URL, 5*time.Second); closed != 0 {
		t.Fatalf("second pass closed = %d, want 0", closed)
	}
}

// Without a registered prefix the reconcile must not touch anything: the same
// FlareSolverr can serve other programs.
func TestSessionRegistryReconcileWithoutPrefixLeavesSessionsAlone(t *testing.T) {
	fake := newFakeFlareSolverr(t)
	registry := NewSessionRegistry()
	fake.mu.Lock()
	fake.sessions["indexarr-old-555555"] = true
	fake.mu.Unlock()

	if closed := registry.ReconcileSessions(context.Background(), fake.server.URL, 5*time.Second); closed != 0 {
		t.Fatalf("closed = %d, want 0 without a registered prefix", closed)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if !fake.sessions["indexarr-old-555555"] {
		t.Error("the session was destroyed without an ownership prefix")
	}
}
