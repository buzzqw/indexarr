package store

import (
	"context"
	"sync"
	"time"
)

// countsTTL is how long the counter snapshots stay valid. The dashboard and
// /healthz are read on every page and by monitoring, while the underlying
// COUNT(*) scans grow with the release cache. A few seconds of staleness is
// invisible to the operator and keeps page rendering independent of the
// archive size.
const countsTTL = 15 * time.Second

// countsCache holds one stats snapshot and one home-activity snapshot. Both
// are invalidated by the same revision, because every write that changes one
// of them normally changes the other too (a live search stores releases and
// records the app search).
type countsCache struct {
	mu  sync.Mutex
	rev uint64

	stats cachedSnapshot[Stats]
	home  cachedSnapshot[HomeActivity]
}

// cachedSnapshot is one value of countsCache with the time it was read.
type cachedSnapshot[T any] struct {
	ok    bool
	at    time.Time
	value T
}

// CountsRevision returns the current counter revision. The web layer uses it
// to invalidate its own derived status cache together with the store counters.
func (s *Store) CountsRevision() uint64 {
	s.counts.mu.Lock()
	defer s.counts.mu.Unlock()
	return s.counts.rev
}

// InvalidateCounts drops the cached counters. It is cheap and safe to call
// after any write that can change a number shown in the UI.
func (s *Store) InvalidateCounts() {
	s.counts.mu.Lock()
	s.counts.rev++
	s.counts.stats.ok = false
	s.counts.home.ok = false
	s.counts.mu.Unlock()
}

// CachedStats is LoadStats with a short TTL, used by every read path
// (/healthz, Home, metrics, maintenance, probe).
func (s *Store) CachedStats(ctx context.Context) (Stats, error) {
	return cachedCount(s, &s.counts.stats, func() (Stats, error) { return s.LoadStats(ctx) })
}

// CachedHomeActivity is LoadHomeActivity with the same short TTL.
func (s *Store) CachedHomeActivity(ctx context.Context) (HomeActivity, error) {
	return cachedCount(s, &s.counts.home, func() (HomeActivity, error) { return s.LoadHomeActivity(ctx) })
}

// cachedCount returns the snapshot while it is younger than countsTTL, and
// otherwise loads it again. The query runs without the lock; its result is
// kept only if no write invalidated the counters in the meantime, so a slow
// read never stores a number older than the write that followed it.
func cachedCount[T any](s *Store, snapshot *cachedSnapshot[T], load func() (T, error)) (T, error) {
	now := time.Now()
	s.counts.mu.Lock()
	if snapshot.ok && now.Sub(snapshot.at) < countsTTL {
		out := snapshot.value
		s.counts.mu.Unlock()
		return out, nil
	}
	rev := s.counts.rev
	s.counts.mu.Unlock()

	value, err := load()
	if err != nil {
		return value, err
	}

	s.counts.mu.Lock()
	if s.counts.rev == rev {
		*snapshot = cachedSnapshot[T]{ok: true, at: now, value: value}
	}
	s.counts.mu.Unlock()
	return value, nil
}
