package indexer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/textutil"
)

// Run executes one indexer into the release cache and records history and
// state. It never touches the legacy sources table.
func (m *Manager) Run(ctx context.Context, indexerID int64) (stats *RunStats, err error) {
	started := time.Now()
	stats = &RunStats{}

	indexer, loadErr := m.store.GetIndexer(ctx, indexerID)
	if loadErr != nil {
		m.finishRun(ctx, indexerID, "", started, 0, loadErr)
		return stats, fmt.Errorf("indexer %d: %w", indexerID, loadErr)
	}

	config, configErr := decodeConfig(indexer.ConfigJSON)
	query := ""
	if configErr == nil {
		query = queryFromConfig(config)
	}
	var payloadBytes int64
	defer func() {
		m.finishRun(ctx, indexer.ID, query, started, payloadBytes, err)
	}()

	if configErr != nil {
		return stats, fmt.Errorf("indexer %s: parse config_json: %w", indexer.Slug, configErr)
	}

	connector, err := m.connectorFor(ctx, indexer)
	if err != nil {
		return stats, err
	}

	categories := categoriesFromValue(config["categories"])
	if len(categories) == 0 {
		categories = connector.Capabilities().Categories
	}

	var items []engine.Item
	if query != "" {
		items, err = connector.Search(ctx, engine.SearchRequest{
			Mode:       engine.ModeSearch,
			Query:      query,
			Categories: categories,
			Limit:      100,
		})
	} else {
		items, err = connector.Latest(ctx, engine.LatestRequest{
			Categories: categories,
			Limit:      100,
		})
	}
	if err != nil {
		return stats, fmt.Errorf("indexer %s: fetch: %w", indexer.Slug, err)
	}
	if err := ctx.Err(); err != nil {
		return stats, err
	}
	stats.Seen = len(items)

	rules, err := m.store.ListBlocklist(ctx)
	if err != nil {
		return stats, fmt.Errorf("indexer %s: load blocklist: %w", indexer.Slug, err)
	}
	matcher := store.NewBlocklistMatcher(rules)

	seen := make(map[string]struct{}, len(items))
	for _, raw := range items {
		if err := ctx.Err(); err != nil {
			return stats, err
		}

		item := raw
		hash := itemHash(item)
		if hash != "" {
			item.InfoHash = hash
		}
		magnet := strings.TrimSpace(item.Magnet)
		if magnet == "" && hash != "" {
			magnet = feed.MagnetFromHash(hash, item.Title)
		}
		item.Magnet = magnet

		if key := itemIdentity(item); key != "" {
			if _, duplicate := seen[key]; duplicate {
				stats.Skipped++
				continue
			}
			seen[key] = struct{}{}
		}

		category := itemCategory(item)
		if _, match := matcher.Match(hash, item.Title, indexer.Name, category); match {
			stats.Blocked++
			continue
		}

		var torrent []byte
		if hash == "" && magnet == "" && strings.TrimSpace(item.DownloadURL) != "" &&
			stats.Torrents < m.maxTorrents {
			payload, downloadErr := connector.Download(ctx, item)
			if downloadErr != nil {
				if ctx.Err() != nil {
					return stats, ctx.Err()
				}
				m.logger.Warn("indexer: torrent download failed",
					"indexer", indexer.Slug,
					"error", m.redact(indexer.ID, downloadErr.Error()))
				stats.Skipped++
				continue
			}
			payloadBytes += int64(len(payload.Body))
			if payload.Magnet != "" {
				magnet = payload.Magnet
				if fromMagnet := feed.MagnetHash(payload.Magnet); fromMagnet != "" {
					hash = fromMagnet
				}
			}
			if len(payload.Body) > 0 {
				torrent = payload.Body
				if hash == "" && item.Protocol != engine.ProtocolUsenet {
					if computed, hashErr := feed.InfoHash(payload.Body); hashErr == nil {
						hash = computed
					}
				}
				stats.Torrents++
			}
			if magnet == "" && hash != "" {
				magnet = feed.MagnetFromHash(hash, item.Title)
			}
			item.Magnet = magnet
			item.InfoHash = hash

			// The .torrent may have revealed an infohash that an infohash rule
			// could not match before the download.
			if _, match := matcher.Match(hash, item.Title, indexer.Name, category); match {
				stats.Blocked++
				continue
			}
		}

		if magnet == "" && len(torrent) == 0 {
			stats.Skipped++
			continue
		}

		dedupKey := releaseKey(hash, item, indexer.Slug)
		if dedupKey == "" {
			stats.Skipped++
			continue
		}

		now := time.Now()
		release := &store.Release{
			DedupKey:    dedupKey,
			InfoHash:    hash,
			IMDbID:      item.IMDbID,
			TMDbID:      item.TMDbID,
			TVDbID:      item.TVDbID,
			Title:       item.Title,
			SearchTitle: textutil.NormalizeTitle(item.Title),
			Size:        item.Size,
			Seeders:     item.Seeders,
			Leechers:    item.Leechers,
			Category:    category,
			PublishedAt: item.Published,
			SourceID:    indexer.LegacySourceID,
			SourceName:  indexer.Name,
			GUID:        item.GUID,
			Magnet:      magnet,
			Torrent:     torrent,
			FirstSeenAt: now,
			LastSeenAt:  now,
		}
		if len(torrent) > 0 {
			release.TorrentFetchedAt = now
		}

		isNew, storeErr := m.store.UpsertRelease(ctx, release)
		if storeErr != nil {
			return stats, fmt.Errorf("indexer %s: store release %q: %w", indexer.Slug, item.Title, storeErr)
		}
		if isNew {
			stats.New++
		} else {
			stats.Updated++
		}

		if provenanceErr := m.store.RecordReleaseSource(ctx, store.ReleaseSource{
			ReleaseID:   release.ID,
			IndexerID:   indexer.ID,
			FirstSeenAt: release.FirstSeenAt,
			LastSeenAt:  release.LastSeenAt,
			Seeders:     item.Seeders,
			Leechers:    item.Leechers,
			DownloadRef: "",
		}); provenanceErr != nil {
			m.logger.Warn("indexer: record release source",
				"indexer", indexer.Slug,
				"release_id", release.ID,
				"error", m.redact(indexer.ID, provenanceErr.Error()))
		}

		if len(stats.Items) < maxRunItems {
			stats.Items = append(stats.Items, item)
		}
	}

	return stats, nil
}

// itemHash returns the normalized infohash from the explicit field or from the
// magnet link.
func itemHash(item engine.Item) string {
	if hash := feed.NormalizeInfoHash(item.InfoHash); hash != "" {
		return hash
	}
	return feed.MagnetHash(item.Magnet)
}

// itemIdentity is the deduplication key inside one run: infohash, then GUID,
// then download URL, then title+size.
func itemIdentity(item engine.Item) string {
	if hash := itemHash(item); hash != "" {
		return "hash:" + hash
	}
	if guid := strings.TrimSpace(item.GUID); guid != "" {
		return "guid:" + guid
	}
	if url := strings.TrimSpace(item.DownloadURL); url != "" {
		return "url:" + url
	}
	title := strings.TrimSpace(item.Title)
	if title == "" {
		return ""
	}
	return "title:" + strings.ToLower(title) + "\x00" + strconv.FormatInt(item.Size, 10)
}

// releaseKey is the persistent deduplication key described by the plan.
func releaseKey(hash string, item engine.Item, slug string) string {
	switch {
	case hash != "":
		return hash
	case strings.TrimSpace(item.GUID) != "":
		sum := sha256.Sum256([]byte(slug + item.GUID))
		return "guid:" + hex.EncodeToString(sum[:])
	case strings.TrimSpace(item.DownloadURL) != "":
		sum := sha256.Sum256([]byte(item.DownloadURL))
		return "url:" + hex.EncodeToString(sum[:])
	default:
		return ""
	}
}

// itemCategory returns the first numeric category, falling back to the Torznab
// "other" category.
func itemCategory(item engine.Item) string {
	for _, category := range item.Categories {
		if category > 0 {
			return strconv.Itoa(category)
		}
	}
	return "8000"
}

// finishRun always records the run and refreshes the indexer state. It uses a
// context that survives cancellation so a canceled run is still auditable.
func (m *Manager) finishRun(ctx context.Context, indexerID int64, query string, started time.Time, payloadBytes int64, runErr error) {
	base := context.WithoutCancel(ctx)
	status := statusFor(ctx, runErr)
	m.recordHistory(base, indexerID, "run", query, status, started, payloadBytes, runErr)

	if indexerID <= 0 {
		return
	}
	state, stateErr := m.store.GetIndexerState(base, indexerID)
	if stateErr != nil && !errors.Is(stateErr, sql.ErrNoRows) {
		m.logger.Warn("indexer: load state", "indexer_id", indexerID, "error", stateErr)
		return
	}
	state.IndexerID = indexerID
	if strings.TrimSpace(state.Circuit) == "" {
		state.Circuit = "closed"
	}
	state.LastError = ""
	if runErr != nil {
		state.LastError = m.redact(indexerID, errorText(runErr))
	}
	state.UpdatedAt = time.Now()
	if err := m.store.UpsertIndexerState(base, state); err != nil {
		m.logger.Warn("indexer: update state", "indexer_id", indexerID, "error", err)
	}
}
