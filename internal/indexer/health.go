package indexer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/store"
)

// HealthResult is what one health check found.
type HealthResult struct {
	Query   string
	Items   int
	MinRows int
}

// Health runs the definition's health.query through the cached connector and
// records a history row of kind "health". It is the query used by the WP9
// periodic job, not a scheduled run: it does not write releases.
func (m *Manager) Health(ctx context.Context, indexerID int64) (HealthResult, error) {
	started := time.Now()
	stats := &RunStats{}
	result := HealthResult{Query: "test", MinRows: 1}

	connector, indexer, err := m.Connector(ctx, indexerID)
	if err != nil {
		m.recordHistory(ctx, indexerID, "health", result.Query, statusFor(ctx, err), started, 0, err, stats)
		return result, err
	}
	if parsed, parseErr := m.definitionFor(ctx, indexer); parseErr == nil {
		result.Query, result.MinRows = healthPlan(parsed)
	}

	capture := newRunCapture(m.sampleSecrets(ctx, indexer.ID))
	ctx = engine.WithResponseObserver(ctx, capture.observe)
	ctx = engine.WithNormalizeObserver(ctx, func(observation engine.NormalizeObservation) {
		if !observation.OK {
			stats.discardParse()
		}
	})
	items, err := connector.Search(ctx, engine.SearchRequest{
		Mode:  engine.ModeSearch,
		Query: result.Query,
		Limit: 100,
	})
	if err != nil {
		err = fmt.Errorf("indexer %s: health: %w", indexer.Slug, err)
	} else {
		stats.Seen = len(items)
		result.Items = len(items)
		for _, item := range items {
			stats.observeItem(item, capture.secrets)
		}
		if len(items) < result.MinRows {
			err = fmt.Errorf("indexer %s: health query %q returned %d items, want at least %d",
				indexer.Slug, result.Query, len(items), result.MinRows)
		}
	}
	m.recordHistory(ctx, indexer.ID, "health", result.Query, statusFor(ctx, err), started, 0, err, stats)
	return result, err
}

// definitionFor loads and parses the definition behind one indexer.
func (m *Manager) definitionFor(ctx context.Context, indexer store.Indexer) (*definition.Definition, error) {
	stored, err := m.store.GetDefinition(ctx, indexer.DefinitionID)
	if err != nil {
		return nil, err
	}
	return definition.Parse([]byte(stored.Body))
}

// healthPlan applies the defaults documented in DEFINITIONS.md: query "test"
// and at least one row. An explicit empty query is preserved, because feeds
// answer a health check with their latest items rather than a title filter.
func healthPlan(def *definition.Definition) (string, int) {
	query, minRows := "test", 1
	if def == nil || def.Health == nil {
		return query, minRows
	}
	query = strings.TrimSpace(def.Health.Query)
	if def.Health.MinRows > 0 {
		minRows = def.Health.MinRows
	}
	return query, minRows
}
