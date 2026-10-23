package indexer

import (
	"context"
	"fmt"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
)

// LatestIndexerIDs returns the enabled indexers that publish a list of new
// releases. The *arr RSS path uses it to read only those caches: a search-only
// site never contributes to the RSS feed.
func (m *Manager) LatestIndexerIDs(ctx context.Context) ([]int64, error) {
	indexers, err := m.store.ListIndexers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(indexers))
	for _, indexer := range indexers {
		if !indexer.Enabled {
			continue
		}
		latest, err := m.LatestSupported(ctx, indexer)
		if err != nil {
			continue
		}
		if latest {
			out = append(out, indexer.ID)
		}
	}
	return out, nil
}

// LatestSupported reports whether an indexer publishes a list or feed of new
// releases, and therefore has a scheduled run. The value comes from the
// definition's additive meta.latest field: absent means true, so every
// definition written before the field existed keeps its cadence.
func (m *Manager) LatestSupported(ctx context.Context, indexer store.Indexer) (bool, error) {
	def, err := m.store.GetDefinition(ctx, indexer.DefinitionID)
	if err != nil {
		return false, fmt.Errorf("indexer %s: load definition %q: %w",
			indexer.Slug, indexer.DefinitionID, err)
	}
	parsed, err := definition.Parse([]byte(def.Body))
	if err != nil {
		return false, fmt.Errorf("indexer %s: parse definition %q: %w",
			indexer.Slug, def.ID, err)
	}
	return parsed.Meta.HasLatest(), nil
}
