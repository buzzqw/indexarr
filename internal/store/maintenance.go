package store

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	Sources         int64
	Runs            int64
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
			(SELECT COUNT(*) FROM sources),
			(SELECT COUNT(*) FROM runs),
			(SELECT COUNT(*) FROM blocklist),
			(SELECT COUNT(*) FROM saved_searches),
			(SELECT COUNT(*) FROM api_keys),
			(SELECT COUNT(*) FROM settings)`,
	).Scan(&r.Releases, &r.Sources, &r.Runs, &r.Blocklist, &r.SavedSearches, &r.APIKeys, &r.Settings); err != nil {
		return r, fmt.Errorf("count rows: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN torrent IS NOT NULL THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN magnet <> '' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN magnet = '' AND torrent IS NULL THEN 1 ELSE 0 END), 0),
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

// ResetSourceErrors clears the last error and the consecutive-failure counter
// of every source. It does not re-enable a source that was disabled.
func (s *Store) ResetSourceErrors(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE sources SET last_error = '', consecutive_errors = 0, updated_at = ?
		 WHERE last_error <> '' OR consecutive_errors <> 0`, time.Now().Unix())
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

// DeleteReleasesBySource removes every cached release belonging to a source.
func (s *Store) DeleteReleasesBySource(ctx context.Context, sourceID int64) (int64, error) {
	if sourceID <= 0 {
		return 0, errors.New("store: invalid source id")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM releases WHERE source_id = ?`, sourceID)
	if err != nil {
		return 0, fmt.Errorf("delete releases by source: %w", err)
	}
	return res.RowsAffected()
}

// DeleteReleasesWithoutLinks removes rows that have neither a magnet nor a
// stored .torrent file, so clients cannot use them.
func (s *Store) DeleteReleasesWithoutLinks(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM releases WHERE magnet = '' AND torrent IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("delete releases without links: %w", err)
	}
	return res.RowsAffected()
}

// DeleteHiddenReleases removes every release hidden from the UI.
func (s *Store) DeleteHiddenReleases(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM releases WHERE hidden = 1`)
	if err != nil {
		return 0, fmt.Errorf("delete hidden releases: %w", err)
	}
	return res.RowsAffected()
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

// RecalculateSourceCounts replaces total_items with the number of releases
// currently cached for each source. Useful after pruning or deleting a source.
func (s *Store) RecalculateSourceCounts(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE sources SET total_items = (
			SELECT COUNT(*) FROM releases WHERE releases.source_id = sources.id
		), updated_at = ?`, time.Now().Unix())
	if err != nil {
		return 0, fmt.Errorf("recalculate source counts: %w", err)
	}
	return res.RowsAffected()
}
