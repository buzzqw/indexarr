package store

import (
	"context"
	"database/sql"
	"fmt"
)

// cleanupTorrentGalaxyHashlessTx removes the TGx provenance of a hashless
// release when another row of the same title from the same TGx indexer already
// carries an infohash. The release row itself is removed only when no other
// source remains, so a shared release is never deleted.
func cleanupTorrentGalaxyHashlessTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT r.id
		FROM releases r
		JOIN release_sources s ON s.release_id = r.id
		JOIN indexers i ON i.id = s.indexer_id AND i.definition_id = 'torrentgalaxy'
		WHERE r.infohash = '' AND r.magnet = '' AND r.guid = ''
		  AND EXISTS (
			SELECT 1
			FROM release_sources s2
			JOIN releases r2 ON r2.id = s2.release_id
			WHERE s2.indexer_id = s.indexer_id
			  AND r2.infohash <> ''
			  AND r2.search_title = r.search_title
			  AND r2.title = r.title
		  )`)
	if err != nil {
		return 0, fmt.Errorf("find hashless TGx rows: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM release_sources
			WHERE release_id = ?
			  AND indexer_id IN (SELECT id FROM indexers WHERE definition_id = 'torrentgalaxy')`, id); err != nil {
			return 0, fmt.Errorf("drop hashless TGx provenance: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM releases
			WHERE id = ? AND NOT EXISTS (SELECT 1 FROM release_sources WHERE release_id = releases.id)`, id); err != nil {
			return 0, fmt.Errorf("delete orphan hashless TGx release: %w", err)
		}
	}
	return int64(len(ids)), nil
}

// CleanupTorrentGalaxyHashless runs the idempotent cleanup outside a startup
// migration. Tests and maintenance can call it directly.
func (s *Store) CleanupTorrentGalaxyHashless(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n, err := cleanupTorrentGalaxyHashlessTx(ctx, tx)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}
