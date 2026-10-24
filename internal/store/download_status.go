package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// QuarantineDuration maps the number of repeated 404s to the link quarantine.
// The first failure is only recorded; the second, third and fourth add 6 h,
// 24 h and 7 days. A successful grab clears the counter and the quarantine.
func QuarantineDuration(failures int64) time.Duration {
	switch {
	case failures >= 4:
		return 7 * 24 * time.Hour
	case failures == 3:
		return 24 * time.Hour
	case failures == 2:
		return 6 * time.Hour
	default:
		return 0
	}
}

// RecordSourceDownloadFailure counts a repeated dead-link response (404/410)
// for one source and returns the new counter and quarantine deadline.
func (s *Store) RecordSourceDownloadFailure(ctx context.Context, releaseID, indexerID int64, message string) (int64, time.Time, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, time.Time{}, err
	}
	defer tx.Rollback()
	var failures int64
	if err := tx.QueryRowContext(ctx,
		`SELECT download_failures FROM release_sources WHERE release_id = ? AND indexer_id = ?`,
		releaseID, indexerID).Scan(&failures); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, time.Time{}, fmt.Errorf("release source: %w", err)
		}
		return 0, time.Time{}, err
	}
	failures++
	var until int64
	if d := QuarantineDuration(failures); d > 0 {
		until = time.Now().Add(d).Unix()
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE release_sources SET download_failures = ?, quarantined_until = ?,
			last_download_error = ? WHERE release_id = ? AND indexer_id = ?`,
		failures, until, trimError(message), releaseID, indexerID); err != nil {
		return 0, time.Time{}, err
	}
	if err := tx.Commit(); err != nil {
		return 0, time.Time{}, err
	}
	return failures, unixTime(until), nil
}

// RecordSourceDownloadNotice stores a login/challenge/temporary error without
// incrementing the dead-link counter. It is shown in the release card.
func (s *Store) RecordSourceDownloadNotice(ctx context.Context, releaseID, indexerID int64, message string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE release_sources SET last_download_error = ? WHERE release_id = ? AND indexer_id = ?`,
		trimError(message), releaseID, indexerID)
	return err
}

// ClearSourceDownloadFailure resets the counter, quarantine and last error.
func (s *Store) ClearSourceDownloadFailure(ctx context.Context, releaseID, indexerID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE release_sources SET download_failures = 0, quarantined_until = 0,
			last_download_error = '' WHERE release_id = ? AND indexer_id = ?`,
		releaseID, indexerID)
	return err
}

func trimError(message string) string {
	const limit = 240
	message = strings.TrimSpace(message)
	if len(message) > limit {
		return message[:limit] + "…"
	}
	return message
}
