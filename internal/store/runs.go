package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RunHistory is one scheduled run of a native indexer, read from
// indexer_history. The legacy runs table no longer exists.
type RunHistory struct {
	ID          int64
	IndexerID   int64
	IndexerName string
	Query       string
	Status      string
	LatencyMS   int64
	Error       string
	StartedAt   time.Time
}

// ListRunHistory returns the most recent run history rows, optionally filtered
// by indexer and by failures only.
func (s *Store) ListRunHistory(ctx context.Context, indexerID int64, onlyErrors bool, limit int) ([]RunHistory, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	query := `SELECT h.id, COALESCE(h.indexer_id, 0), COALESCE(i.name, ''),
		h.query, h.status, h.latency_ms, h.error, h.started_at
		FROM indexer_history h
		LEFT JOIN indexers i ON i.id = h.indexer_id
		WHERE h.kind = 'run'`
	var args []any
	if indexerID > 0 {
		query += ` AND h.indexer_id = ?`
		args = append(args, indexerID)
	}
	if onlyErrors {
		query += ` AND h.status = 'error'`
	}
	query += ` ORDER BY h.started_at DESC, h.id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunHistory
	for rows.Next() {
		var run RunHistory
		var started int64
		if err := rows.Scan(&run.ID, &run.IndexerID, &run.IndexerName, &run.Query,
			&run.Status, &run.LatencyMS, &run.Error, &started); err != nil {
			return nil, err
		}
		run.StartedAt = unixTime(started)
		out = append(out, run)
	}
	return out, rows.Err()
}

// GetRunHistory returns one run history row by id.
func (s *Store) GetRunHistory(ctx context.Context, id int64) (RunHistory, error) {
	var run RunHistory
	var started int64
	err := s.db.QueryRowContext(ctx, `
		SELECT h.id, COALESCE(h.indexer_id, 0), COALESCE(i.name, ''),
			h.query, h.status, h.latency_ms, h.error, h.started_at
		FROM indexer_history h
		LEFT JOIN indexers i ON i.id = h.indexer_id
		WHERE h.kind = 'run' AND h.id = ?`, id).Scan(&run.ID, &run.IndexerID,
		&run.IndexerName, &run.Query, &run.Status, &run.LatencyMS, &run.Error, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return RunHistory{}, fmt.Errorf("run %d: %w", id, sql.ErrNoRows)
	}
	run.StartedAt = unixTime(started)
	return run, err
}

// ClearRunHistory deletes run history older than before.
func (s *Store) ClearRunHistory(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM indexer_history WHERE kind = 'run' AND started_at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RecentIndexerRunStatuses returns the statuses of the most recent runs of one
// indexer, newest first. The native scheduler uses it to auto-disable an
// indexer after repeated failures.
func (s *Store) RecentIndexerRunStatuses(ctx context.Context, indexerID int64, limit int) ([]string, error) {
	if indexerID <= 0 || limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT status FROM indexer_history
		WHERE kind = 'run' AND indexer_id = ?
		ORDER BY started_at DESC, id DESC LIMIT ?`, indexerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return nil, err
		}
		out = append(out, status)
	}
	return out, rows.Err()
}
