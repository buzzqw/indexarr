package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/dedup"
)

// DedupeStats reports what one cleanup pass merged.
type DedupeStats struct {
	Before       int64
	After        int64
	HashMerged   int64
	TitleMerged  int64
	Removed      int64
	MergedGroups int64
}

// HashlessSource is a release that has no infohash yet and whose provenance
// still carries a sealed download reference. The enrichment job opens the
// reference through the indexer manager and computes the hash from the magnet
// or the .torrent it receives.
type HashlessSource struct {
	ReleaseID   int64
	IndexerID   int64
	IndexerSlug string
	IndexerName string
	Title       string
	Size        int64
	DownloadRef string
}

// FindSameRelease returns the row a new hashless release belongs to, using the
// same conservative rules as the cleanup job. It never returns a row that
// carries a different infohash. A release with an infohash is matched by hash
// only: the caller already has the strongest identity.
func (s *Store) FindSameRelease(ctx context.Context, rel *Release) (Release, bool, error) {
	if rel == nil || strings.TrimSpace(rel.Title) == "" || rel.Size <= 0 {
		return Release{}, false, nil
	}
	if hash := strings.TrimSpace(rel.InfoHash); hash != "" {
		return s.ReleaseByInfoHash(ctx, strings.ToLower(hash))
	}
	title := rel.SearchTitle
	if title == "" {
		title = NormalizeSearch(rel.Title)
	}
	lower := int64(float64(rel.Size) * (1 - dedup.DefaultSizeTolerance))
	upper := int64(float64(rel.Size) * (1 + dedup.DefaultSizeTolerance))
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+releaseColumns+` FROM releases
		WHERE search_title = ? AND size BETWEEN ? AND ?
		ORDER BY CASE WHEN infohash <> '' THEN 0 ELSE 1 END, id`, title, lower, upper)
	if err != nil {
		return Release{}, false, err
	}
	defer rows.Close()
	target := dedup.Meta{
		Title:       rel.Title,
		Size:        rel.Size,
		InfoHash:    rel.InfoHash,
		PublishedAt: rel.PublishedAt,
		FirstSeenAt: rel.FirstSeenAt,
		LastSeenAt:  rel.LastSeenAt,
	}
	for rows.Next() {
		candidate, err := scanRelease(rows)
		if err != nil {
			return Release{}, false, err
		}
		meta := metaFromRelease(candidate)
		if dedup.Match(target, meta, dedup.Options{}) {
			return candidate, true, nil
		}
	}
	return Release{}, false, rows.Err()
}

func metaFromRelease(rel Release) dedup.Meta {
	return dedup.Meta{
		ID:          rel.ID,
		Title:       rel.Title,
		Size:        rel.Size,
		InfoHash:    rel.InfoHash,
		PublishedAt: rel.PublishedAt,
		FirstSeenAt: rel.FirstSeenAt,
		LastSeenAt:  rel.LastSeenAt,
	}
}

// SetReleaseInfoHash records an infohash recovered by the enrichment job. If
// another row already owns that hash the two rows are merged and the surviving
// id is returned. The operation is idempotent.
func (s *Store) SetReleaseInfoHash(ctx context.Context, releaseID int64, hash string) (keeperID int64, merged bool, err error) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if releaseID <= 0 || hash == "" {
		return releaseID, false, nil
	}
	existing, found, err := s.ReleaseByInfoHash(ctx, hash)
	if err != nil {
		return releaseID, false, err
	}
	if found && existing.ID != releaseID {
		if err := s.MergeReleaseRows(ctx, existing.ID, releaseID); err != nil {
			return releaseID, false, err
		}
		return existing.ID, true, nil
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE releases SET infohash = ? WHERE id = ?`, hash, releaseID); err != nil {
		return releaseID, false, fmt.Errorf("set infohash: %w", err)
	}
	return releaseID, false, nil
}

// MergeReleaseRows merges dupID into keeperID. Every provenance row is moved
// to the keeper (never deleted), the best payload and metadata are kept and
// favorites survive. It runs in one transaction so a crash cannot leave a
// source pointing at a deleted release.
func (s *Store) MergeReleaseRows(ctx context.Context, keeperID, dupID int64) error {
	if keeperID <= 0 || dupID <= 0 || keeperID == dupID {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := mergeReleasePair(ctx, tx, keeperID, dupID); err != nil {
		return err
	}
	return tx.Commit()
}

func mergeReleasePair(ctx context.Context, tx *sql.Tx, keeperID, dupID int64) error {
	// Provenance first: the delete cascades release_sources, so moving the
	// rows before deleting the duplicate keeps every source reachable.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO release_sources(release_id, indexer_id, first_seen_at,
			last_seen_at, seeders, leechers, download_ref, grab_count, last_grab_at)
		SELECT ?, indexer_id, first_seen_at, last_seen_at, seeders, leechers,
			download_ref, grab_count, last_grab_at
		FROM release_sources WHERE release_id = ?
		ON CONFLICT(release_id, indexer_id) DO UPDATE SET
			first_seen_at = CASE
				WHEN release_sources.first_seen_at = 0 THEN excluded.first_seen_at
				WHEN excluded.first_seen_at = 0 THEN release_sources.first_seen_at
				WHEN excluded.first_seen_at < release_sources.first_seen_at THEN excluded.first_seen_at
				ELSE release_sources.first_seen_at END,
			last_seen_at = MAX(release_sources.last_seen_at, excluded.last_seen_at),
			seeders = MAX(release_sources.seeders, excluded.seeders),
			leechers = MAX(release_sources.leechers, excluded.leechers),
			download_ref = CASE
				WHEN release_sources.download_ref = '' THEN excluded.download_ref
				WHEN excluded.seeders > release_sources.seeders
					AND excluded.download_ref <> '' THEN excluded.download_ref
				ELSE release_sources.download_ref END,
			grab_count = release_sources.grab_count + excluded.grab_count,
			last_grab_at = MAX(release_sources.last_grab_at, excluded.last_grab_at)`,
		keeperID, dupID); err != nil {
		return fmt.Errorf("move release sources: %w", err)
	}

	// Metadata: keep the keeper's title (it is the visible row), take any
	// field the keeper is missing from the duplicate and never lower a swarm
	// counter or lose a favorite.
	result, err := tx.ExecContext(ctx, `
		UPDATE releases SET
			infohash = CASE WHEN infohash = '' THEN COALESCE((
				SELECT infohash FROM releases WHERE id = ? AND infohash <> ''), '')
				ELSE infohash END,
			imdb_id = CASE WHEN imdb_id = '' THEN COALESCE((
				SELECT imdb_id FROM releases WHERE id = ? AND imdb_id <> ''), '')
				ELSE imdb_id END,
			tmdb_id = CASE WHEN tmdb_id = '' THEN COALESCE((
				SELECT tmdb_id FROM releases WHERE id = ? AND tmdb_id <> ''), '')
				ELSE tmdb_id END,
			tvdb_id = CASE WHEN tvdb_id = '' THEN COALESCE((
				SELECT tvdb_id FROM releases WHERE id = ? AND tvdb_id <> ''), '')
				ELSE tvdb_id END,
			size = CASE WHEN size <= 0 THEN COALESCE((
				SELECT size FROM releases WHERE id = ?), 0) ELSE size END,
			seeders = MAX(seeders, COALESCE((SELECT seeders FROM releases WHERE id = ?), 0)),
			leechers = MAX(leechers, COALESCE((SELECT leechers FROM releases WHERE id = ?), 0)),
			published_at = CASE WHEN published_at = 0 THEN COALESCE((
				SELECT published_at FROM releases WHERE id = ?), 0) ELSE published_at END,
			magnet = CASE WHEN magnet = '' THEN COALESCE((
				SELECT magnet FROM releases WHERE id = ? AND magnet <> ''), '')
				ELSE magnet END,
			torrent = COALESCE(torrent, (
				SELECT torrent FROM releases WHERE id = ? AND torrent IS NOT NULL)),
			torrent_size = MAX(torrent_size, COALESCE((
				SELECT torrent_size FROM releases WHERE id = ?), 0)),
			torrent_fetched_at = MAX(torrent_fetched_at, COALESCE((
				SELECT torrent_fetched_at FROM releases WHERE id = ?), 0)),
			payload = COALESCE(payload, (
				SELECT payload FROM releases WHERE id = ? AND payload IS NOT NULL)),
			payload_type = CASE WHEN payload_type = '' THEN COALESCE((
				SELECT payload_type FROM releases WHERE id = ? AND payload_type <> ''), '')
				ELSE payload_type END,
			protocol = CASE WHEN protocol = '' THEN COALESCE((
				SELECT protocol FROM releases WHERE id = ? AND protocol <> ''), 'torrent')
				ELSE protocol END,
			first_seen_at = CASE
				WHEN releases.first_seen_at = 0 THEN COALESCE((
					SELECT first_seen_at FROM releases WHERE id = ?), 0)
				WHEN dup.first_seen_at > 0 AND dup.first_seen_at < releases.first_seen_at
					THEN dup.first_seen_at
				ELSE releases.first_seen_at END,
			last_seen_at = MAX(last_seen_at, COALESCE((
				SELECT last_seen_at FROM releases WHERE id = ?), 0)),
			times_seen = times_seen + COALESCE((
				SELECT times_seen FROM releases WHERE id = ?), 0),
			favorite = MAX(favorite, COALESCE((
				SELECT favorite FROM releases WHERE id = ?), 0)),
			hidden = MIN(hidden, COALESCE((
				SELECT hidden FROM releases WHERE id = ?), 0))
		FROM (SELECT first_seen_at FROM releases WHERE id = ?) AS dup
		WHERE releases.id = ?`,
		dupID, dupID, dupID, dupID, dupID, dupID, dupID, dupID, dupID, dupID,
		dupID, dupID, dupID, dupID, dupID, dupID, dupID, dupID, dupID, dupID,
		dupID, dupID, keeperID)
	if err != nil {
		return fmt.Errorf("merge release metadata: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM releases WHERE id = ?`, dupID); err != nil {
		return fmt.Errorf("delete duplicate release: %w", err)
	}
	return nil
}

// MergeDuplicates runs the idempotent cleanup on the whole cache. It first
// groups by infohash (the safest identity), then groups the remaining rows by
// normalized title and applies the conservative rules. Different torrents
// (two different non-empty hashes) are never merged.
func (s *Store) MergeDuplicates(ctx context.Context) (DedupeStats, error) {
	var stats DedupeStats
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, title, size, infohash, published_at, first_seen_at, last_seen_at
		FROM releases ORDER BY id`)
	if err != nil {
		return stats, fmt.Errorf("list releases for dedupe: %w", err)
	}
	type candidate struct {
		meta dedup.Meta
	}
	var all []candidate
	for rows.Next() {
		var id, size, published, firstSeen, lastSeen int64
		var title, hash string
		if err := rows.Scan(&id, &title, &size, &hash, &published, &firstSeen, &lastSeen); err != nil {
			rows.Close()
			return stats, err
		}
		all = append(all, candidate{meta: dedup.Meta{
			ID: id, Title: title, Size: size, InfoHash: hash,
			PublishedAt: unixTime(published), FirstSeenAt: unixTime(firstSeen),
			LastSeenAt: unixTime(lastSeen),
		}})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return stats, err
	}
	stats.Before = int64(len(all))

	removed := make(map[int64]struct{}, len(all)/2)
	merge := func(keeper, dup int64) error {
		if keeper == dup {
			return nil
		}
		if _, gone := removed[dup]; gone {
			return nil
		}
		if err := mergeReleasePair(ctx, tx, keeper, dup); err != nil {
			return err
		}
		removed[dup] = struct{}{}
		return nil
	}

	// Pass 1: infohash. The hash is the torrent identity; identical hashes are
	// always the same torrent, private tracker or not.
	byHash := make(map[string]int64)
	for _, row := range all {
		hash := strings.ToLower(strings.TrimSpace(row.meta.InfoHash))
		if hash == "" {
			continue
		}
		if keeper, ok := byHash[hash]; ok {
			if err := merge(keeper, row.meta.ID); err != nil {
				return stats, err
			}
			stats.HashMerged++
			continue
		}
		byHash[hash] = row.meta.ID
	}

	// Pass 2: title + normalized size. A hash-bearing row is the preferred
	// keeper so recovered hashes keep their identity.
	byTitle := make(map[string][]dedup.Meta)
	for _, row := range all {
		if _, gone := removed[row.meta.ID]; gone {
			continue
		}
		normalized := dedup.Extract(row.meta.Title).Normalized
		if normalized == "" {
			continue
		}
		byTitle[normalized] = append(byTitle[normalized], row.meta)
	}
	keys := make([]string, 0, len(byTitle))
	for key := range byTitle {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		group := byTitle[key]
		if len(group) < 2 {
			continue
		}
		sort.SliceStable(group, func(i, j int) bool {
			left, right := group[i], group[j]
			if (left.InfoHash == "") != (right.InfoHash == "") {
				return left.InfoHash != ""
			}
			return left.ID < right.ID
		})
		var keepers []dedup.Meta
		for _, item := range group {
			attached := false
			for i := range keepers {
				if dedup.Match(keepers[i], item, dedup.Options{}) {
					if err := merge(keepers[i].ID, item.ID); err != nil {
						return stats, err
					}
					stats.TitleMerged++
					attached = true
					break
				}
			}
			if !attached {
				keepers = append(keepers, item)
			}
		}
		if len(keepers) < len(group) {
			stats.MergedGroups++
		}
	}

	if err := tx.Commit(); err != nil {
		return stats, err
	}
	stats.Removed = int64(len(removed))
	stats.After = stats.Before - stats.Removed
	return stats, nil
}

// FuseReleases collapses an aggregated result page in memory. Persistent
// cleanup already merges the rows; this pass protects the current request from
// a duplicate written after the last cleanup pass. The first matching row
// keeps its position and absorbs the missing payload of the others.
func FuseReleases(releases []Release) []Release {
	if len(releases) < 2 {
		return releases
	}
	out := make([]Release, 0, len(releases))
	// Match requires the same normalized title. A title bucket keeps the
	// pass near-linear on a 20k-release cache instead of comparing every row
	// with every earlier row; the exact identity rules stay unchanged.
	buckets := make(map[string][]int, len(releases))
	for _, rel := range releases {
		key := dedup.Extract(rel.Title).Normalized
		matched := -1
		for _, i := range buckets[key] {
			if dedup.Match(metaFromRelease(out[i]), metaFromRelease(rel), dedup.Options{}) {
				matched = i
				break
			}
		}
		if matched < 0 {
			out = append(out, rel)
			buckets[key] = append(buckets[key], len(out)-1)
			continue
		}
		keeper := &out[matched]
		if keeper.InfoHash == "" && rel.InfoHash != "" {
			// Keep the position but adopt the strongest identity, retaining
			// the swarm of the hashless copy and the fields it filled in.
			rel.Title = keeper.Title
			rel.Seeders = maxInt64(rel.Seeders, keeper.Seeders)
			rel.Leechers = maxInt64(rel.Leechers, keeper.Leechers)
			rel.TimesSeen += keeper.TimesSeen
			if keeper.FirstSeenAt.IsZero() || (!rel.FirstSeenAt.IsZero() && rel.FirstSeenAt.After(keeper.FirstSeenAt)) {
				rel.FirstSeenAt = keeper.FirstSeenAt
			}
			if rel.LastSeenAt.Before(keeper.LastSeenAt) {
				rel.LastSeenAt = keeper.LastSeenAt
			}
			if rel.Magnet == "" {
				rel.Magnet = keeper.Magnet
			}
			if len(rel.Torrent) == 0 {
				rel.Torrent = keeper.Torrent
				rel.TorrentSize = keeper.TorrentSize
				rel.TorrentFetchedAt = keeper.TorrentFetchedAt
			}
			*keeper = rel
			continue
		}
		if keeper.InfoHash == "" {
			keeper.InfoHash = rel.InfoHash
		}
		if keeper.Magnet == "" {
			keeper.Magnet = rel.Magnet
		}
		if len(keeper.Torrent) == 0 {
			keeper.Torrent = rel.Torrent
			keeper.TorrentSize = rel.TorrentSize
			keeper.TorrentFetchedAt = rel.TorrentFetchedAt
		}
		if len(keeper.Payload) == 0 {
			keeper.Payload = rel.Payload
			keeper.PayloadType = rel.PayloadType
		}
		if keeper.IMDbID == "" {
			keeper.IMDbID = rel.IMDbID
		}
		if keeper.TMDbID == "" {
			keeper.TMDbID = rel.TMDbID
		}
		if keeper.TVDbID == "" {
			keeper.TVDbID = rel.TVDbID
		}
		keeper.Seeders = maxInt64(keeper.Seeders, rel.Seeders)
		keeper.Leechers = maxInt64(keeper.Leechers, rel.Leechers)
		keeper.TimesSeen += rel.TimesSeen
		if keeper.FirstSeenAt.IsZero() || (!rel.FirstSeenAt.IsZero() && rel.FirstSeenAt.Before(keeper.FirstSeenAt)) {
			keeper.FirstSeenAt = rel.FirstSeenAt
		}
		if rel.LastSeenAt.After(keeper.LastSeenAt) {
			keeper.LastSeenAt = rel.LastSeenAt
		}
	}
	return out
}

// HashlessSources lists torrent releases without an infohash that still have a
// sealed download reference. The caller caps the work per indexer so a popular
// site cannot be hammered by the enrichment job.
func (s *Store) HashlessSources(ctx context.Context, scanLimit int) ([]HashlessSource, error) {
	if scanLimit <= 0 {
		scanLimit = 5000
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, rs.indexer_id, i.slug, i.name, r.title, r.size, rs.download_ref
		FROM release_sources rs
		JOIN releases r ON r.id = rs.release_id
		JOIN indexers i ON i.id = rs.indexer_id
		WHERE r.infohash = '' AND r.protocol = 'torrent'
			AND rs.download_ref <> ''
		ORDER BY r.id, rs.indexer_id
		LIMIT ?`, scanLimit)
	if err != nil {
		return nil, fmt.Errorf("list hashless sources: %w", err)
	}
	defer rows.Close()
	var out []HashlessSource
	for rows.Next() {
		var item HashlessSource
		if err := rows.Scan(&item.ReleaseID, &item.IndexerID, &item.IndexerSlug,
			&item.IndexerName, &item.Title, &item.Size, &item.DownloadRef); err != nil {
			return nil, fmt.Errorf("scan hashless source: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// RecordSourceGrab records which copy of a merged release actually delivered
// the payload. It is best effort: a failed counter update never fails a grab.
func (s *Store) RecordSourceGrab(ctx context.Context, releaseID, indexerID int64) error {
	if releaseID <= 0 || indexerID <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE release_sources SET grab_count = grab_count + 1, last_grab_at = ?
		WHERE release_id = ? AND indexer_id = ?`, time.Now().Unix(), releaseID, indexerID)
	if err != nil {
		return fmt.Errorf("record source grab: %w", err)
	}
	return nil
}

// ReleaseSourcesFor returns the provenance rows of one release with the
// health and priority needed to choose the best copy. It is used by the
// release detail page and by the resolver.
func (s *Store) ReleaseSourcesFor(ctx context.Context, releaseID int64) ([]ReleaseSourceInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rs.indexer_id, i.name, i.priority, i.enabled,
			COALESCE(st.last_error, ''), COALESCE(st.circuit, 'closed'),
			rs.seeders, rs.leechers, rs.last_seen_at, rs.download_ref,
			rs.grab_count, rs.last_grab_at
		FROM release_sources rs
		JOIN indexers i ON i.id = rs.indexer_id
		LEFT JOIN indexer_state st ON st.indexer_id = i.id
		WHERE rs.release_id = ?
		ORDER BY rs.seeders DESC, i.priority, i.id`, releaseID)
	if err != nil {
		return nil, fmt.Errorf("release sources: %w", err)
	}
	defer rows.Close()
	var out []ReleaseSourceInfo
	for rows.Next() {
		var item ReleaseSourceInfo
		var lastSeen, lastGrab int64
		var lastError, circuit string
		var enabled int
		if err := rows.Scan(&item.IndexerID, &item.Name, &item.Priority, &enabled,
			&lastError, &circuit, &item.Seeders, &item.Leechers, &lastSeen,
			&item.DownloadRef, &item.GrabCount, &lastGrab); err != nil {
			return nil, fmt.Errorf("scan release source: %w", err)
		}
		item.Enabled = enabled != 0
		item.Healthy = item.Enabled && strings.TrimSpace(lastError) == "" &&
			!strings.EqualFold(strings.TrimSpace(circuit), "open")
		item.HasDownloadRef = strings.TrimSpace(item.DownloadRef) != ""
		item.LastSeenAt = unixTime(lastSeen)
		item.LastGrabAt = unixTime(lastGrab)
		out = append(out, item)
	}
	return out, rows.Err()
}

// ReleaseSourceInfo is one indexer copy of a merged release.
type ReleaseSourceInfo struct {
	IndexerID      int64
	Name           string
	Priority       int
	Enabled        bool
	Healthy        bool
	Seeders        int64
	Leechers       int64
	LastSeenAt     time.Time
	DownloadRef    string
	HasDownloadRef bool
	GrabCount      int64
	LastGrabAt     time.Time
}

// BetterThan reports whether this copy should be preferred over other. A
// healthy copy wins, then the user priority (lower is preferred), then the
// most recent seeders. A copy with a sealed download reference wins a full
// tie because it can still deliver bytes when the magnet is gone.
func (s ReleaseSourceInfo) BetterThan(other ReleaseSourceInfo) bool {
	if s.Healthy != other.Healthy {
		return s.Healthy
	}
	if s.Priority != other.Priority {
		return s.Priority < other.Priority
	}
	if s.Seeders != other.Seeders {
		return s.Seeders > other.Seeders
	}
	return s.HasDownloadRef && !other.HasDownloadRef
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ErrNoRelease is returned when a merge is asked for a missing row.
var ErrNoRelease = errors.New("store: release not found")
