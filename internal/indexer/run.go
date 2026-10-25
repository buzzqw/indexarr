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
	"github.com/buzzqw/indexarr/internal/smartfeed"
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
		m.finishRun(ctx, indexerID, "", started, 0, loadErr, stats)
		return stats, fmt.Errorf("indexer %d: %w", indexerID, loadErr)
	}

	config, configErr := decodeConfig(indexer.ConfigJSON)
	query := ""
	if configErr == nil {
		query = queryFromConfig(config)
	}
	var payloadBytes int64
	var downloadProblem error
	var capture *runCapture
	defer func() {
		if err == nil && capture != nil {
			m.recordSample(context.WithoutCancel(ctx), indexer.ID, capture, stats)
		}
		m.finishRun(ctx, indexer.ID, query, started, payloadBytes, err, stats)
		m.store.InvalidateCounts()
	}()

	if configErr != nil {
		return stats, fmt.Errorf("indexer %s: parse config_json: %w", indexer.Slug, configErr)
	}

	if err := m.checkQueryLimit(ctx, indexer.ID); err != nil {
		return stats, err
	}

	connector, err := m.connectorFor(ctx, indexer)
	if err != nil {
		return stats, err
	}
	capture = newRunCapture(m.sampleSecrets(ctx, indexer.ID))
	ctx = engine.WithResponseObserver(ctx, capture.observe)
	ctx = engine.WithNormalizeObserver(ctx, func(observation engine.NormalizeObservation) {
		if !observation.OK {
			stats.discardParse()
		}
	})
	runRules := newRunFilter(config, time.Now())
	if _, explicit := config["min_size_mb"]; !explicit && isUploaderList(indexer, config) {
		// Uploader lists are the one place where a 50 MB floor is on by
		// default: samples, NFOs and artwork are noise there.
		runRules.minSize = 50 << 20
	}
	storedSource := settingBool(settingString(config["store_latest"]))
	// A MirCrew Smartfeed channel lists pending topics. The feed itself never
	// opens or thanks a topic: the paired native indexer is used only when a
	// client later asks for the download.
	smartfeedSource := strings.EqualFold(strings.TrimSpace(indexer.DefinitionID), smartfeed.DefinitionID) ||
		settingBool(settingString(config["smartfeed"]))
	var nativePair store.Indexer
	var hasNativePair, pairLoaded bool

	categories := categoriesFromValue(config["categories"])
	// A memorised search-only site follows the categories the linked apps
	// actually consume. An explicit category choice still narrows the result.
	if storedSource {
		if roots, rootsErr := m.store.LinkedAppCategoryRoots(ctx, indexer.ID); rootsErr == nil && len(roots) > 0 {
			if len(categories) == 0 {
				categories = roots
			} else {
				categories = intersectCategoryRoots(categories, roots)
			}
		}
	}
	if len(categories) == 0 {
		categories = connector.Capabilities().Categories
	}

	var items []engine.Item
	if query != "" {
		items, err = connector.Search(ctx, engine.SearchRequest{
			Mode:       engine.ModeSearch,
			Query:      query,
			Categories: categories,
		})
	} else {
		items, err = connector.Latest(ctx, engine.LatestRequest{
			Categories: categories,
			MaxAge:     time.Duration(intFromConfig(config["max_age_days"])) * 24 * time.Hour,
			Known: func(item engine.Item) bool {
				_, found, lookupErr := m.store.FindSameRelease(ctx, &store.Release{
					Title:    item.Title,
					Size:     item.Size,
					InfoHash: itemHash(item),
				})
				return lookupErr == nil && found
			},
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

	usenet := engine.Protocol(strings.ToLower(strings.TrimSpace(indexer.Protocol))) == engine.ProtocolUsenet
	usenetItems, torrentItems := 0, 0
	seen := make(map[string]struct{}, len(items))
	for _, raw := range items {
		if err := ctx.Err(); err != nil {
			return stats, err
		}

		item := raw
		if item.Published.IsZero() {
			// A feed item without a date is not discarded: its first sighting
			// is the publication date indexarr can defend.
			item.Published = time.Now()
		}
		stats.observeItem(item, capture.secrets)
		itemUsenet := item.Protocol == engine.ProtocolUsenet || (usenet && item.Protocol == "")
		if itemUsenet {
			usenetItems++
			if err := m.runUsenetItem(ctx, indexer, item, stats, seen, matcher, runRules, &payloadBytes); err != nil {
				return stats, err
			}
			continue
		}
		torrentItems++
		hash := itemHash(item)
		if hash != "" {
			item.InfoHash = hash
		}
		magnet := strings.TrimSpace(item.Magnet)
		if magnet == "" && hash != "" {
			magnet = feed.MagnetFromHash(hash, item.Title)
		}
		item.Magnet = magnet
		pending := smartfeedSource && strings.EqualFold(strings.TrimSpace(item.Attributes[smartfeed.AttributePending]), "true")

		if key := itemIdentity(item); key != "" {
			if _, duplicate := seen[key]; duplicate {
				stats.duplicate()
				continue
			}
			seen[key] = struct{}{}
		}

		if tooOld, excluded, tooSmall := runRules.reject(item); tooOld || excluded || tooSmall {
			switch {
			case tooOld:
				stats.discardTooOld()
			case tooSmall:
				stats.discardOther()
			default:
				stats.discardCategory()
			}
			continue
		}

		category := itemCategory(item)
		if _, match := matcher.Match(hash, item.Title, indexer.Name, category); match {
			stats.Blocked++
			continue
		}

		var torrent []byte
		if magnet == "" && strings.TrimSpace(item.DownloadURL) != "" &&
			stats.Torrents < m.maxTorrents {
			payload, downloadErr := connector.Download(ctx, item)
			if downloadErr != nil {
				if ctx.Err() != nil {
					return stats, ctx.Err()
				}
				m.logger.Warn("indexer: torrent download failed",
					"indexer", indexer.Slug,
					"error", m.redact(indexer.ID, downloadErr.Error()))
				stats.discardOther()
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

		if magnet == "" && len(torrent) == 0 && !pending {
			stats.discardNoLink()
			continue
		}

		dedupKey := releaseKey(hash, item, indexer.Slug)
		if dedupKey == "" {
			stats.discardOther()
			continue
		}

		// The pending topic points at the native MirCrew indexer, not at the
		// RSS channel: opening the topic and thanking it belong to the grab.
		// Nothing is downloaded or opened here, so a poll can never thank.
		downloadRef := ""
		if pending {
			if !pairLoaded {
				nativePair, hasNativePair = m.smartfeedNativePair(ctx, indexer, config)
				pairLoaded = true
			}
			if hasNativePair && m.vault != nil {
				if ref, refErr := SealDownloadRef(m.vault, nativePair.ID, item); refErr == nil {
					downloadRef = ref
				} else {
					m.logger.Warn("indexer: seal Smartfeed topic reference",
						"indexer", indexer.Slug, "error", refErr)
				}
			}
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

		// Stored sources apply the cautious §1g merge: same normalized title,
		// size within rounding tolerance and compatible dates become one row
		// with an extra source, never a second release. The live search keeps
		// its own path unchanged.
		if storedSource {
			if dup, found, dupErr := m.store.FindStoredDuplicate(ctx, *release); dupErr != nil {
				m.logger.Warn("indexer: find stored duplicate",
					"indexer", indexer.Slug, "error", m.redact(indexer.ID, dupErr.Error()))
			} else if found {
				release.DedupKey = dup.DedupKey
				if release.InfoHash == "" {
					release.InfoHash = dup.InfoHash
				}
			}
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
			DownloadRef: downloadRef,
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

	if downloadProblem != nil {
		return stats, downloadProblem
	}
	if indexer.DefinitionID == "rss" && usenetItems > 0 && torrentItems == 0 && indexer.Protocol != "usenet" {
		_ = m.store.SetIndexerProtocol(ctx, indexer.ID, "usenet")
	} else if indexer.DefinitionID == "rss" && torrentItems > 0 && usenetItems == 0 && indexer.Protocol != "torrent" {
		_ = m.store.SetIndexerProtocol(ctx, indexer.ID, "torrent")
	}
	return stats, nil
}

// runUsenetItem stores one Usenet release. There is no infohash: the GUID is
// the identity and the downloaded .nzb (or an opaque, vault-sealed download
// reference) is the link the Newznab API later serves.
func (m *Manager) runUsenetItem(ctx context.Context, indexer store.Indexer, item engine.Item, stats *RunStats,
	seen map[string]struct{}, matcher *store.BlocklistMatcher, rules runFilter, payloadBytes *int64) error {
	item.Protocol = engine.ProtocolUsenet
	guid := strings.TrimSpace(item.GUID)
	downloadURL := strings.TrimSpace(item.DownloadURL)
	if guid == "" && downloadURL == "" {
		stats.discardNoLink()
		return nil
	}
	if key := usenetIdentity(item); key != "" {
		if _, duplicate := seen[key]; duplicate {
			stats.duplicate()
			return nil
		}
		seen[key] = struct{}{}
	}

	if tooOld, excluded, tooSmall := rules.reject(item); tooOld || excluded || tooSmall {
		switch {
		case tooOld:
			stats.discardTooOld()
		case tooSmall:
			stats.discardOther()
		default:
			stats.discardCategory()
		}
		return nil
	}

	category := itemCategory(item)
	if _, match := matcher.Match("", item.Title, indexer.Name, category); match {
		stats.Blocked++
		return nil
	}

	dedupKey := releaseKey("", item, indexer.Slug)
	if dedupKey == "" {
		stats.discardOther()
		return nil
	}

	// A previous run may already have the .nzb: update the metadata without
	// spending another download.
	var body []byte
	var payloadType string
	existing, found, err := m.store.ReleaseByKey(ctx, dedupKey)
	if err != nil {
		return fmt.Errorf("indexer %s: load release %q: %w", indexer.Slug, item.Title, err)
	}
	if found {
		switch {
		case len(existing.Payload) > 0:
			body = existing.Payload
			payloadType = existing.PayloadType
		case len(existing.Torrent) > 0:
			body = existing.Torrent
			payloadType = existing.PayloadType
		}
	}

	magnet := strings.TrimSpace(item.Magnet)
	if len(body) == 0 && magnet == "" && downloadURL != "" && stats.Torrents < m.maxTorrents {
		payload, downloadErr := m.Download(ctx, indexer.ID, item)
		if downloadErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m.logger.Warn("indexer: usenet download failed",
				"indexer", indexer.Slug,
				"error", m.redact(indexer.ID, downloadErr.Error()))
			stats.discardOther()
			return nil
		}
		if len(payload.Body) > 0 {
			body = payload.Body
			payloadType = strings.TrimSpace(payload.ContentType)
			if payloadType == "" {
				payloadType = "application/x-nzb"
			}
			*payloadBytes += int64(len(payload.Body))
			stats.Torrents++
		}
		if magnet == "" {
			magnet = strings.TrimSpace(payload.Magnet)
		}
	}
	if len(body) > 0 && payloadType == "" {
		payloadType = "application/x-nzb"
	}

	// Without a payload, keep only an opaque reference. The raw tracker URL
	// (which carries the passkey) never reaches the database.
	downloadRef := ""
	if len(body) == 0 && magnet == "" && downloadURL != "" {
		if m.vault == nil {
			stats.discardOther()
			return nil
		}
		ref, refErr := SealDownloadRef(m.vault, indexer.ID, item)
		if refErr != nil {
			m.logger.Warn("indexer: seal download reference",
				"indexer", indexer.Slug, "error", refErr)
			stats.discardOther()
			return nil
		}
		downloadRef = ref
	}
	if len(body) == 0 && magnet == "" && downloadRef == "" {
		stats.discardNoLink()
		return nil
	}

	now := time.Now()
	release := &store.Release{
		DedupKey:    dedupKey,
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
		Protocol:    "usenet",
		Payload:     body,
		PayloadType: payloadType,
		FirstSeenAt: now,
		LastSeenAt:  now,
	}

	isNew, storeErr := m.store.UpsertRelease(ctx, release)
	if storeErr != nil {
		return fmt.Errorf("indexer %s: store release %q: %w", indexer.Slug, item.Title, storeErr)
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
		DownloadRef: downloadRef,
	}); provenanceErr != nil {
		m.logger.Warn("indexer: record release source",
			"indexer", indexer.Slug,
			"release_id", release.ID,
			"error", m.redact(indexer.ID, provenanceErr.Error()))
	}

	if len(stats.Items) < maxRunItems {
		stats.Items = append(stats.Items, item)
	}
	return nil
}

// smartfeedNativePair finds the MirCrew search indexer that belongs to the
// same site. It runs once per poll and only when a pending topic needs a
// sealed grab reference, so a missing native indexer never blocks the feed.
func (m *Manager) smartfeedNativePair(ctx context.Context, source store.Indexer, sourceConfig map[string]any) (store.Indexer, bool) {
	indexers, err := m.store.ListIndexers(ctx)
	if err != nil {
		return store.Indexer{}, false
	}
	site := strings.TrimSpace(settingString(sourceConfig["site"]))
	if site == "" {
		site = smartfeed.Site
	}
	var fallback store.Indexer
	fallbacks := 0
	for _, candidate := range indexers {
		if !strings.EqualFold(strings.TrimSpace(candidate.DefinitionID), smartfeed.NativeDefinitionID) {
			continue
		}
		config, configErr := decodeConfig(candidate.ConfigJSON)
		if configErr != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(settingString(config["site"])), site) {
			return candidate, true
		}
		fallback = candidate
		fallbacks++
	}
	// A native indexer created before channels existed has no site key. If it
	// is the only MirCrew indexer, pairing it is unambiguous.
	if fallbacks == 1 {
		return fallback, true
	}
	return store.Indexer{}, false
}

func usenetIdentity(item engine.Item) string {
	if guid := strings.TrimSpace(item.GUID); guid != "" {
		return "guid:" + guid
	}
	if url := strings.TrimSpace(item.DownloadURL); url != "" {
		return "url:" + url
	}
	return ""
}

// itemHash returns the normalized infohash from the explicit field or from the
// magnet link.
// intersectCategoryRoots keeps the configured categories that belong to a
// root consumed by a linked app. An empty result falls back to the roots, so a
// misconfigured explicit list cannot silently stop a stored source.
func intersectCategoryRoots(configured, roots []int) []int {
	out := make([]int, 0, len(configured))
	for _, category := range configured {
		for _, root := range roots {
			if categoryMatches([]int{root}, category) {
				out = append(out, category)
				break
			}
		}
	}
	if len(out) == 0 {
		return append([]int(nil), roots...)
	}
	return dedupeCategories(out)
}

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
	case strings.TrimSpace(item.DetailsURL) != "":
		sum := sha256.Sum256([]byte("details:" + item.DetailsURL))
		return "details:" + hex.EncodeToString(sum[:])
	default:
		title := strings.TrimSpace(item.Title)
		if title == "" {
			return ""
		}
		sum := sha256.Sum256([]byte(slug + "\x00" + strings.ToLower(title) +
			"\x00" + strconv.FormatInt(item.Size, 10)))
		return "title:" + hex.EncodeToString(sum[:])
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
func (m *Manager) finishRun(ctx context.Context, indexerID int64, query string, started time.Time, payloadBytes int64, runErr error, stats *RunStats) {
	base := context.WithoutCancel(ctx)
	status := statusFor(ctx, runErr)
	m.recordHistory(base, indexerID, "run", query, status, started, payloadBytes, runErr, stats)

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
