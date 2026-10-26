package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/store"
)

// CapabilitiesFor returns the real capabilities of the selected indexers. An
// empty id list means every enabled indexer. Indexers that cannot build a
// connector are skipped: caps must never take the whole API down because one
// definition is temporarily unusable.
func (m *Manager) CapabilitiesFor(ctx context.Context, ids []int64) (map[int64]engine.Capabilities, error) {
	out := make(map[int64]engine.Capabilities)
	selected := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id > 0 {
			selected[id] = struct{}{}
		}
	}
	indexers, err := m.store.ListIndexers(ctx)
	if err != nil {
		return nil, err
	}
	for _, indexer := range indexers {
		if !indexer.Enabled {
			continue
		}
		if len(selected) > 0 {
			if _, ok := selected[indexer.ID]; !ok {
				continue
			}
		}
		connector, _, err := m.Connector(ctx, indexer.ID)
		if err != nil {
			continue
		}
		out[indexer.ID] = connector.Capabilities()
	}
	return out, nil
}

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
	parsed, err := definition.ParseShared([]byte(def.Body))
	if err != nil {
		return false, fmt.Errorf("indexer %s: parse definition %q: %w",
			indexer.Slug, def.ID, err)
	}
	if stored, ok := indexerConfigBool(indexer.ConfigJSON, "store_latest"); ok && stored {
		// A memorised site must have a real latest page. A definition without
		// one cannot be stored: the scheduler never invents a query for it.
		return parsed.HasLatestList(), nil
	}
	if value, ok := indexerConfigBool(indexer.ConfigJSON, "latest"); ok {
		return value, nil
	}
	return parsed.Meta.HasLatest(), nil
}

func indexerConfigBool(configJSON, key string) (bool, bool) {
	if strings.TrimSpace(configJSON) == "" {
		return false, false
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(configJSON), &values); err != nil {
		return false, false
	}
	raw, ok := values[key]
	if !ok {
		return false, false
	}
	value, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false, false
	}
	return value, true
}
