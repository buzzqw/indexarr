package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ReleaseProvenance is the aggregated view of every indexer that supplied one
// release. Names are ordered by indexer name (case-insensitive) so the Torznab
// output is stable across requests.
type ReleaseProvenance struct {
	ReleaseID  int64
	Indexers   int
	Names      []string
	Seeders    int64
	Leechers   int64
	LastSeenAt time.Time
	// HasDownloadRef reports that at least one provenance row carries a sealed
	// download reference. The Torznab item then links to indexarr's /dl route
	// even when no magnet or .torrent is cached: the resolver fetches the
	// payload at grab time.
	HasDownloadRef bool
}

// StaleRelease is one provenance row whose swarm counters are older than the
// requested cutoff. Name is the indexer name, Title the cached release title.
type StaleRelease struct {
	ReleaseID  int64
	IndexerID  int64
	Name       string
	Title      string
	LastSeenAt time.Time
}

const (
	// staleProvenanceDefaultLimit is used when the caller passes a non-positive
	// limit. The refresh job belongs to the scheduler: this package only selects
	// the rows, it never calls an indexer or starts a background task.
	staleProvenanceDefaultLimit = 200
	staleProvenanceMaxLimit     = 1000

	// provenanceChunkSize keeps the IN list well below SQLite's variable limit.
	provenanceChunkSize = 500
)

// ReleaseProvenance returns the provenance of the requested releases grouped by
// release. Non-positive and duplicate ids are ignored; an empty input returns an
// empty map without querying the database. Releases without provenance rows are
// simply absent from the map.
func (s *Store) ReleaseProvenance(ctx context.Context, releaseIDs []int64) (map[int64]ReleaseProvenance, error) {
	out := make(map[int64]ReleaseProvenance)
	ids := uniquePositiveIDs(releaseIDs)
	for start := 0; start < len(ids); start += provenanceChunkSize {
		end := start + provenanceChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		if err := s.loadProvenanceChunk(ctx, ids[start:end], out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// loadProvenanceChunk reads one IN-list page and merges it into out. The rows are
// ordered explicitly instead of relying on GROUP_CONCAT ordering, which SQLite
// does not guarantee.
func (s *Store) loadProvenanceChunk(ctx context.Context, ids []int64, out map[int64]ReleaseProvenance) error {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT rs.release_id, i.name, rs.seeders, rs.leechers, rs.last_seen_at,
			rs.download_ref
		FROM release_sources rs
		JOIN indexers i ON i.id = rs.indexer_id
		WHERE rs.release_id IN (`+strings.Join(placeholders, ", ")+`)
		ORDER BY rs.release_id, i.name COLLATE NOCASE, i.id`, args...)
	if err != nil {
		return fmt.Errorf("release provenance: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var releaseID int64
		var name string
		var seeders, leechers, lastSeen int64
		var downloadRef string
		if err := rows.Scan(&releaseID, &name, &seeders, &leechers, &lastSeen,
			&downloadRef); err != nil {
			return fmt.Errorf("scan release provenance: %w", err)
		}
		prov := out[releaseID]
		prov.ReleaseID = releaseID
		prov.Indexers++
		prov.Names = append(prov.Names, name)
		if strings.TrimSpace(downloadRef) != "" {
			prov.HasDownloadRef = true
		}
		if seeders > prov.Seeders {
			prov.Seeders = seeders
		}
		if leechers > prov.Leechers {
			prov.Leechers = leechers
		}
		if seen := unixTime(lastSeen); seen.After(prov.LastSeenAt) {
			prov.LastSeenAt = seen
		}
		out[releaseID] = prov
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate release provenance: %w", err)
	}
	return nil
}

// uniquePositiveIDs drops duplicates and non-positive ids while keeping the
// caller's order.
func uniquePositiveIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// StaleProvenance lists provenance rows last seen before olderThan, oldest
// first. It is the data source for a future scheduler refresh job: this method
// never contacts an indexer. The limit defaults to 200 and is capped at 1000.
func (s *Store) StaleProvenance(ctx context.Context, olderThan time.Time, limit int) ([]StaleRelease, error) {
	limit = clampStaleLimit(limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT rs.release_id, rs.indexer_id, i.name, r.title, rs.last_seen_at
		FROM release_sources rs
		JOIN releases r ON r.id = rs.release_id
		JOIN indexers i ON i.id = rs.indexer_id
		WHERE rs.last_seen_at < ?
		ORDER BY rs.last_seen_at, rs.release_id, rs.indexer_id
		LIMIT ?`, olderThan.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("stale provenance: %w", err)
	}
	defer rows.Close()
	var out []StaleRelease
	for rows.Next() {
		var item StaleRelease
		var lastSeen int64
		if err := rows.Scan(&item.ReleaseID, &item.IndexerID, &item.Name,
			&item.Title, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan stale provenance: %w", err)
		}
		item.LastSeenAt = unixTime(lastSeen)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale provenance: %w", err)
	}
	return out, nil
}

func clampStaleLimit(limit int) int {
	switch {
	case limit <= 0:
		return staleProvenanceDefaultLimit
	case limit > staleProvenanceMaxLimit:
		return staleProvenanceMaxLimit
	default:
		return limit
	}
}
