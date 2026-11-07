package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DBReport describes the physical database file and the main tables. It is
// read-only and powers the maintenance page.
type DBReport struct {
	PageSize        int64
	PageCount       int64
	FreelistCount   int64
	JournalMode     string
	AutoVacuum      int64
	WALBytes        int64
	SHMBytes        int64
	Releases        int64
	ReleasesTorrent int64
	ReleasesMagnet  int64
	ReleasesNoLink  int64
	Hidden          int64
	Favorites       int64
	Indexers        int64
	History         int64
	Blocklist       int64
	SavedSearches   int64
	APIKeys         int64
	Settings        int64
}

// FreeBytes is the space SQLite can reclaim with VACUUM.
func (r DBReport) FreeBytes() int64 { return r.FreelistCount * r.PageSize }

// LoadDBReport collects page, WAL and row information for the maintenance UI.
func (s *Store) LoadDBReport(ctx context.Context) (DBReport, error) {
	var r DBReport
	var err error
	if r.PageSize, err = s.pragmaInt(ctx, "page_size"); err != nil {
		return r, err
	}
	if r.PageCount, err = s.pragmaInt(ctx, "page_count"); err != nil {
		return r, err
	}
	if r.FreelistCount, err = s.pragmaInt(ctx, "freelist_count"); err != nil {
		return r, err
	}
	if r.AutoVacuum, err = s.pragmaInt(ctx, "auto_vacuum"); err != nil {
		return r, err
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&r.JournalMode); err != nil {
		return r, fmt.Errorf("pragma journal_mode: %w", err)
	}
	r.WALBytes = fileSize(s.path + "-wal")
	r.SHMBytes = fileSize(s.path + "-shm")

	if err := s.db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM releases),
			(SELECT COUNT(*) FROM indexers),
			(SELECT COUNT(*) FROM indexer_history),
			(SELECT COUNT(*) FROM blocklist),
			(SELECT COUNT(*) FROM saved_searches),
			(SELECT COUNT(*) FROM api_keys),
			(SELECT COUNT(*) FROM settings)`,
	).Scan(&r.Releases, &r.Indexers, &r.History, &r.Blocklist, &r.SavedSearches, &r.APIKeys, &r.Settings); err != nil {
		return r, fmt.Errorf("count rows: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN torrent IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN magnet <> '' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN `+noLinkCondition+` THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN hidden = 1 THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN favorite = 1 THEN 1 ELSE 0 END), 0)
		FROM releases`,
	).Scan(&r.ReleasesTorrent, &r.ReleasesMagnet, &r.ReleasesNoLink, &r.Hidden, &r.Favorites); err != nil {
		return r, fmt.Errorf("count releases: %w", err)
	}
	return r, nil
}

func (s *Store) pragmaInt(ctx context.Context, name string) (int64, error) {
	var value int64
	if err := s.db.QueryRowContext(ctx, "PRAGMA "+name).Scan(&value); err != nil {
		return 0, fmt.Errorf("pragma %s: %w", name, err)
	}
	return value, nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0
		}
		return 0
	}
	return info.Size()
}

// Optimize asks SQLite to refresh planner statistics and runs a WAL
// checkpoint. It is cheap and safe on a live database.
func (s *Store) Optimize(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "PRAGMA optimize"); err != nil {
		return fmt.Errorf("optimize: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("wal checkpoint: %w", err)
	}
	return nil
}

// Reindex rebuilds every index. It can take a while on a large cache.
func (s *Store) Reindex(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "REINDEX"); err != nil {
		return fmt.Errorf("reindex: %w", err)
	}
	return nil
}

// ResetIndexerErrors clears the last error and the open circuit of every
// indexer. It does not re-enable an indexer that was disabled.
func (s *Store) ResetIndexerErrors(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE indexer_state SET last_error = '', circuit = 'closed', disabled_until = 0, updated_at = ?
		 WHERE last_error <> '' OR circuit <> 'closed' OR disabled_until <> 0`, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	affected, _ := res.RowsAffected()
	return affected, nil
}

// QuickCheck runs SQLite's fast integrity check. It is lighter than
// IntegrityCheck and suitable as a routine diagnostic.
func (s *Store) QuickCheck(ctx context.Context) (string, error) {
	var result string
	if err := s.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return "", fmt.Errorf("quick check: %w", err)
	}
	return result, nil
}

// ForeignKeyCheck returns how many rows violate a foreign key. The normal
// value is zero.
func (s *Store) ForeignKeyCheck(ctx context.Context) (int64, error) {
	rows, err := s.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return 0, fmt.Errorf("foreign key check: %w", err)
	}
	defer rows.Close()
	var violations int64
	for rows.Next() {
		violations++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("foreign key check: %w", err)
	}
	return violations, nil
}

// Analyze rebuilds the SQLite planner statistics for every table.
func (s *Store) Analyze(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, "ANALYZE"); err != nil {
		return fmt.Errorf("analyze: %w", err)
	}
	return nil
}

// Checkpoint writes the WAL back into the database and truncates it. It
// returns SQLite's busy flag, the WAL frame count and the frames written.
func (s *Store) Checkpoint(ctx context.Context) (busy, logFrames, checkpointed int64, err error) {
	err = s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").
		Scan(&busy, &logFrames, &checkpointed)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("wal checkpoint: %w", err)
	}
	return busy, logFrames, checkpointed, nil
}

// ErrForumHistory means a bulk cleanup tried to remove what indexarr read from
// a forum. Forum data is an archive and is never deleted automatically.
var ErrForumHistory = errors.New("store: forum history is kept")

// DeleteReleasesByIndexer removes releases that were seen only through one
// indexer. A release also supplied by another indexer stays in the cache.
func (s *Store) DeleteReleasesByIndexer(ctx context.Context, indexerID int64) (int64, error) {
	if indexerID <= 0 {
		return 0, errors.New("store: invalid indexer id")
	}
	forumIDs, err := s.forumIndexerIDs(ctx)
	if err != nil {
		return 0, err
	}
	for _, id := range forumIDs {
		if id == indexerID {
			return 0, ErrForumHistory
		}
	}
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM releases
		WHERE id IN (SELECT release_id FROM release_sources WHERE indexer_id = ?)
		  AND NOT EXISTS (
			SELECT 1 FROM release_sources rs
			WHERE rs.release_id = releases.id AND rs.indexer_id <> ?)`,
		indexerID, indexerID)
	if err != nil {
		return 0, fmt.Errorf("delete releases by indexer: %w", err)
	}
	removed, _ := res.RowsAffected()
	s.InvalidateCounts()
	return removed, nil
}

// noLinkCondition matches a release no client can download: no magnet, no
// stored .torrent, no ed2k link, no stored payload (NZB) and no source with a
// sealed download reference. The reference is how private trackers and sites
// without magnets are served: /dl resolves it when a client grabs, so a row
// with one is usable even though nothing is stored yet. It reads the columns
// of the outer "releases" row.
const noLinkCondition = `releases.magnet = '' AND COALESCE(length(releases.torrent), 0) = 0
	AND releases.ed2k_link = '' AND COALESCE(length(releases.payload), 0) = 0
	AND NOT EXISTS (
		SELECT 1 FROM release_sources dl
		WHERE dl.release_id = releases.id AND dl.download_ref <> '')`

// DeleteReleasesWithoutLinks removes rows no client can download (see
// noLinkCondition). Favorites, downloaded releases and forum history are
// always kept.
func (s *Store) DeleteReleasesWithoutLinks(ctx context.Context) (int64, error) {
	forumIDs, err := s.forumIndexerIDs(ctx)
	if err != nil {
		return 0, err
	}
	query := `DELETE FROM releases WHERE ` + noLinkCondition + `
		AND releases.favorite = 0 AND releases.downloaded_at = 0`
	args := []any{}
	if len(forumIDs) > 0 {
		placeholders, forumArgs := idPlaceholders(forumIDs)
		query += ` AND NOT EXISTS (
			SELECT 1 FROM release_sources f
			WHERE f.release_id = releases.id AND f.indexer_id IN (` + placeholders + `))`
		args = append(args, forumArgs...)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("delete releases without links: %w", err)
	}
	removed, _ := res.RowsAffected()
	if removed > 0 {
		s.InvalidateCounts()
	}
	return removed, nil
}

// FTSCheck verifies the full-text index of release titles against the
// releases table. A nil error means the index matches.
func (s *Store) FTSCheck(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO releases_fts(releases_fts, rank) VALUES('integrity-check', 1)`); err != nil {
		return fmt.Errorf("title index check: %w", err)
	}
	return nil
}

// FTSRebuild rebuilds the full-text index of release titles from the
// releases table.
func (s *Store) FTSRebuild(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO releases_fts(releases_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("title index rebuild: %w", err)
	}
	return nil
}

// FTSOptimize merges the segments of both full-text indexes (titles and file
// names). Searches read fewer b-trees afterwards; the content is unchanged.
func (s *Store) FTSOptimize(ctx context.Context) error {
	for _, table := range []string{"releases_fts", "release_files_fts"} {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO `+table+`(`+table+`) VALUES('optimize')`); err != nil {
			return fmt.Errorf("optimize %s: %w", table, err)
		}
	}
	return nil
}

// DeleteOrphans removes file lists and file-name index rows whose release is
// gone. Deletes normally take them along; this is the safety net for rows
// left by an interrupted cleanup or an older version.
func (s *Store) DeleteOrphans(ctx context.Context) (int64, error) {
	var total int64
	for _, statement := range []string{
		`DELETE FROM release_files WHERE NOT EXISTS (
			SELECT 1 FROM releases r WHERE r.id = release_files.release_id)`,
		`DELETE FROM release_files_fts WHERE NOT EXISTS (
			SELECT 1 FROM releases r WHERE r.id = release_files_fts.release_id)`,
	} {
		res, err := s.db.ExecContext(ctx, statement)
		if err != nil {
			return total, fmt.Errorf("delete orphans: %w", err)
		}
		removed, _ := res.RowsAffected()
		total += removed
	}
	return total, nil
}

// MaintenanceState is what the routine maintenance remembers between
// restarts: when the daily and weekly passes last completed and the report
// of the last run, as JSON written by the upkeep package.
type MaintenanceState struct {
	DailyAt  time.Time
	WeeklyAt time.Time
	Report   string
}

const (
	keyMaintenanceDailyAt  = "maintenance_daily_at"
	keyMaintenanceWeeklyAt = "maintenance_weekly_at"
	keyMaintenanceReport   = "maintenance_report"
)

// LoadMaintenanceState reads the routine maintenance bookkeeping. Missing
// keys give zero times, so a new install is due at the first idle window.
func (s *Store) LoadMaintenanceState(ctx context.Context) (MaintenanceState, error) {
	var out MaintenanceState
	rows, err := s.db.QueryContext(ctx,
		`SELECT key, value FROM settings WHERE key IN (?, ?, ?)`,
		keyMaintenanceDailyAt, keyMaintenanceWeeklyAt, keyMaintenanceReport)
	if err != nil {
		return out, fmt.Errorf("load maintenance state: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return out, err
		}
		switch key {
		case keyMaintenanceDailyAt:
			out.DailyAt = parseUnix(value)
		case keyMaintenanceWeeklyAt:
			out.WeeklyAt = parseUnix(value)
		case keyMaintenanceReport:
			out.Report = value
		}
	}
	return out, rows.Err()
}

// SaveMaintenanceState writes the routine maintenance bookkeeping in one
// transaction. A zero time is stored as 0.
func (s *Store) SaveMaintenanceState(ctx context.Context, state MaintenanceState) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range map[string]string{
		keyMaintenanceDailyAt:  formatUnix(state.DailyAt),
		keyMaintenanceWeeklyAt: formatUnix(state.WeeklyAt),
		keyMaintenanceReport:   state.Report,
	} {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings(key, value) VALUES(?, ?)
			 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
			return fmt.Errorf("save maintenance state: %w", err)
		}
	}
	return tx.Commit()
}

func parseUnix(value string) time.Time {
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

func formatUnix(t time.Time) string {
	if t.IsZero() {
		return "0"
	}
	return strconv.FormatInt(t.Unix(), 10)
}

// DeleteHiddenReleases removes every release hidden from the UI.
func (s *Store) DeleteHiddenReleases(ctx context.Context) (int64, error) {
	forumIDs, err := s.forumIndexerIDs(ctx)
	if err != nil {
		return 0, err
	}
	query := `DELETE FROM releases WHERE hidden = 1`
	args := []any{}
	if len(forumIDs) > 0 {
		placeholders, forumArgs := idPlaceholders(forumIDs)
		query += ` AND NOT EXISTS (
			SELECT 1 FROM release_sources f
			WHERE f.release_id = releases.id AND f.indexer_id IN (` + placeholders + `))`
		args = append(args, forumArgs...)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("delete hidden releases: %w", err)
	}
	removed, _ := res.RowsAffected()
	s.InvalidateCounts()
	return removed, nil
}

// StatsReset counts what ResetUsageStats removed.
type StatsReset struct {
	Searches int64 // app requests (Requests tab, per-app statistics)
	History  int64 // source searches, downloads and .torrent fetches
}

// ResetUsageStats clears the usage statistics: the app requests and the
// search, download and grab rows of the source history, with their metrics.
// Scheduled runs, health history, tests, sources, releases and settings stay.
func (s *Store) ResetUsageStats(ctx context.Context) (StatsReset, error) {
	var out StatsReset
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("reset statistics: %w", err)
	}
	defer tx.Rollback()
	const kinds = `kind IN ('search', 'grab', 'download')`
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM indexer_history_metrics WHERE history_id IN (SELECT id FROM indexer_history WHERE `+kinds+`)`); err != nil {
		return out, fmt.Errorf("reset statistics: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM indexer_history WHERE `+kinds)
	if err != nil {
		return out, fmt.Errorf("reset statistics: %w", err)
	}
	out.History, _ = res.RowsAffected()
	res, err = tx.ExecContext(ctx, `DELETE FROM app_searches`)
	if err != nil {
		return out, fmt.Errorf("reset statistics: %w", err)
	}
	out.Searches, _ = res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return out, fmt.Errorf("reset statistics: %w", err)
	}
	return out, nil
}

// ResetAPIUsage clears the per-key request counters and last-used timestamps.
func (s *Store) ResetAPIUsage(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET requests = 0, last_used_at = 0
		 WHERE requests <> 0 OR last_used_at <> 0`)
	if err != nil {
		return 0, fmt.Errorf("reset API usage: %w", err)
	}
	return res.RowsAffected()
}
