package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DownloadConfirmation is one download a client reports as completed. The
// client sends it after the payload reaches 100%, so it is the only evidence
// indexarr has that a grab turned into a real file. Protocol is "torrent" or
// "ed2k"; Hash is the infohash or the eD2k MD4. Title is the release title the
// client downloaded: it is the fallback when indexarr did not know the hash
// when it served the link.
type DownloadConfirmation struct {
	Protocol    string
	Hash        string
	Title       string
	Client      string
	Size        int64
	CompletedAt time.Time
}

// ConfirmDownload marks the grab that produced a completed download. A client
// can report the same completion more than once; only the first report touches
// a row, so the call is idempotent.
//
// The release is matched by its infohash or eD2k hash. When the hash is not in
// the cache the title is used, and the hash is stored on the release so the
// next report finds it directly. The grab is the newest unconfirmed delivery
// of that release at or before the completion time: a repeated grab for the
// same release must not make an older delivery look confirmed.
//
// It returns the confirmed grab id, or zero when nothing matched.
func (s *Store) ConfirmDownload(ctx context.Context, in DownloadConfirmation) (int64, error) {
	protocol := strings.ToLower(strings.TrimSpace(in.Protocol))
	if protocol != "torrent" && protocol != "ed2k" {
		return 0, fmt.Errorf("confirm download: unknown protocol %q", in.Protocol)
	}
	hash := strings.ToLower(strings.TrimSpace(in.Hash))
	title := strings.TrimSpace(in.Title)
	if len(title) > 300 {
		title = title[:300]
	}
	if hash == "" && title == "" {
		return 0, nil
	}
	client := strings.TrimSpace(in.Client)
	if len(client) > 80 {
		client = client[:80]
	}
	completed := in.CompletedAt
	if completed.IsZero() {
		completed = time.Now()
	}
	if completed.After(time.Now().Add(5 * time.Minute)) {
		completed = time.Now()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("confirm download: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	releaseID, err := confirmRelease(ctx, tx, protocol, hash, title, completed)
	if err != nil {
		return 0, err
	}
	grabID, err := confirmGrab(ctx, tx, releaseID, title, completed)
	if err != nil {
		return 0, err
	}
	if grabID == 0 {
		return 0, nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE indexer_history
		SET confirmed_at = ?, confirmed_by = ?
		WHERE id = ? AND confirmed_at = 0`,
		completed.Unix(), client, grabID)
	if err != nil {
		return 0, fmt.Errorf("confirm download: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("confirm download: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("confirm download: %w", err)
	}
	if updated == 0 {
		return 0, nil
	}
	return grabID, nil
}

// confirmRelease finds the release a confirmation belongs to. A hash match is
// exact; the title is the fallback for a release indexarr served without a
// hash (a site page that only carries the download link), and in that case the
// hash is kept for the next report.
func confirmRelease(ctx context.Context, tx *sql.Tx, protocol, hash, title string, completed time.Time) (int64, error) {
	column := "infohash"
	if protocol == "ed2k" {
		column = "ed2k_hash"
	}
	if hash != "" {
		var id int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM releases WHERE `+column+` = ? COLLATE NOCASE AND `+column+` <> ''
			 ORDER BY id DESC LIMIT 1`, hash).Scan(&id)
		switch {
		case err == nil:
			return id, nil
		case !errors.Is(err, sql.ErrNoRows):
			return 0, fmt.Errorf("confirm download: find release: %w", err)
		}
	}
	if title == "" {
		return 0, nil
	}
	var id int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM releases
		WHERE title = ? COLLATE NOCASE
			AND downloaded_at > 0
			AND downloaded_at <= ?
		ORDER BY downloaded_at DESC, id DESC LIMIT 1`,
		title, completed.Add(5*time.Minute).Unix()).Scan(&id)
	switch {
	case err == nil:
		if hash != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE releases SET `+column+` = ? WHERE id = ? AND `+column+` = ''`,
				hash, id); err != nil {
				return 0, fmt.Errorf("confirm download: store hash: %w", err)
			}
		}
		return id, nil
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	default:
		return 0, fmt.Errorf("confirm download: find release by title: %w", err)
	}
}

// confirmGrab finds the delivery that produced the completed file: the newest
// unconfirmed grab at or before the completion. When the release is unknown,
// the title narrows the search to a week before the completion so an old
// download with the same name cannot be picked.
func confirmGrab(ctx context.Context, tx *sql.Tx, releaseID int64, title string, completed time.Time) (int64, error) {
	limit := completed.Add(5 * time.Minute).Unix()
	if releaseID > 0 {
		var id int64
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM indexer_history
			WHERE kind = 'grab' AND release_id = ? AND confirmed_at = 0
				AND started_at <= ?
			ORDER BY started_at DESC, id DESC LIMIT 1`,
			releaseID, limit).Scan(&id)
		switch {
		case err == nil:
			return id, nil
		case errors.Is(err, sql.ErrNoRows):
			return 0, nil
		default:
			return 0, fmt.Errorf("confirm download: find grab: %w", err)
		}
	}
	if title == "" {
		return 0, nil
	}
	var id int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM indexer_history
		WHERE kind = 'grab' AND confirmed_at = 0
			AND query = ? COLLATE NOCASE
			AND started_at <= ? AND started_at >= ?
		ORDER BY started_at DESC, id DESC LIMIT 1`,
		title, limit, completed.Add(-7*24*time.Hour).Unix()).Scan(&id)
	switch {
	case err == nil:
		return id, nil
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	default:
		return 0, fmt.Errorf("confirm download: find grab by title: %w", err)
	}
}
