package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// StoredStats is the "Memorizzate: N release, X MB, +Y al giorno" summary of
// the sources the user chose to keep.
type StoredStats struct {
	Releases int64
	Bytes    int64
	PerDay   int64
	Sources  int
	DBSize   int64
}

// StoredSourceIDs returns the ids of the indexers whose configuration asks to
// memorise new releases. The flag lives in config_json so an existing database
// needs no migration.
func (s *Store) StoredSourceIDs(ctx context.Context) ([]int64, error) {
	indexers, err := s.ListIndexers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, 4)
	for _, indexer := range indexers {
		if indexerStoredConfig(indexer.ConfigJSON) {
			out = append(out, indexer.ID)
		}
	}
	return out, nil
}

// StoredSourceCount counts the indexers with the memorise flag on.
func (s *Store) StoredSourceCount(ctx context.Context) (int, error) {
	ids, err := s.StoredSourceIDs(ctx)
	if err != nil {
		return 0, err
	}
	return len(ids), nil
}

func indexerStoredConfig(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return false
	}
	value := strings.TrimSpace(values["store_latest"])
	return strings.EqualFold(value, "true") || value == "1"
}

// StoredStats summarises the releases that belong to at least one stored
// source. PerDay is the seven-day average, a stable figure that does not jump
// with a single large first read.
func (s *Store) StoredStats(ctx context.Context, now time.Time) (StoredStats, error) {
	out := StoredStats{}
	ids, err := s.StoredSourceIDs(ctx)
	if err != nil {
		return out, err
	}
	out.Sources = len(ids)
	if size, err := s.SizeBytes(); err == nil {
		out.DBSize = size
	}
	if len(ids) == 0 {
		return out, nil
	}
	placeholders, args := idPlaceholders(ids)
	row := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(size), 0) FROM releases r
		WHERE EXISTS (
			SELECT 1 FROM release_sources rs
			WHERE rs.release_id = r.id AND rs.indexer_id IN (`+placeholders+`))`, args...)
	if err := row.Scan(&out.Releases, &out.Bytes); err != nil {
		return out, err
	}
	row = s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM releases r
		WHERE r.first_seen_at >= ? AND EXISTS (
			SELECT 1 FROM release_sources rs
			WHERE rs.release_id = r.id AND rs.indexer_id IN (`+placeholders+`))`,
		append([]any{now.Add(-7 * 24 * time.Hour).Unix()}, args...)...)
	var recent int64
	if err := row.Scan(&recent); err != nil {
		return out, err
	}
	out.PerDay = (recent + 3) / 7
	return out, nil
}

func idPlaceholders(ids []int64) (string, []any) {
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	return strings.Join(placeholders, ","), args
}

// FindStoredDuplicate applies the cautious §1g rule to a stored source: the
// normalized title must be identical, the size within the rounding tolerance,
// the protocol equal and the dates compatible. A candidate with a different
// known infohash is never merged. Callers keep the existing dedup key, so the
// duplicate becomes another release_sources row instead of a lost result.
func (s *Store) FindStoredDuplicate(ctx context.Context, rel Release) (Release, bool, error) {
	if strings.TrimSpace(rel.SearchTitle) == "" || rel.Size <= 0 {
		return Release{}, false, nil
	}
	tolerance := rel.Size / 67 // about 1.5%
	if tolerance < 1 {
		tolerance = 1
	}
	published := rel.PublishedAt.Unix()
	if published < 0 {
		published = 0
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+releaseColumns+` FROM releases
		WHERE search_title = ? AND protocol = ? AND size BETWEEN ? AND ?
		  AND (? = 0 OR published_at = 0 OR ABS(published_at - ?) <= 604800)
		ORDER BY id LIMIT 8`,
		rel.SearchTitle, rel.Protocol, rel.Size-tolerance, rel.Size+tolerance,
		published, published)
	if err != nil {
		return Release{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		candidate, err := scanRelease(rows)
		if err != nil {
			return Release{}, false, err
		}
		if rel.InfoHash != "" && candidate.InfoHash != "" &&
			!strings.EqualFold(rel.InfoHash, candidate.InfoHash) {
			continue
		}
		if !sameCategoryGroup(rel.Category, candidate.Category) {
			continue
		}
		return candidate, true, nil
	}
	return Release{}, false, rows.Err()
}

func sameCategoryGroup(left, right string) bool {
	if left == "" || right == "" {
		return true
	}
	if left == right {
		return true
	}
	leftGroup, rightGroup := left[:1], right[:1]
	return leftGroup == rightGroup
}

// MarkReleaseDownloaded records that a client grabbed this release. Retention
// never removes a downloaded release.
func (s *Store) MarkReleaseDownloaded(ctx context.Context, releaseID int64) error {
	if releaseID <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE releases SET downloaded_at = ? WHERE id = ? AND downloaded_at = 0`,
		time.Now().Unix(), releaseID)
	if err != nil {
		return fmt.Errorf("mark release downloaded: %w", err)
	}
	return nil
}

// PruneStoredReleases removes only releases that came exclusively from stored
// sources and are older than the retention window. Favorites and downloaded
// releases are always kept, and a release shared with any non-stored source is
// kept as well. The deletion cascades to release_sources.
func (s *Store) PruneStoredReleases(ctx context.Context, before time.Time) (int64, error) {
	ids, err := s.StoredSourceIDs(ctx)
	if err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders, args := idPlaceholders(ids)
	query := `
		DELETE FROM releases WHERE id IN (
			SELECT r.id FROM releases r
			WHERE r.favorite = 0 AND r.downloaded_at = 0
			  AND COALESCE(NULLIF(r.published_at, 0), r.first_seen_at) < ?
			  AND EXISTS (SELECT 1 FROM release_sources rs WHERE rs.release_id = r.id)
			  AND NOT EXISTS (
				SELECT 1 FROM release_sources rs
				WHERE rs.release_id = r.id AND rs.indexer_id NOT IN (` + placeholders + `))
		)`
	allArgs := append([]any{before.Unix()}, args...)
	res, err := s.db.ExecContext(ctx, query, allArgs...)
	if err != nil {
		return 0, fmt.Errorf("prune stored releases: %w", err)
	}
	return res.RowsAffected()
}

// LinkedAppCategoryRoots returns the top-level standard categories of the
// applications that consume this indexer: Sonarr asks TV (5000), Radarr movies
// (2000), Lidarr audio, Readarr books and Whisparr adult. A stored source uses
// these roots so a periodic read follows what the apps really search.
func (s *Store) LinkedAppCategoryRoots(ctx context.Context, indexerID int64) ([]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT a.kind FROM applications a
		JOIN app_indexers ai ON ai.application_id = a.id
		WHERE ai.indexer_id = ?`, indexerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[int]bool{}
	var out []int
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return nil, err
		}
		root := 0
		switch strings.ToLower(strings.TrimSpace(kind)) {
		case "sonarr":
			root = 5000
		case "radarr":
			root = 2000
		case "lidarr":
			root = 3000
		case "readarr":
			root = 7000
		case "whisparr":
			root = 6000
		}
		if root > 0 && !seen[root] {
			seen[root] = true
			out = append(out, root)
		}
	}
	return out, rows.Err()
}
