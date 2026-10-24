package indexer

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/textutil"
)

var liveSeasonEpisode = regexp.MustCompile(`(?i)s\d{1,2}[ ._-]?e\d{1,3}`)

const (
	// liveGlobalConcurrency bounds the fan-out of every Torznab request. The
	// per-host limiter of the shared transport still applies underneath, so
	// eight sites are queried at a time while one host never exceeds its own
	// rate.
	liveGlobalConcurrency = 16

	// liveBackgroundTimeout is the safety limit for an indexer that missed the
	// request deadline. It keeps working in the background so its result is
	// already cached on the next request, but it cannot run forever.
	liveBackgroundTimeout = 3 * time.Minute
)

// LiveQuery is one app-facing Torznab search. It is independent of the store
// filter because it also carries the year and the IDs the live fan-out needs.
type LiveQuery struct {
	Mode       string
	Query      string
	Season     int
	Episode    int
	Year       int
	IMDbID     string
	TMDbID     string
	TVDbID     string
	Categories []int
	Limit      int
	// IndexerIDs restricts the fan-out to the indexers selected by the
	// request (an aggregate filter or the single-indexer endpoint). Empty
	// means every enabled indexer.
	IndexerIDs []int64
}

// LiveIndexerResult is what one indexer did during a live search. Mode is the
// effective mode: it differs from the requested one when the fan-out fell back
// to a text search, and Fallback says so explicitly.
type LiveIndexerResult struct {
	IndexerID int64
	Name      string
	Mode      string
	Fallback  bool
	Query     string
	Results   int
	Duration  time.Duration
	TimedOut  bool
	Skipped   bool
	Err       string
}

// LiveResult is the partial response of the fan-out. Indexers that were still
// running when the request deadline expired are reported with TimedOut true;
// they keep working in the background and cache their result.
type LiveResult struct {
	Stored   int
	Indexers []LiveIndexerResult
}

// SearchLive queries every enabled indexer that can answer the requested mode,
// stores what it returns and reports one outcome per indexer. The call returns
// when every indexer answered or when ctx expires; the remaining indexers are
// detached and continue in the background.
func (m *Manager) SearchLive(ctx context.Context, req LiveQuery) (result LiveResult, err error) {
	result = LiveResult{}
	if !liveQueryPresent(req) {
		return result, nil
	}
	indexers, err := m.store.ListIndexers(ctx)
	if err != nil {
		return result, err
	}
	rules, err := m.store.ListBlocklist(ctx)
	if err != nil {
		m.logger.Warn("live search: cannot load blocklist", "error", err)
	}
	matcher := store.NewBlocklistMatcher(rules)

	selected := make(map[int64]struct{}, len(req.IndexerIDs))
	for _, id := range req.IndexerIDs {
		if id > 0 {
			selected[id] = struct{}{}
		}
	}

	// A background context without the request deadline: an indexer that is
	// late keeps running, bounded by liveBackgroundTimeout.
	workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), liveBackgroundTimeout)
	var done chan struct{}
	defer func() {
		// The wait group closes done when every worker has finished. Only then
		// the timeout and its context are released.
		if done != nil {
			go func() {
				<-done
				cancel()
			}()
			return
		}
		cancel()
	}()

	var targets []store.Indexer
	for _, indexer := range indexers {
		if !indexer.Enabled {
			continue
		}
		if len(selected) > 0 {
			if _, ok := selected[indexer.ID]; !ok {
				continue
			}
		}
		targets = append(targets, indexer)
	}
	if len(targets) == 0 {
		return result, nil
	}
	// A site that already answered this exact title with zero results and no
	// timeout is unlikely to change within a few hours. Remembering that in
	// the search history keeps the cold path short across restarts without
	// dropping titles: any positive answer cancels the skip.
	if zeros := m.zeroIndexers(ctx, req); len(zeros) > 0 {
		kept := targets[:0]
		for _, target := range targets {
			if zeros[target.ID] {
				m.logger.Debug("live search: skip known-zero indexer", "indexer", target.Slug)
				continue
			}
			kept = append(kept, target)
		}
		targets = kept
		if len(targets) == 0 {
			return result, nil
		}
	}

	// A short-lived response cache absorbs Sonarr/Radarr repetitions and the
	// offset pages that follow the first one. It is keyed by the effective
	// query and the selected indexers, so a different request never reuses it.
	cacheTTL := 5 * time.Minute
	if settings, settingsErr := m.store.LoadSettings(ctx); settingsErr == nil {
		if settings.TorznabLiveCache <= 0 {
			cacheTTL = 0
		} else {
			cacheTTL = time.Duration(settings.TorznabLiveCache) * time.Second
		}
	}
	cacheKey := liveCacheKey(req, targets)
	if cacheTTL > 0 {
		if cached, ok := m.cachedLive(cacheKey); ok {
			return cached, nil
		}
	}

	// Identical live queries already in flight share one fan-out. This is
	// especially useful when Sonarr fires the same title from two requests.
	flight, leader := m.joinLiveFlight(cacheKey)
	if !leader {
		select {
		case <-flight.done:
			return flight.result, flight.err
		case <-ctx.Done():
			return LiveResult{}, ctx.Err()
		}
	}
	defer func() { m.finishLiveFlight(cacheKey, flight, result, err) }()

	results := make(chan LiveIndexerResult, len(targets))
	var wg sync.WaitGroup
	done = make(chan struct{})
	for _, target := range targets {
		wg.Add(1)
		go func(indexer store.Indexer) {
			defer wg.Done()
			outcome := m.liveOne(workCtx, indexer, req, matcher)
			results <- outcome
		}(target)
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	pending := make(map[int64]store.Indexer, len(targets))
	for _, target := range targets {
		pending[target.ID] = target
	}
	completed, stored := 0, 0
	softTimer := time.NewTimer(600 * time.Millisecond)
	defer softTimer.Stop()
	softFired := false

	finishPartial := func() LiveResult {
		// Drain whatever completed in the same instant, then report the rest
		// as timed out. Workers continue under workCtx and cache their result
		// for the next request.
		for {
			select {
			case outcome := <-results:
				delete(pending, outcome.IndexerID)
				result.Indexers = append(result.Indexers, outcome)
				result.Stored += outcome.Results
			default:
				goto drained
			}
		}
	drained:
		for id, indexer := range pending {
			result.Indexers = append(result.Indexers, LiveIndexerResult{
				IndexerID: id,
				Name:      indexer.Name,
				Mode:      requestedMode(req),
				TimedOut:  true,
			})
		}
		m.cacheLive(cacheKey, cacheTTL, result)
		return result
	}

	for len(pending) > 0 {
		select {
		case outcome := <-results:
			delete(pending, outcome.IndexerID)
			result.Indexers = append(result.Indexers, outcome)
			result.Stored += outcome.Results
			completed++
			stored += outcome.Results
			if softFired && stored == 0 && completed >= 3 {
				return finishPartial(), nil
			}
		case <-softTimer.C:
			// Adaptive wait: a query that is finding nothing anywhere can
			// return as soon as the fast indexers have answered; a query with
			// results waits for the full configured window so late sites can
			// contribute.
			softFired = true
			if stored == 0 && completed >= 3 {
				return finishPartial(), nil
			}
		case <-ctx.Done():
			return finishPartial(), nil
		}
	}
	m.cacheLive(cacheKey, cacheTTL, result)
	return result, nil
}

// zeroIndexers returns the indexers that recently answered the same query
// with zero stored results. Timeouts, skips and errors are neutral. The
// lookback is short enough that newly published releases are seen again.
func (m *Manager) zeroIndexers(ctx context.Context, req LiveQuery) map[int64]bool {
	query := textutil.NormalizeTitle(strings.TrimSpace(req.Query))
	if query == "" {
		return nil
	}
	rows, err := m.store.ListAppSearches(ctx, 300, 0)
	if err != nil {
		return nil
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	cutoff := time.Now().Add(-6 * time.Hour)
	zero := make(map[int64]bool)
	positive := make(map[int64]bool)
	for _, row := range rows {
		if row.CreatedAt.Before(cutoff) {
			continue
		}
		if mode != "" && !strings.EqualFold(row.Mode, mode) {
			continue
		}
		if textutil.NormalizeTitle(strings.TrimSpace(row.Query)) != query {
			continue
		}
		// A completed search with no result at all is the strongest zero
		// signal, even when every slow indexer timed out. Without this, the
		// alternate titles Sonarr tries would each pay a fresh five-second
		// window for the same empty answer.
		if row.TotalResults == 0 && row.Error == "" {
			for _, entry := range row.Live {
				if entry.IndexerID > 0 {
					zero[entry.IndexerID] = true
				}
			}
			continue
		}
		for _, entry := range row.Live {
			if entry.IndexerID <= 0 {
				continue
			}
			switch {
			case entry.Results > 0:
				positive[entry.IndexerID] = true
			case entry.TimedOut || entry.Skipped || entry.Error != "":
				// neutral
			default:
				zero[entry.IndexerID] = true
			}
		}
	}
	for id := range positive {
		delete(zero, id)
	}
	return zero
}

// liveFirstPageOnly reports an uploader list. Those pages do not depend on
// the query: opening three pages only multiplies the wait without changing
// the answer. A normal search keeps its definition pagination.
func liveFirstPageOnly(configJSON string) bool {
	config, err := decodeConfig(configJSON)
	if err != nil {
		return false
	}
	if settingString(config["uploader"]) != "" {
		return true
	}
	return strings.Contains(settingString(config["filter"]), "u=")
}

func (m *Manager) joinLiveFlight(key string) (*liveFlight, bool) {
	m.liveFlightMu.Lock()
	defer m.liveFlightMu.Unlock()
	if flight, ok := m.liveFlights[key]; ok {
		return flight, false
	}
	flight := &liveFlight{done: make(chan struct{})}
	m.liveFlights[key] = flight
	return flight, true
}

func (m *Manager) finishLiveFlight(key string, flight *liveFlight, result LiveResult, err error) {
	m.liveFlightMu.Lock()
	flight.result = result
	flight.err = err
	close(flight.done)
	delete(m.liveFlights, key)
	m.liveFlightMu.Unlock()
}

// liveCacheKey normalizes the fields that change a fan-out response.
func liveCacheKey(req LiveQuery, targets []store.Indexer) string {
	ids := make([]int64, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, target.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	categories := append([]int(nil), req.Categories...)
	sort.Ints(categories)
	return strings.Join([]string{
		strings.ToLower(strings.TrimSpace(req.Mode)),
		textutil.NormalizeTitle(req.Query),
		strconv.Itoa(req.Season),
		strconv.Itoa(req.Episode),
		strconv.Itoa(req.Year),
		strings.ToLower(strings.TrimSpace(req.IMDbID)),
		strings.ToLower(strings.TrimSpace(req.TMDbID)),
		strings.ToLower(strings.TrimSpace(req.TVDbID)),
		fmt.Sprint(ids),
		fmt.Sprint(categories),
		strconv.Itoa(req.Limit),
	}, "|")
}

func (m *Manager) cachedLive(key string) (LiveResult, bool) {
	m.liveCacheMu.Lock()
	defer m.liveCacheMu.Unlock()
	entry, ok := m.liveCache[key]
	if !ok {
		return LiveResult{}, false
	}
	if time.Now().After(entry.expires) {
		delete(m.liveCache, key)
		return LiveResult{}, false
	}
	out := entry.result
	out.Indexers = append([]LiveIndexerResult(nil), entry.result.Indexers...)
	return out, true
}

func (m *Manager) cacheLive(key string, ttl time.Duration, result LiveResult) {
	if ttl <= 0 || key == "" {
		return
	}
	m.liveCacheMu.Lock()
	defer m.liveCacheMu.Unlock()
	now := time.Now()
	for storedKey, entry := range m.liveCache {
		if now.After(entry.expires) {
			delete(m.liveCache, storedKey)
		}
	}
	if len(m.liveCache) >= liveCacheMaxEntries {
		var oldestKey string
		var oldest time.Time
		for storedKey, entry := range m.liveCache {
			if oldestKey == "" || entry.created.Before(oldest) {
				oldestKey, oldest = storedKey, entry.created
			}
		}
		if oldestKey != "" {
			delete(m.liveCache, oldestKey)
		}
	}
	m.liveCache[key] = liveCacheEntry{
		result:  result,
		expires: now.Add(ttl),
		created: now,
	}
}

// liveOne runs one indexer. It is deliberately independent of the request
// context so a late indexer still caches its result after the fan-out returned.
func (m *Manager) liveOne(ctx context.Context, indexer store.Indexer, req LiveQuery, matcher *store.BlocklistMatcher) LiveIndexerResult {
	started := time.Now()
	outcome := LiveIndexerResult{IndexerID: indexer.ID, Name: indexer.Name, Mode: requestedMode(req)}

	select {
	case m.liveSem <- struct{}{}:
		defer func() { <-m.liveSem }()
	case <-ctx.Done():
		outcome.Err = "live search canceled"
		outcome.Duration = time.Since(started)
		return outcome
	}

	connector, _, err := m.Connector(ctx, indexer.ID)
	if err != nil {
		outcome.Err = m.redact(indexer.ID, errorText(err))
		outcome.Duration = time.Since(started)
		return outcome
	}
	plan, ok, reason := planLiveSearch(connector.Capabilities(), req)
	if !ok {
		outcome.Skipped = true
		outcome.Err = reason
		outcome.Duration = time.Since(started)
		return outcome
	}
	outcome.Mode = string(plan.mode)
	outcome.Fallback = plan.fallback
	outcome.Query = plan.request.Query
	if liveFirstPageOnly(indexer.ConfigJSON) {
		plan.request.FirstPageOnly = true
	}

	// The transport breaker is the existing per-indexer breaker. When it is
	// open the indexer is excluded from the fan-out instead of being hit with
	// a request that is going to fail immediately.
	if m.breakerOpen(indexer.ID, indexer.Slug) {
		outcome.Skipped = true
		outcome.Err = "circuit breaker open"
		outcome.Duration = time.Since(started)
		return outcome
	}

	items, err := m.Search(ctx, indexer.ID, plan.request)
	if err != nil {
		outcome.Err = m.redact(indexer.ID, errorText(err))
		outcome.Duration = time.Since(started)
		return outcome
	}
	count, err := m.storeLiveItems(ctx, indexer, items, matcher)
	if err != nil {
		outcome.Err = m.redact(indexer.ID, errorText(err))
	}
	outcome.Results = count
	outcome.Duration = time.Since(started)
	return outcome
}

type livePlan struct {
	request  engine.SearchRequest
	mode     engine.Mode
	fallback bool
}

// planLiveSearch decides the effective mode and query. It mirrors the
// fallbacks used by Jackett and Prowlarr: a TV search without tv-search
// support becomes a text search for "SxxEyy", a movie search appends the year,
// and an ID-only request is skipped instead of inventing a query.
func planLiveSearch(caps engine.Capabilities, req LiveQuery) (livePlan, bool, string) {
	if caps.LocalOnly {
		return livePlan{}, false, "feed-only indexer: cache only"
	}
	mode := engine.Mode(strings.ToLower(strings.TrimSpace(req.Mode)))
	if mode == "" {
		mode = engine.ModeSearch
	}
	if !mode.Valid() {
		return livePlan{}, false, "unsupported search mode"
	}
	query := strings.TrimSpace(req.Query)
	plan := livePlan{
		mode: mode,
		request: engine.SearchRequest{
			Mode:        mode,
			Query:       query,
			Season:      req.Season,
			Episode:     req.Episode,
			Year:        req.Year,
			IMDbID:      strings.TrimSpace(req.IMDbID),
			TMDbID:      strings.TrimSpace(req.TMDbID),
			TVDbID:      strings.TrimSpace(req.TVDbID),
			Categories:  req.Categories,
			Limit:       req.Limit,
			SkipDetails: true,
		},
	}
	if plan.request.Limit <= 0 {
		plan.request.Limit = 50
	}

	supportsMode := caps.Supports(mode)
	switch mode {
	case engine.ModeSearch:
		if !supportsMode {
			return livePlan{}, false, "search not supported"
		}
	case engine.ModeTV:
		if !supportsMode {
			if !caps.Supports(engine.ModeSearch) || query == "" {
				return livePlan{}, false, "tv-search not supported"
			}
			plan.mode = engine.ModeSearch
			plan.fallback = true
			plan.request.Mode = engine.ModeSearch
			plan.request.Query = appendEpisode(query, req.Season, req.Episode)
		}
	case engine.ModeMovie:
		if !supportsMode {
			if !caps.Supports(engine.ModeSearch) || query == "" {
				return livePlan{}, false, "movie-search not supported"
			}
			plan.mode = engine.ModeSearch
			plan.fallback = true
			plan.request.Mode = engine.ModeSearch
			plan.request.Query = appendYear(query, req.Year)
		}
	case engine.ModeMusic, engine.ModeBook:
		if !supportsMode {
			if !caps.Supports(engine.ModeSearch) || query == "" {
				return livePlan{}, false, string(mode) + " not supported"
			}
			plan.mode = engine.ModeSearch
			plan.fallback = true
			plan.request.Mode = engine.ModeSearch
		}
	}

	if plan.request.Query == "" {
		// Without a keyword only an ID the connector declares can answer.
		if !hasSupportedID(caps, plan.mode, plan.request) {
			return livePlan{}, false, "no query and no supported id"
		}
	}
	return plan, true, ""
}

// hasSupportedID reports whether the request has at least one identifier the
// effective mode accepts. It never fabricates a query.
func hasSupportedID(caps engine.Capabilities, mode engine.Mode, req engine.SearchRequest) bool {
	candidates := []struct {
		param string
		value string
	}{
		{engine.ParamIMDbID, req.IMDbID},
		{engine.ParamTMDbID, req.TMDbID},
		{engine.ParamTVDbID, req.TVDbID},
	}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate.value) != "" && caps.SupportsParam(mode, candidate.param) {
			return true
		}
	}
	return false
}

// appendEpisode adds the canonical SxxEyy (or Sxx) tag without duplicating a
// tag the user already typed.
func appendEpisode(query string, season, episode int) string {
	if season <= 0 {
		return query
	}
	if liveSeasonEpisode.MatchString(query) {
		return query
	}
	tag := fmt.Sprintf("S%02d", season)
	if episode > 0 {
		tag += fmt.Sprintf("E%02d", episode)
	}
	return strings.TrimSpace(query + " " + tag)
}

// appendYear appends the release year when it is known and not already part
// of the query.
func appendYear(query string, year int) string {
	if year <= 0 {
		return query
	}
	value := strconv.Itoa(year)
	for _, field := range strings.Fields(query) {
		if field == value {
			return query
		}
	}
	return strings.TrimSpace(query + " " + value)
}

func requestedMode(req LiveQuery) string {
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		return string(engine.ModeSearch)
	}
	return mode
}

func liveQueryPresent(req LiveQuery) bool {
	if strings.TrimSpace(req.Query) != "" {
		return true
	}
	return strings.TrimSpace(req.IMDbID) != "" || strings.TrimSpace(req.TMDbID) != "" ||
		strings.TrimSpace(req.TVDbID) != ""
}

// storeLiveItems writes the items of a live search into the cache. It never
// opens a details page and never downloads a payload: a magnet is built from
// the infohash in the listing, otherwise the item is kept behind a sealed
// download reference that the existing resolver opens at grab time.
func (m *Manager) storeLiveItems(ctx context.Context, indexer store.Indexer, items []engine.Item, matcher *store.BlocklistMatcher) (int, error) {
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

		downloadRef := ""
		if magnet == "" && hash == "" {
			if m.vault == nil {
				continue
			}
			if strings.TrimSpace(item.DownloadURL) == "" && strings.TrimSpace(item.DetailsURL) == "" {
				continue
			}
			ref, refErr := SealDownloadRef(m.vault, indexer.ID, item)
			if refErr != nil {
				m.logger.Warn("live search: seal download reference",
					"indexer", indexer.Slug, "error", refErr)
				continue
			}
			downloadRef = ref
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
			Protocol:    "torrent",
			FirstSeenAt: now,
			LastSeenAt:  now,
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
			DownloadRef: downloadRef,
		}); err != nil {
			if errors.Is(err, store.ErrIndexerGone) {
				m.logger.Debug("live search: indexer deleted during search; provenance discarded",
					"indexer", indexer.Slug)
				break
			}
			m.logger.Warn("live search: provenance", "indexer", indexer.Slug, "error", err)
		}
		stored++
	}
	return stored, nil
}
