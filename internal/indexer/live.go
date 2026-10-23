package indexer

import (
	"context"
	"fmt"
	"regexp"
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
	liveGlobalConcurrency = 8

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
func (m *Manager) SearchLive(ctx context.Context, req LiveQuery) (LiveResult, error) {
	var result LiveResult
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
	for len(pending) > 0 {
		select {
		case outcome := <-results:
			delete(pending, outcome.IndexerID)
			result.Indexers = append(result.Indexers, outcome)
			result.Stored += outcome.Results
		case <-ctx.Done():
			// Drain whatever completed in the same instant, then report the
			// rest as timed out. Workers continue under workCtx.
			for {
				select {
				case outcome := <-results:
					delete(pending, outcome.IndexerID)
					result.Indexers = append(result.Indexers, outcome)
					result.Stored += outcome.Results
				default:
					goto timedOut
				}
			}
		timedOut:
			for id, indexer := range pending {
				result.Indexers = append(result.Indexers, LiveIndexerResult{
					IndexerID: id,
					Name:      indexer.Name,
					Mode:      requestedMode(req),
					TimedOut:  true,
				})
			}
			return result, nil
		}
	}
	return result, nil
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
			m.logger.Warn("live search: provenance", "indexer", indexer.Slug, "error", err)
		}
		stored++
	}
	return stored, nil
}
