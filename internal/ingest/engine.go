// Package ingest runs the periodic acquisition: it asks each source for its
// items, normalises them and writes them to the SQLite cache.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/notify"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/textutil"
)

// ErrAlreadyRunning is returned when the same source is triggered twice.
var ErrAlreadyRunning = errors.New("source is already running")

var errRunCanceled = errors.New("run canceled")

var liveSeasonEpisode = regexp.MustCompile(`(?i)s\d{1,2}[ ._-]?e\d{1,3}`)

// SearchLive queries enabled Torznab sources on demand, stores what they return
// and reports how many items were new or updated. It is used by the Torznab
// endpoint when the local cache has no result; RSS/HTML sources are skipped
// because they cannot answer a query.
func (e *Engine) SearchLive(ctx context.Context, filter store.Filter) (int, error) {
	query := strings.TrimSpace(filter.Query)
	if query == "" {
		return 0, nil
	}
	if filter.Season > 0 && filter.Episode > 0 && !liveSeasonEpisode.MatchString(query) {
		query += fmt.Sprintf(" S%02dE%02d", filter.Season, filter.Episode)
	}
	settings, err := e.store.LoadSettings(ctx)
	if err != nil {
		return 0, err
	}
	sources, err := e.store.ListSources(ctx)
	if err != nil {
		return 0, err
	}
	rules, err := e.store.ListBlocklist(ctx)
	if err != nil {
		e.log.Warn("live search: cannot load blocklist", "component", "indexarr", "error", err)
	}
	matcher := store.NewBlocklistMatcher(rules)

	var (
		mu     sync.Mutex
		stored int
		wg     sync.WaitGroup
		sem    = make(chan struct{}, 3)
	)
	for _, source := range sources {
		if !source.Enabled || (source.Kind != "" && source.Kind != "torznab") {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(source store.Source) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			items, err := e.torznabClient(source, settings).Fetch(ctx, query, 50)
			if err != nil {
				e.log.Warn("live search source failed",
					sourceLogArgs(source, "error", err.Error())...)
				return
			}
			result := store.RunResult{}
			attempts := 0
			for _, item := range items {
				if ctx.Err() != nil {
					return
				}
				if err := e.saveItem(ctx, source, settings, item, &result, &attempts, matcher); err != nil {
					e.log.Warn("live search item skipped", sourceLogArgs(source,
						"title", textutil.Truncate(item.Title, 80), "error", err.Error())...)
				}
			}
			if count := int(result.New + result.Updated); count > 0 {
				mu.Lock()
				stored += count
				mu.Unlock()
			}
		}(source)
	}
	wg.Wait()
	if stored == 0 && ctx.Err() != nil {
		return 0, ctx.Err()
	}
	return stored, nil
}

// ErrQueueFull is returned when the worker queue is saturated.
var ErrQueueFull = errors.New("run queue is full, try again shortly")

// Engine owns the HTTP fetchers and the worker queue.
type Engine struct {
	store         *store.Store
	log           *slog.Logger
	http          *http.Client
	jobs          chan int64
	running       sync.Map
	cancels       sync.Map
	wg            sync.WaitGroup
	startOnce     sync.Once
	backupRunning atomic.Bool
	backupRetry   atomic.Int64
}

// New builds an engine. It does not start any goroutine until Start is called.
func New(st *store.Store, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{
		store: st,
		log:   logger,
		http: &http.Client{
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   10,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: time.Second,
			},
			// The per-run context is the real deadline; this is a safety net
			// against a transport that never returns.
			Timeout: 4 * time.Minute,
		},
		jobs: make(chan int64, 128),
	}
}

// IsRunning reports whether a source currently has a run in progress.
func (e *Engine) IsRunning(sourceID int64) bool {
	_, ok := e.running.Load(sourceID)
	return ok
}

// Cancel stops the run in progress for a source, if any.
func (e *Engine) Cancel(sourceID int64) bool {
	cancel, ok := e.cancels.Load(sourceID)
	if !ok {
		return false
	}
	cancel.(context.CancelFunc)()
	return true
}

// Trigger queues a manual run. It fails immediately when the source is
// already running or the queue is full.
func (e *Engine) Trigger(sourceID int64) error {
	if e.IsRunning(sourceID) {
		return ErrAlreadyRunning
	}
	select {
	case e.jobs <- sourceID:
		return nil
	default:
		return ErrQueueFull
	}
}

// TriggerAll queues every enabled source and returns how many were queued.
func (e *Engine) TriggerAll(ctx context.Context) (int, error) {
	sources, err := e.store.ListSources(ctx)
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		if err := e.Trigger(source.ID); err == nil {
			queued++
		}
	}
	return queued, nil
}

// TestAll tests every enabled source in the background and reports the count
// immediately. Each outcome is written to the application log, visible in the
// Logs tab; a full run can take minutes with many slow indexers.
func (e *Engine) TestAll(ctx context.Context) (int, error) {
	sources, err := e.store.ListSources(ctx)
	if err != nil {
		return 0, err
	}
	var enabled []store.Source
	for _, source := range sources {
		if source.Enabled {
			enabled = append(enabled, source)
		}
	}
	if len(enabled) == 0 {
		return 0, nil
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
	go func() {
		defer cancel()
		e.log.Info("testing sources", "component", "indexarr", "count", len(enabled))
		sem := make(chan struct{}, 3)
		var wg sync.WaitGroup
		for _, source := range enabled {
			wg.Add(1)
			sem <- struct{}{}
			go func(source store.Source) {
				defer wg.Done()
				defer func() { <-sem }()
				_, _ = e.TestSource(runCtx, source)
			}(source)
		}
		wg.Wait()
		e.log.Info("source test finished", "component", "indexarr", "count", len(enabled))
	}()
	return len(enabled), nil
}

// RunSource runs one source synchronously. The scheduler and the manual
// trigger both end up here, so one source can never run twice at once.
func (e *Engine) RunSource(ctx context.Context, sourceID int64) (*store.RunResult, error) {
	if _, busy := e.running.LoadOrStore(sourceID, struct{}{}); busy {
		return nil, ErrAlreadyRunning
	}
	defer e.running.Delete(sourceID)

	source, err := e.store.GetSource(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	settings, err := e.store.LoadSettings(ctx)
	if err != nil {
		return nil, err
	}
	timeout := settings.FetchTimeout()*3 + 30*time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	e.cancels.Store(sourceID, cancel)
	defer e.cancels.Delete(sourceID)
	runID, startedAt, err := e.store.BeginSourceRun(runCtx, sourceID)
	if err != nil {
		e.log.Error("cannot start source run", sourceLogArgs(source, "error", err.Error())...)
		return nil, err
	}
	result := store.RunResult{}
	runErr := e.ingest(runCtx, source, settings, &result)
	if errors.Is(runErr, context.Canceled) {
		runErr = errRunCanceled
	}
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(runCtx), 10*time.Second)
	defer finishCancel()
	if err := e.store.FinishSourceRun(finishCtx, runID, source, startedAt, result, runErr); err != nil {
		e.log.Error("cannot record run", sourceLogArgs(source, "error", err.Error())...)
	}
	if runErr != nil && !errors.Is(runErr, errRunCanceled) {
		e.log.Error("source run failed", sourceLogArgs(source, "error", runErr.Error())...)
	}
	e.afterRun(finishCtx, source, settings, result, runErr)
	return &result, runErr
}

// afterRun performs the post-run actions: notifications and auto-disabling a
// source after repeated failures.
func (e *Engine) afterRun(ctx context.Context, source store.Source, settings store.Settings, result store.RunResult, runErr error) {
	if runErr != nil {
		if errors.Is(runErr, errRunCanceled) {
			return
		}
		disabled := false
		if settings.AutoDisableAfter > 0 {
			if latest, err := e.store.GetSource(ctx, source.ID); err == nil &&
				latest.ConsecutiveErrors >= int64(settings.AutoDisableAfter) && latest.Enabled {
				if err := e.store.SetSourceEnabled(ctx, source.ID, false); err == nil {
					disabled = true
					e.log.Warn("source auto-disabled after repeated failures",
						sourceLogArgs(source, "errors", latest.ConsecutiveErrors)...)
				}
			}
		}
		if settings.NotifyFailures {
			kind := "run_failed"
			if disabled {
				kind = "source_disabled"
			}
			e.sendNotification(settings, notify.Event{
				Kind: kind, Source: source.Name, Error: runErr.Error(), Count: int(result.Seen),
			})
		}
		return
	}
	if result.New > 0 && settings.NotifyNewReleases {
		e.sendNotification(settings, notify.Event{
			Kind: "new_releases", Source: source.Name, Count: int(result.New), Titles: result.NewTitles,
		})
	}
}

func (e *Engine) sendNotification(settings store.Settings, event notify.Event) {
	cfg := notify.Config{
		WebhookURL:     settings.NotifyWebhookURL,
		TelegramToken:  settings.NotifyTelegramToken,
		TelegramChatID: settings.NotifyTelegramChatID,
	}
	if !notify.Configured(cfg) {
		return
	}
	event.At = time.Now().UTC()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := notify.Send(ctx, cfg, event); err != nil {
			e.log.Warn("notification failed", "component", "notify", "kind", event.Kind, "error", err)
		}
	}()
}

func (e *Engine) torznabClient(source store.Source, settings store.Settings) *feed.Client {
	return feed.NewClient(feed.ClientConfig{
		Name:         source.Name,
		URL:          source.URL,
		Manager:      source.Manager,
		APIKey:       source.APIKey,
		Username:     source.Username,
		Password:     source.Password,
		FlareSolverr: settings.FlareSolverrURL,
		Categories:   source.CategoryIDs(),
		UserAgent:    settings.UserAgent,
		Timeout:      settings.FetchTimeout(),
		HTTPClient:   e.http,
	})
}

// sourceLogArgs gives every source-related record the same fields, so the Logs
// page can filter by origin and by source.
func sourceLogArgs(source store.Source, extra ...any) []any {
	component := "indexer"
	if source.Kind == "rss" {
		component = "rss"
	}
	args := []any{"component", component, "source", source.Name, "kind", source.Kind}
	if host := sourceHost(source.URL); host != "" {
		args = append(args, "host", host)
	}
	return append(args, extra...)
}

func sourceHost(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func (e *Engine) fetchItems(ctx context.Context, source store.Source, settings store.Settings) ([]feed.Item, error) {
	switch source.Kind {
	case "", "torznab":
		return e.torznabClient(source, settings).Fetch(ctx, source.Query, 100)
	case "rss":
		return feed.FetchRSS(ctx, e.http, source.URL, feed.RequestOptions{
			UserAgent:    settings.UserAgent,
			Username:     source.Username,
			Password:     source.Password,
			FlareSolverr: settings.FlareSolverrURL,
		})
	default:
		return nil, fmt.Errorf("unknown source kind %q", source.Kind)
	}
}

func (e *Engine) ingest(ctx context.Context, source store.Source, settings store.Settings, result *store.RunResult) error {
	items, err := e.fetchItems(ctx, source, settings)
	if err != nil {
		return err
	}
	result.Seen = int64(len(items))
	rules, err := e.store.ListBlocklist(ctx)
	if err != nil {
		e.log.Warn("cannot load blocklist", "component", "indexarr", "error", err)
	}
	matcher := store.NewBlocklistMatcher(rules)
	attempts := 0
	for _, item := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := e.saveItem(ctx, source, settings, item, result, &attempts, matcher); err != nil {
			e.log.Warn("item skipped", sourceLogArgs(source,
				"title", textutil.Truncate(item.Title, 80), "error", err.Error())...)
		}
	}
	return nil
}

func (e *Engine) saveItem(ctx context.Context, source store.Source, settings store.Settings, item feed.Item, result *store.RunResult, attempts *int, matcher *store.BlocklistMatcher) error {
	title := strings.TrimSpace(item.Title)
	if title == "" {
		return nil
	}
	if strings.TrimSpace(item.Category) == "" && source.DefaultCategory != "" {
		item.Category = source.DefaultCategory
	}
	hash := feed.NormalizeInfoHash(item.InfoHash)
	magnet := strings.TrimSpace(item.Magnet)
	if hash == "" {
		hash = feed.MagnetHash(magnet)
	}
	if magnet == "" && hash != "" {
		magnet = feed.MagnetFromHash(hash, title)
	}
	if rule, blocked := matcher.Match(hash, title, source.Name, item.Category); blocked {
		result.Blocked++
		e.log.Debug("release blocked", "source", source.Name,
			"title", textutil.Truncate(title, 80), "kind", rule.Kind, "value", rule.Value)
		return nil
	}
	// The identity is fixed before the .torrent download: an item that exposes
	// only a link keeps its GUID/URL key across runs, so the stored file can be
	// reused instead of downloaded again. The infohash, once known, is what
	// merges the same release coming from another source.
	provisionalKey := feed.DedupKey(item, source.Name)
	var torrent []byte
	if magnet == "" && settings.FetchTorrents && item.TorrentURL != "" &&
		*attempts < settings.MaxTorrentsPerRun {
		existing, found, lookupErr := e.store.ReleaseByKey(ctx, provisionalKey)
		if lookupErr == nil && found && len(existing.Torrent) > 0 {
			// Already downloaded in a previous run: reuse it instead of
			// asking the indexer for the same file again.
			torrent = existing.Torrent
			hash = existing.InfoHash
		} else {
			*attempts = *attempts + 1
			blob, fetchedHash, err := feed.FetchTorrent(ctx, e.http, item.TorrentURL, feed.TorrentRequest{
				APIKey:    source.APIKey,
				UserAgent: settings.UserAgent,
				Username:  source.Username,
				Password:  source.Password,
				MaxBytes:  settings.MaxTorrentBytes,
			})
			if err != nil {
				e.log.Warn("torrent download failed", sourceLogArgs(source,
					"title", textutil.Truncate(title, 80), "error", err.Error())...)
			} else {
				torrent = blob
				hash = fetchedHash
				result.Torrents++
			}
		}
	}
	if magnet == "" && hash != "" {
		// The .torrent gave the release its identity: expose it as a magnet
		// too, so clients can choose the cheaper of the two links.
		magnet = feed.MagnetFromHash(hash, title)
	}
	if magnet == "" && hash == "" && len(torrent) == 0 {
		// A link to a web page is not a release: storing it would only add a
		// row no client can download.
		return nil
	}
	item.InfoHash = hash
	item.Magnet = magnet
	now := time.Now()
	release := store.Release{
		DedupKey:         provisionalKey,
		InfoHash:         hash,
		IMDbID:           item.IMDbID,
		TMDbID:           item.TMDbID,
		TVDbID:           item.TVDbID,
		Title:            title,
		SearchTitle:      textutil.NormalizeTitle(title),
		Size:             item.Size,
		Seeders:          item.Seeders,
		Leechers:         item.Leechers,
		Category:         item.Category,
		PublishedAt:      item.Published,
		SourceID:         source.ID,
		SourceName:       source.Name,
		GUID:             item.GUID,
		Magnet:           magnet,
		Torrent:          torrent,
		TorrentFetchedAt: time.Time{},
		FirstSeenAt:      now,
		LastSeenAt:       now,
	}
	if len(torrent) > 0 {
		release.TorrentFetchedAt = now
	}
	isNew, err := e.store.UpsertRelease(ctx, &release)
	if err != nil {
		return err
	}
	if isNew {
		result.New++
		if len(result.NewTitles) < 5 {
			result.NewTitles = append(result.NewTitles, title)
		}
	} else {
		result.Updated++
	}
	return nil
}

// TestSource probes a source without writing anything. It is used by the
// "Test" button in the web interface.
func (e *Engine) TestSource(ctx context.Context, source store.Source) (string, error) {
	settings, err := e.store.LoadSettings(ctx)
	if err != nil {
		return "", err
	}
	testCtx, cancel := context.WithTimeout(ctx, settings.FetchTimeout()*2+10*time.Second)
	defer cancel()
	var message string
	var testErr error
	if source.Kind == "rss" {
		message, testErr = feed.TestRSS(testCtx, e.http, source.URL, feed.RequestOptions{
			UserAgent:    settings.UserAgent,
			Username:     source.Username,
			Password:     source.Password,
			FlareSolverr: settings.FlareSolverrURL,
		})
	} else {
		message, testErr = e.torznabClient(source, settings).Test(testCtx)
	}
	if testErr != nil {
		e.log.Warn("source test failed", sourceLogArgs(source, "error", testErr.Error())...)
	} else {
		e.log.Info("source test ok", sourceLogArgs(source, "result", message)...)
	}
	return message, testErr
}
