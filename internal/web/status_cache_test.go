package web

import (
	"context"
	"testing"
)

// TestHomeStatusCacheFollowsStoreRevision proves the sidebar cache cannot hide
// a state change: the store increments its counter revision on every write, so
// the second call recomputes instead of serving the previous snapshot.
func TestHomeStatusCacheFollowsStoreRevision(t *testing.T) {
	env := newTestEnv(t)
	before := env.server.HomeStatus(context.Background())
	if before.ProblemIndexers != 0 {
		t.Fatalf("initial HomeStatus = %#v", before)
	}
	seedBrokenIndexer(t, env, "cache-broken", "HTTP 429: too many requests")
	after := env.server.HomeStatus(context.Background())
	if after.ProblemIndexers != 1 || after.Level != "warn" {
		t.Fatalf("HomeStatus after the write = %#v, want one problem", after)
	}
}
