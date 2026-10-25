package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ReleaseFile is one file name inside a stored torrent.
type ReleaseFile struct {
	Path string
	Size int64
}

// maxFilesPerRelease bounds the work and the database growth for a hostile or
// pathological torrent. Real releases stay far below this.
const maxFilesPerRelease = 5000

// ReplaceReleaseFiles stores the file list of one release. The FTS table is
// maintained manually because its rows reference a release, not a file id.
// An empty list removes the previous rows.
func (s *Store) ReplaceReleaseFiles(ctx context.Context, releaseID int64, files []ReleaseFile) error {
	if releaseID <= 0 {
		return fmt.Errorf("store: invalid release id %d", releaseID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM release_files_fts WHERE release_id = ?`, releaseID); err != nil {
		return fmt.Errorf("store: clear file FTS: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM release_files WHERE release_id = ?`, releaseID); err != nil {
		return fmt.Errorf("store: clear files: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE releases SET files_parsed_at = ? WHERE id = ?`,
		time.Now().Unix(), releaseID); err != nil {
		return fmt.Errorf("store: mark files parsed: %w", err)
	}
	seen := make(map[string]bool, len(files))
	inserted := 0
	for _, file := range files {
		path := strings.TrimSpace(file.Path)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO release_files(release_id, path, size) VALUES(?, ?, ?)`,
			releaseID, path, file.Size); err != nil {
			return fmt.Errorf("store: insert file: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO release_files_fts(release_id, path) VALUES(?, ?)`,
			releaseID, path); err != nil {
			return fmt.Errorf("store: index file: %w", err)
		}
		inserted++
		if inserted >= maxFilesPerRelease {
			break
		}
	}
	return tx.Commit()
}

// TorrentWithoutFiles is a release payload waiting for the file-list backfill.
type TorrentWithoutFiles struct {
	ID      int64
	Torrent []byte
}

// ReleasesWithoutFiles returns stored torrents whose file list has not been
// parsed yet. The caller parses them and calls ReplaceReleaseFiles.
func (s *Store) ReleasesWithoutFiles(ctx context.Context, limit int) ([]TorrentWithoutFiles, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.id, r.torrent FROM releases r
		WHERE r.torrent IS NOT NULL AND length(r.torrent) > 0
		  AND r.files_parsed_at = 0
		ORDER BY r.id
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TorrentWithoutFiles
	for rows.Next() {
		var item TorrentWithoutFiles
		var payload []byte
		if err := rows.Scan(&item.ID, &payload); err != nil {
			return nil, err
		}
		item.Torrent = payload
		out = append(out, item)
	}
	return out, rows.Err()
}

// ReleaseFileCount returns the number of file names stored for one release.
func (s *Store) ReleaseFileCount(ctx context.Context, releaseID int64) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM release_files WHERE release_id = ?`, releaseID).Scan(&count)
	return count, err
}
