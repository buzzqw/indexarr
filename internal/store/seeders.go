package store

import (
	"context"
	"fmt"
	"time"
)

// SeedersCandidate is a recent provenance row whose swarm has not been
// refreshed within the TTL.
type SeedersCandidate struct {
	ReleaseID   int64
	IndexerID   int64
	Name        string
	Title       string
	PublishedAt time.Time
	LastSeenAt  time.Time
	GrabCount   int64
}

// SeedersCandidates selects copies that are worth refreshing: enabled,
// non-experimental definitions, releases published or seen in the last two
// weeks (or grabbed at least once), and a seeders check older than the TTL.
// Feed-only and LiveExcluded sources are filtered by the caller, which knows
// the connector capabilities.
func (s *Store) SeedersCandidates(ctx context.Context, now time.Time, ttl time.Duration, limit int) ([]SeedersCandidate, error) {
	limit = clampStaleLimit(limit)
	cutoff := now.Add(-ttl).Unix()
	recent := now.Add(-14 * 24 * time.Hour).Unix()
	rows, err := s.db.QueryContext(ctx, `
		SELECT rs.release_id, rs.indexer_id, i.name, r.title, r.published_at, rs.last_seen_at, rs.grab_count
		FROM release_sources rs
		JOIN releases r ON r.id = rs.release_id
		JOIN indexers i ON i.id = rs.indexer_id
		JOIN definitions d ON d.id = i.definition_id
		WHERE i.enabled = 1
		  AND d.state NOT IN ('bozza', 'degradata')
		  AND (rs.seeders_checked_at = 0 OR rs.seeders_checked_at < ?)
		  AND (r.published_at >= ? OR r.last_seen_at >= ? OR rs.grab_count > 0)
		ORDER BY r.published_at DESC, rs.last_seen_at DESC
		LIMIT ?`, cutoff, recent, recent, limit)
	if err != nil {
		return nil, fmt.Errorf("seeders candidates: %w", err)
	}
	defer rows.Close()
	var out []SeedersCandidate
	for rows.Next() {
		var item SeedersCandidate
		var published, seen int64
		if err := rows.Scan(&item.ReleaseID, &item.IndexerID, &item.Name, &item.Title,
			&published, &seen, &item.GrabCount); err != nil {
			return nil, fmt.Errorf("scan seeders candidate: %w", err)
		}
		item.PublishedAt = unixTime(published)
		item.LastSeenAt = unixTime(seen)
		out = append(out, item)
	}
	return out, rows.Err()
}

// MarkSeedersChecked records that the source was queried, even when the
// release no longer exists or the search returned no matching row.
func (s *Store) MarkSeedersChecked(ctx context.Context, releaseID, indexerID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE release_sources SET seeders_checked_at = ? WHERE release_id = ? AND indexer_id = ?`,
		at.Unix(), releaseID, indexerID)
	return err
}

// UpdateSourceSwarm refreshes one source's swarm counters without touching
// last_seen_at, which means "seen in a listing or search".
func (s *Store) UpdateSourceSwarm(ctx context.Context, releaseID, indexerID, seeders, leechers int64, checked time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE release_sources SET seeders = ?, leechers = ?, seeders_checked_at = ?
		 WHERE release_id = ? AND indexer_id = ?`,
		seeders, leechers, checked.Unix(), releaseID, indexerID)
	return err
}
