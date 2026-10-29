package indexer

import (
	"context"
	"encoding/json"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/store"
)

// pendingSearchSaver returns the callback that keeps an indexer's queue of
// flood-deferred search pages in the database, so a restart resumes it.
func (m *Manager) pendingSearchSaver(indexer store.Indexer) func([]engine.PendingSearch) {
	return func(queue []engine.PendingSearch) {
		encoded := ""
		if len(queue) > 0 {
			data, err := json.Marshal(queue)
			if err != nil {
				m.logger.Warn("indexer: encode pending searches", "indexer", indexer.Slug, "error", err)
				return
			}
			encoded = string(data)
		}
		if err := m.store.SetPendingSearches(context.Background(), indexer.ID, encoded); err != nil {
			m.logger.Warn("indexer: save pending searches", "indexer", indexer.Slug, "error", err)
		}
	}
}

// resumePendingSearches hands the saved queue to a freshly cached connector.
func (m *Manager) resumePendingSearches(ctx context.Context, indexer store.Indexer, connector engine.Connector) {
	resumer, ok := connector.(interface {
		ResumePendingSearches([]engine.PendingSearch)
	})
	if !ok || !indexer.Enabled {
		// A disabled indexer keeps its queue for when it is enabled again; a
		// Test on it must not start sending the queued pages.
		return
	}
	saved, err := m.store.PendingSearches(ctx, indexer.ID)
	if err != nil || saved == "" {
		if err != nil {
			m.logger.Warn("indexer: load pending searches", "indexer", indexer.Slug, "error", err)
		}
		return
	}
	var queue []engine.PendingSearch
	if err := json.Unmarshal([]byte(saved), &queue); err != nil {
		m.logger.Warn("indexer: decode pending searches", "indexer", indexer.Slug, "error", err)
		_ = m.store.SetPendingSearches(ctx, indexer.ID, "")
		return
	}
	resumer.ResumePendingSearches(queue)
}

// ResumePendingSearches builds the connector of every enabled indexer with a
// saved queue, which resumes it. It runs once at startup: without it a queue
// would wait for the first search or run of its indexer.
func (m *Manager) ResumePendingSearches(ctx context.Context) {
	if m == nil {
		return
	}
	ids, err := m.store.IndexersWithPendingSearches(ctx)
	if err != nil {
		m.logger.Warn("indexer: list pending searches", "error", err)
		return
	}
	for _, id := range ids {
		if _, _, err := m.Connector(ctx, id); err != nil {
			m.logger.Warn("indexer: resume pending searches", "indexer_id", id, "error", err)
			continue
		}
		m.logger.Info("indexer: resumed pending searches", "indexer_id", id)
	}
}
