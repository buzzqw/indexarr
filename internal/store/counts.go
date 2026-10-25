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

	statsOK bool
	statsAt time.Time
	stats   Stats

	homeOK bool
	homeAt time.Time
	home   HomeActivity
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
	s.counts.statsOK = false
	s.counts.homeOK = false
	s.counts.mu.Unlock()
}

// CachedStats is LoadStats with a short TTL, used by every read path
// (/healthz, Home, metrics, maintenance, probe).
func (s *Store) CachedStats(ctx context.Context) (Stats, error) {
	now := time.Now()
	s.counts.mu.Lock()
	if s.counts.statsOK && now.Sub(s.counts.statsAt) < countsTTL {
		out := s.counts.stats
		s.counts.mu.Unlock()
		return out, nil
	}
	rev := s.counts.rev
	s.counts.mu.Unlock()

	stats, err := s.LoadStats(ctx)
	if err != nil {
		return stats, err
	}

	s.counts.mu.Lock()
	if s.counts.rev == rev {
		s.counts.stats = stats
		s.counts.statsOK = true
		s.counts.statsAt = now
	}
	s.counts.mu.Unlock()
	return stats, nil
}

// CachedHomeActivity is LoadHomeActivity with the same short TTL.
func (s *Store) CachedHomeActivity(ctx context.Context) (HomeActivity, error) {
	now := time.Now()
	s.counts.mu.Lock()
	if s.counts.homeOK && now.Sub(s.counts.homeAt) < countsTTL {
		out := s.counts.home
		s.counts.mu.Unlock()
		return out, nil
	}
	rev := s.counts.rev
	s.counts.mu.Unlock()

	activity, err := s.LoadHomeActivity(ctx)
	if err != nil {
		return activity, err
	}

	s.counts.mu.Lock()
	if s.counts.rev == rev {
		s.counts.home = activity
		s.counts.homeOK = true
		s.counts.homeAt = now
	}
	s.counts.mu.Unlock()
	return activity, nil
}
