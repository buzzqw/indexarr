package store

import (
	"context"
	"fmt"
	"time"
)

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
