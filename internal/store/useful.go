package store

import (
	"context"
	"fmt"
	"time"
)

// SourceDownloadStat is one source's share of the downloads served in a
// window. It counts grab rows: a torrent or NZB served to an app, or a magnet
// redirect. Internal "download" rows (a .torrent fetched to read its infohash)
// are not downloads the user asked for and are left out.
type SourceDownloadStat struct {
	IndexerID    int64
	Name         string
	DefinitionID string
	Downloads    int64
}

// SourceDownloadStats counts successful downloads per source since the given
// instant, newest first in the result order (largest first).
func (s *Store) SourceDownloadStats(ctx context.Context, since time.Time) ([]SourceDownloadStat, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.name, i.definition_id, COUNT(*) AS downloads
		FROM indexer_history h
		JOIN indexers i ON i.id = h.indexer_id
		WHERE h.kind = 'grab' AND h.started_at >= ?
		GROUP BY i.id, i.name, i.definition_id
		ORDER BY downloads DESC, i.name`, since.Unix())
	if err != nil {
		return nil, fmt.Errorf("source download stats: %w", err)
	}
	defer rows.Close()
	var out []SourceDownloadStat
	for rows.Next() {
		var item SourceDownloadStat
		if err := rows.Scan(&item.IndexerID, &item.Name, &item.DefinitionID, &item.Downloads); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GrabStat counts the grabs one source served in a window, split by delivery.
// Ed2k covers the eD2k links indexarr handed over itself (the list download and
// the send to aMule): a client that opens the ed2k:// link straight from the
// feed never reaches indexarr.
type GrabStat struct {
	IndexerID int64
	Name      string
	Torrents  int64
	Magnets   int64
	NZBs      int64
	Ed2k      int64
}

// Total is every grab of the source.
func (g GrabStat) Total() int64 { return g.Torrents + g.Magnets + g.NZBs + g.Ed2k }

// GrabStatsByIndexer returns the grabs served per source since the given
// instant (zero means all), largest first.
func (s *Store) GrabStatsByIndexer(ctx context.Context, since time.Time) ([]GrabStat, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT i.id, i.name,
			SUM(CASE WHEN h.status NOT IN ('magnet', 'nzb', 'ed2k') THEN 1 ELSE 0 END),
			SUM(CASE WHEN h.status = 'magnet' THEN 1 ELSE 0 END),
			SUM(CASE WHEN h.status = 'nzb' THEN 1 ELSE 0 END),
			SUM(CASE WHEN h.status = 'ed2k' THEN 1 ELSE 0 END)
		FROM indexer_history h
		JOIN indexers i ON i.id = h.indexer_id
		WHERE h.kind = 'grab' AND h.started_at >= ?
		GROUP BY i.id, i.name
		ORDER BY COUNT(*) DESC, i.name COLLATE NOCASE`, sinceUnix(since))
	if err != nil {
		return nil, fmt.Errorf("grab stats: %w", err)
	}
	defer rows.Close()
	var out []GrabStat
	for rows.Next() {
		var item GrabStat
		if err := rows.Scan(&item.IndexerID, &item.Name, &item.Torrents, &item.Magnets, &item.NZBs, &item.Ed2k); err != nil {
			return nil, fmt.Errorf("scan grab stats: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// Grab is one download served to an app.
type Grab struct {
	ID          int64
	IndexerID   int64
	IndexerName string
	Title       string
	Via         string
	App         string
	At          time.Time
}

// ListGrabs returns the newest grabs since the given instant.
func (s *Store) ListGrabs(ctx context.Context, since time.Time, limit, offset int) ([]Grab, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT h.id, COALESCE(h.indexer_id, 0), COALESCE(i.name, ''), h.query, h.status,
			h.app, h.started_at
		FROM indexer_history h
		LEFT JOIN indexers i ON i.id = h.indexer_id
		WHERE h.kind = 'grab' AND h.started_at >= ?
		ORDER BY h.started_at DESC, h.id DESC
		LIMIT ? OFFSET ?`, sinceUnix(since), limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list grabs: %w", err)
	}
	defer rows.Close()
	var out []Grab
	for rows.Next() {
		var item Grab
		var at int64
		if err := rows.Scan(&item.ID, &item.IndexerID, &item.IndexerName, &item.Title, &item.Via, &item.App, &at); err != nil {
			return nil, fmt.Errorf("scan grab: %w", err)
		}
		item.At = time.Unix(at, 0)
		out = append(out, item)
	}
	return out, rows.Err()
}

// CountGrabs counts the grabs since the given instant.
func (s *Store) CountGrabs(ctx context.Context, since time.Time) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM indexer_history WHERE kind = 'grab' AND started_at >= ?`,
		sinceUnix(since)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count grabs: %w", err)
	}
	return count, nil
}
