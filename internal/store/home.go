package store

import (
	"context"
	"time"
)

// HomeActivity is the "last 24 hours" panel of the home page. It intentionally
// contains plain user-facing numbers, not cache or database internals.
type HomeActivity struct {
	NewReleases  int64
	AppSearches  int64
	Grabs        int64
	LastActivity time.Time
}

// LastReleaseSeen is the newest last_seen_at in the cache.
func (s *Store) LastReleaseSeen(ctx context.Context) (time.Time, error) {
	var last int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(last_seen_at), 0) FROM releases`).Scan(&last); err != nil {
		return time.Time{}, err
	}
	return unixTime(last), nil
}

// LoadHomeActivity counts the things that happened in the last 24 hours.
func (s *Store) LoadHomeActivity(ctx context.Context) (HomeActivity, error) {
	var out HomeActivity
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM releases WHERE first_seen_at >= ?`, cutoff).
		Scan(&out.NewReleases); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM app_searches WHERE created_at >= ?`, cutoff).
		Scan(&out.AppSearches); err != nil {
		return out, err
	}
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM indexer_history WHERE kind = 'grab' AND started_at >= ?`, cutoff).
		Scan(&out.Grabs); err != nil {
		return out, err
	}
	var last int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(last_seen_at), 0) FROM releases`).Scan(&last); err != nil {
		return out, err
	}
	out.LastActivity = unixTime(last)
	return out, nil
}
