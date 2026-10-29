package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PendingSearches returns the saved queue of deferred search pages of one
// indexer, or "" when nothing is pending.
func (s *Store) PendingSearches(ctx context.Context, indexerID int64) (string, error) {
	var queue string
	err := s.db.QueryRowContext(ctx,
		`SELECT queue_json FROM indexer_pending_searches WHERE indexer_id = ?`, indexerID).Scan(&queue)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("pending searches: %w", err)
	}
	return queue, nil
}

// SetPendingSearches saves the queue of one indexer. An empty queue removes
// the row.
func (s *Store) SetPendingSearches(ctx context.Context, indexerID int64, queue string) error {
	if indexerID <= 0 {
		return nil
	}
	if queue == "" || queue == "[]" || queue == "null" {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM indexer_pending_searches WHERE indexer_id = ?`, indexerID); err != nil {
			return fmt.Errorf("clear pending searches: %w", err)
		}
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO indexer_pending_searches(indexer_id, queue_json, updated_at)
		VALUES(?, ?, ?)
		ON CONFLICT(indexer_id) DO UPDATE SET
			queue_json = excluded.queue_json,
			updated_at = excluded.updated_at`,
		indexerID, queue, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("set pending searches: %w", err)
	}
	return nil
}

// IndexersWithPendingSearches lists the enabled indexers that have a saved
// queue, so the process can resume them at startup.
func (s *Store) IndexersWithPendingSearches(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.indexer_id FROM indexer_pending_searches p
		JOIN indexers i ON i.id = p.indexer_id
		WHERE i.enabled = 1
		ORDER BY p.indexer_id`)
	if err != nil {
		return nil, fmt.Errorf("indexers with pending searches: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan pending searches: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
