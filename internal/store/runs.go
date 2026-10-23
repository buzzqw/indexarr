package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RunResult is what a single ingestion run did.
type RunResult struct {
	Seen      int64
	New       int64
	Updated   int64
	Blocked   int64
	Torrents  int64
	NewTitles []string
}

// Run is one row of the run history.
type Run struct {
	ID              int64
	SourceID        int64
	SourceName      string
	StartedAt       time.Time
	FinishedAt      time.Time
	Status          string
	ItemsSeen       int64
	ItemsNew        int64
	ItemsUpdated    int64
	ItemsBlocked    int64
	TorrentsFetched int64
	Error           string
	DurationMS      int64
}

// ListRuns returns the most recent runs, optionally filtered by source.
func (s *Store) ListRuns(ctx context.Context, sourceID int64, onlyErrors bool, limit int) ([]Run, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	query := `SELECT id, COALESCE(source_id, 0), source_name, started_at, finished_at,
		status, items_seen, items_new, items_updated, items_blocked, torrents_fetched, error, duration_ms
		FROM runs`
	var clauses []string
	var args []any
	if sourceID > 0 {
		clauses = append(clauses, `source_id = ?`)
		args = append(args, sourceID)
	}
	if onlyErrors {
		clauses = append(clauses, `status = 'error'`)
	}
	if len(clauses) > 0 {
		query += ` WHERE ` + joinAnd(clauses)
	}
	query += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var run Run
		var started, finished int64
		if err := rows.Scan(&run.ID, &run.SourceID, &run.SourceName, &started, &finished,
			&run.Status, &run.ItemsSeen, &run.ItemsNew, &run.ItemsUpdated,
			&run.ItemsBlocked, &run.TorrentsFetched, &run.Error, &run.DurationMS); err != nil {
			return nil, err
		}
		run.StartedAt = unixTime(started)
		run.FinishedAt = unixTime(finished)
		out = append(out, run)
	}
	return out, rows.Err()
}

// ClearRuns deletes runs older than before and returns how many were removed.
func (s *Store) ClearRuns(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM runs WHERE started_at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// FinishInterruptedRuns marks runs left "running" by a previous process as
// interrupted. Called once at startup.
func (s *Store) FinishInterruptedRuns(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE runs SET status = 'error', error = 'interrupted: indexarr stopped during the run',
			finished_at = ?
		WHERE status = 'running'`, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetRun returns one run by id.
func (s *Store) GetRun(ctx context.Context, id int64) (Run, error) {
	var run Run
	var started, finished int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(source_id, 0), source_name, started_at, finished_at,
			status, items_seen, items_new, items_updated, items_blocked, torrents_fetched, error, duration_ms
		FROM runs WHERE id = ?`, id).Scan(&run.ID, &run.SourceID, &run.SourceName,
		&started, &finished, &run.Status, &run.ItemsSeen, &run.ItemsNew,
		&run.ItemsUpdated, &run.ItemsBlocked, &run.TorrentsFetched, &run.Error, &run.DurationMS)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("run %d: %w", id, sql.ErrNoRows)
	}
	run.StartedAt = unixTime(started)
	run.FinishedAt = unixTime(finished)
	return run, err
}

func joinAnd(clauses []string) string {
	out := ""
	for i, clause := range clauses {
		if i > 0 {
			out += " AND "
		}
		out += clause
	}
	return out
}
