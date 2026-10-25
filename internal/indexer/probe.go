package indexer

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

// LatestProbe is the measured estimate shown before a fourth stored source is
// activated. It comes from one real read of the site's latest list; nothing is
// written to the database.
type LatestProbe struct {
	// Items is the number of distinct items read in the trial page.
	Items int
	// Dated is the number of items that carried a usable publication date.
	Dated int
	// Window is the observed time between the oldest and newest item.
	Window time.Duration
	// PerDay is the measured release rate. It is a lower bound when the page
	// filled up in less than a day: the page cannot show more than it holds.
	PerDay int64
	// LowerBound is true when the sample filled the requested limit. The UI
	// must show "at least" because the real rate can be higher.
	LowerBound bool
	// AvgBytes is the measured database space per release (rows, sources and
	// FTS), not the payload size of the torrent: a stored release keeps
	// metadata, not its content.
	AvgBytes int64
	// BytesPerMonth is PerDay * AvgBytes * 30.
	BytesPerMonth int64
	// DBSize is the size of the database at probe time, including WAL.
	DBSize int64
}

// ProbeLatest reads the latest list of one indexer without storing anything.
// It is the measurement behind the fourth-source warning.
func (m *Manager) ProbeLatest(ctx context.Context, indexerID int64, limit int) (LatestProbe, error) {
	indexer, err := m.store.GetIndexer(ctx, indexerID)
	if err != nil {
		return LatestProbe{}, fmt.Errorf("probe indexer %d: %w", indexerID, err)
	}
	config, err := decodeConfig(indexer.ConfigJSON)
	if err != nil {
		return LatestProbe{}, fmt.Errorf("probe indexer %s: parse config_json: %w", indexer.Slug, err)
	}
	connector, err := m.connectorFor(ctx, indexer)
	if err != nil {
		return LatestProbe{}, err
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	categories := categoriesFromValue(config["categories"])
	if len(categories) == 0 {
		categories = connector.Capabilities().Categories
	}
	items, err := connector.Latest(ctx, engine.LatestRequest{
		Categories:  categories,
		Limit:       limit,
		SkipDetails: true,
	})
	if err != nil {
		return LatestProbe{}, fmt.Errorf("probe indexer %s: latest: %w", indexer.Slug, err)
	}
	dbSize, _ := m.store.SizeBytes()
	stats, _ := m.store.CachedStats(ctx)
	avg := int64(2500) // measured default: about 2.5 KB per release with FTS
	if stats.Releases > 0 && dbSize > 0 {
		avg = dbSize / stats.Releases
		if avg < 1000 {
			avg = 1000
		}
	}
	probe := EstimateLatest(items, avg, dbSize)
	probe.LowerBound = len(items) >= limit
	return probe, nil
}

// EstimateLatest turns one latest-list page into the estimate used by the
// warning. fallbackAvgBytes is the measured database space per release; the
// payload size of an item is deliberately ignored because indexarr stores
// metadata, not the torrent content. It is pure so the arithmetic is testable
// without a network.
func EstimateLatest(items []engine.Item, fallbackAvgBytes, dbSize int64) LatestProbe {
	probe := LatestProbe{Items: len(items), DBSize: dbSize}
	if len(items) == 0 {
		probe.AvgBytes = fallbackAvgBytes
		return probe
	}
	var oldest, newest time.Time
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		key := strings.TrimSpace(item.InfoHash)
		if key == "" {
			key = strings.TrimSpace(item.GUID)
		}
		if key == "" {
			key = fmt.Sprintf("%s|%d", strings.ToLower(strings.TrimSpace(item.Title)), item.Size)
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		if item.Published.IsZero() {
			continue
		}
		probe.Dated++
		if oldest.IsZero() || item.Published.Before(oldest) {
			oldest = item.Published
		}
		if newest.IsZero() || item.Published.After(newest) {
			newest = item.Published
		}
	}
	probe.Items = len(seen)
	if probe.Items == 0 {
		probe.AvgBytes = fallbackAvgBytes
		return probe
	}
	probe.AvgBytes = fallbackAvgBytes
	if !oldest.IsZero() && !newest.IsZero() {
		probe.Window = newest.Sub(oldest)
	}
	days := 1.0
	if probe.Window > 24*time.Hour {
		days = probe.Window.Hours() / 24
	}
	probe.PerDay = int64(math.Round(float64(probe.Items) / days))
	if probe.PerDay < 1 {
		probe.PerDay = 1
	}
	probe.BytesPerMonth = probe.PerDay * probe.AvgBytes * 30
	return probe
}
