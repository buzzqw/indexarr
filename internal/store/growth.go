package store

import (
	"context"
	"fmt"
	"time"
)

// DatabaseGrowth is the size snapshot used by Settings. It is intentionally
// cheap: page counts plus SQLite's dbstat for the FTS5 tables.
type DatabaseGrowth struct {
	TotalBytes int64
	FTSBytes   int64
	LastDay    int64
	LastWeek   int64
	Releases   int64
}

// Growth returns the current database size and release growth counters.
func (s *Store) Growth(ctx context.Context, now time.Time) (DatabaseGrowth, error) {
	var out DatabaseGrowth
	var pages, pageSize int64
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return out, fmt.Errorf("page_count: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_size`).Scan(&pageSize); err != nil {
		return out, fmt.Errorf("page_size: %w", err)
	}
	out.TotalBytes = pages * pageSize
	// dbstat exists in SQLite; if a build omits it, the panel still shows the
	// total size and zero FTS bytes rather than failing the settings page.
	_ = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(pgsize),0) FROM dbstat WHERE name LIKE 'releases_fts%'`).Scan(&out.FTSBytes)
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM releases`).Scan(&out.Releases); err != nil {
		return out, fmt.Errorf("count releases: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM releases WHERE first_seen_at >= ?`,
		now.Add(-24*time.Hour).Unix()).Scan(&out.LastDay); err != nil {
		return out, fmt.Errorf("daily growth: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM releases WHERE first_seen_at >= ?`,
		now.Add(-7*24*time.Hour).Unix()).Scan(&out.LastWeek); err != nil {
		return out, fmt.Errorf("weekly growth: %w", err)
	}
	return out, nil
}

// UpdateSourceSwarm refreshes one source's swarm counters and marks the
// provenance row as freshly observed.
func (s *Store) UpdateSourceSwarm(ctx context.Context, releaseID, indexerID, seeders, leechers int64, seen time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE release_sources SET seeders = ?, leechers = ?, last_seen_at = ?
		 WHERE release_id = ? AND indexer_id = ?`,
		seeders, leechers, seen.Unix(), releaseID, indexerID)
	return err
}
