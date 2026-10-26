package store

import (
	"context"
	"fmt"
	"time"
)

// SourceDownloadStat is one source's share of the downloads served in a
// window. A download row is written when indexarr resolves a payload for a
// grab, so it measures the results that were actually used, not just cached.
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
		WHERE h.kind = 'download' AND h.status = 'ok' AND h.started_at >= ?
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
