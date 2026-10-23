package indexer

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/textutil"
)

var liveSeasonEpisode = regexp.MustCompile(`(?i)s\d{1,2}[ ._-]?e\d{1,3}`)

// SearchLive queries every enabled Torznab indexer on demand, stores what it
// returns and reports how many items were new or updated. It is the native
// replacement for the live search that used to live in the legacy ingest
// package: HTML/feed indexers are skipped because they cannot answer a query.
func (m *Manager) SearchLive(ctx context.Context, filter store.Filter) (int, error) {
	query := strings.TrimSpace(filter.Query)
	if query == "" {
		return 0, nil
	}
	if filter.Season > 0 && filter.Episode > 0 && !liveSeasonEpisode.MatchString(query) {
		query += fmt.Sprintf(" S%02dE%02d", filter.Season, filter.Episode)
	}
	indexers, err := m.store.ListIndexers(ctx)
	if err != nil {
		return 0, err
	}
	rules, err := m.store.ListBlocklist(ctx)
	if err != nil {
		m.logger.Warn("live search: cannot load blocklist", "error", err)
	}
	matcher := store.NewBlocklistMatcher(rules)

	var (
		mu     sync.Mutex
		stored int
		wg     sync.WaitGroup
		sem    = make(chan struct{}, 3)
	)
	for _, indexer := range indexers {
		if !indexer.Enabled || !m.isTorznab(ctx, indexer) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(indexer store.Indexer) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			config, configErr := decodeConfig(indexer.ConfigJSON)
			if configErr != nil {
				m.logger.Warn("live search: parse config", "indexer", indexer.Slug, "error", configErr)
				return
			}
			categories := categoriesFromValue(config["categories"])
			items, searchErr := m.Search(ctx, indexer.ID, engine.SearchRequest{
				Mode:       engine.ModeSearch,
				Query:      query,
				Categories: categories,
				Limit:      50,
			})
			if searchErr != nil {
				m.logger.Warn("live search indexer failed",
					"indexer", indexer.Slug, "error", m.redact(indexer.ID, searchErr.Error()))
				return
			}
			count, storeErr := m.storeLiveItems(ctx, indexer, items, matcher)
			if storeErr != nil {
				m.logger.Warn("live search: store", "indexer", indexer.Slug,
					"error", m.redact(indexer.ID, storeErr.Error()))
				return
			}
			if count > 0 {
				mu.Lock()
				stored += count
				mu.Unlock()
			}
		}(indexer)
	}
	wg.Wait()
	if stored == 0 && ctx.Err() != nil {
		return 0, ctx.Err()
	}
	return stored, nil
}

// isTorznab reports whether the indexer uses the Torznab family connector.
func (m *Manager) isTorznab(ctx context.Context, indexer store.Indexer) bool {
	def, err := m.store.GetDefinition(ctx, indexer.DefinitionID)
	if err != nil {
		return false
	}
	parsed, err := definition.Parse([]byte(def.Body))
	if err != nil {
		return false
	}
	return connectorName(parsed, def) == "torznab"
}

// storeLiveItems writes the items of a live search into the cache using the
// same identity and provenance rules as a scheduled run.
func (m *Manager) storeLiveItems(ctx context.Context, indexer store.Indexer, items []engine.Item, matcher *store.BlocklistMatcher) (int, error) {
	connector, _, err := m.Connector(ctx, indexer.ID)
	if err != nil {
		return 0, err
	}
	stored := 0
	seen := make(map[string]struct{}, len(items))
	for _, raw := range items {
		if ctx.Err() != nil {
			return stored, ctx.Err()
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
				continue
			}
			seen[key] = struct{}{}
		}
		category := itemCategory(item)
		if _, blocked := matcher.Match(hash, item.Title, indexer.Name, category); blocked {
			continue
		}
		var torrent []byte
		if hash == "" && magnet == "" && strings.TrimSpace(item.DownloadURL) != "" {
			payload, downloadErr := connector.Download(ctx, item)
			if downloadErr != nil {
				m.logger.Warn("live search: download failed", "indexer", indexer.Slug,
					"error", m.redact(indexer.ID, downloadErr.Error()))
				continue
			}
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
			}
			if magnet == "" && hash != "" {
				magnet = feed.MagnetFromHash(hash, item.Title)
			}
			item.Magnet = magnet
			item.InfoHash = hash
		}
		if magnet == "" && len(torrent) == 0 {
			continue
		}
		dedupKey := releaseKey(hash, item, indexer.Slug)
		if dedupKey == "" {
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
			GUID:        item.GUID,
			Magnet:      magnet,
			Torrent:     torrent,
			Protocol:    "torrent",
			FirstSeenAt: now,
			LastSeenAt:  now,
		}
		if len(torrent) > 0 {
			release.TorrentFetchedAt = now
		}
		if _, err := m.store.UpsertRelease(ctx, release); err != nil {
			return stored, err
		}
		if err := m.store.RecordReleaseSource(ctx, store.ReleaseSource{
			ReleaseID:   release.ID,
			IndexerID:   indexer.ID,
			FirstSeenAt: release.FirstSeenAt,
			LastSeenAt:  release.LastSeenAt,
			Seeders:     item.Seeders,
			Leechers:    item.Leechers,
		}); err != nil {
			m.logger.Warn("live search: provenance", "indexer", indexer.Slug, "error", err)
		}
		stored++
	}
	return stored, nil
}
