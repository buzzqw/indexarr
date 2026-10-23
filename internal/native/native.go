// Package native schedules periodic runs of every native indexer.
//
// Every configured indexer is native now: a Jackett or Prowlarr site is an
// ordinary Torznab indexer pointing at the manager's per-indexer endpoint. The
// runner owns the periodic pass, automatic backups, retention, notifications
// and the auto-disable policy.
package native

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buzzqw/indexarr/internal/backup"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/notify"
	"github.com/buzzqw/indexarr/internal/store"
)

const (
	// defaultWorkers is the number of indexers run concurrently when Config
	// does not set one.
	defaultWorkers = 2

	// minInterval is the shortest scheduler period. A misconfigured interval
	// must not hammer trackers.
	minInterval = time.Minute

	// component is attached to every log record.
	component = "native"
)

// Skip reasons reported in RunOutcome.Reason.
const (
	ReasonDisabled = "disabled"
)

var (
	// ErrStoreRequired is returned by New without a store.
	ErrStoreRequired = errors.New("native: store is required")

	// ErrManagerRequired is returned by New without an indexer manager.
	ErrManagerRequired = errors.New("native: manager is required")

	// ErrTestAllRunning is returned when a background test pass is active.
	ErrTestAllRunning = errors.New("native: test pass already running")
)

// Config configures a Runner. Store and Manager are required; every other
// field has a usable default.
type Config struct {
	Store    *store.Store
	Manager  *indexer.Manager
	Logger   *slog.Logger
	Interval time.Duration // 0 => settings.DefaultInterval(), at least 60s
	Workers  int           // 0 => 2
	OnResult func(RunOutcome)
}

// RunOutcome is the result of considering one indexer.
type RunOutcome struct {
	IndexerID int64
	Slug      string
	Name      string
	Skipped   bool // disabled
	Reason    string
	Stats     *indexer.RunStats
	Err       error
	Duration  time.Duration
}

// Runner periodically runs every native indexer. It is safe to call RunOnce
// and RunIndexer while Start is running.
type Runner struct {
	store    *store.Store
	manager  *indexer.Manager
	logger   *slog.Logger
	interval time.Duration
	workers  int
	onResult func(RunOutcome)

	startOnce     sync.Once
	wg            sync.WaitGroup
	backupRunning atomic.Bool
	backupRetry   atomic.Int64
	testingAll    atomic.Bool
}

// New builds a Runner.
func New(cfg Config) (*Runner, error) {
	if cfg.Store == nil {
		return nil, ErrStoreRequired
	}
	if cfg.Manager == nil {
		return nil, ErrManagerRequired
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger = logger.With("component", component)

	workers := cfg.Workers
	if workers <= 0 {
		workers = defaultWorkers
	}

	return &Runner{
		store:    cfg.Store,
		manager:  cfg.Manager,
		logger:   logger,
		interval: resolveInterval(cfg.Store, cfg.Interval, logger),
		workers:  workers,
		onResult: cfg.OnResult,
	}, nil
}

// resolveInterval applies the documented defaults and the 60 second floor.
func resolveInterval(st *store.Store, configured time.Duration, logger *slog.Logger) time.Duration {
	interval := configured
	if interval <= 0 {
		settings, err := st.LoadSettings(context.Background())
		if err != nil {
			logger.Warn("native: load settings, using the default interval", "error", err)
			interval = store.DefaultSettings().DefaultInterval()
		} else {
			interval = settings.DefaultInterval()
		}
	}
	if interval < minInterval {
		interval = minInterval
	}
	return interval
}

// Start runs one pass immediately and then once per interval. It returns
// immediately; the background goroutine stops when ctx is canceled. Calling
// Start more than once is a no-op; use Wait to join the goroutine.
func (r *Runner) Start(ctx context.Context) {
	ctx = nonNilContext(ctx)
	r.startOnce.Do(func() {
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.loop(ctx)
		}()
	})
}

// Wait blocks until the goroutine started by Start has stopped. It returns
// immediately when Start was never called.
func (r *Runner) Wait() { r.wg.Wait() }

func (r *Runner) loop(ctx context.Context) {
	r.logger.Info("native: scheduler started",
		"interval", r.interval.String(), "workers", r.workers)

	r.runPass(ctx)
	lastMaintenance := time.Now()
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("native: scheduler stopped")
			return
		case <-ticker.C:
			r.runPass(ctx)
			r.maybeBackup(ctx)
			if time.Since(lastMaintenance) >= 6*time.Hour {
				lastMaintenance = time.Now()
				r.maintenance(ctx)
			}
		}
	}
}

// runPass executes one full pass and logs a compact summary. A canceled
// context stops the pass without logging a misleading summary.
func (r *Runner) runPass(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	outcomes := r.RunOnce(ctx)
	if ctx.Err() != nil {
		return
	}
	r.logSummary(outcomes)
}

func (r *Runner) logSummary(outcomes []RunOutcome) {
	if len(outcomes) == 0 {
		r.logger.Debug("native: run complete", "indexers", 0)
		return
	}
	var ran, skipped, failed, seen, newCount, updated int
	for _, outcome := range outcomes {
		switch {
		case outcome.Err != nil:
			failed++
		case outcome.Skipped:
			skipped++
		default:
			ran++
		}
		if outcome.Stats != nil {
			seen += outcome.Stats.Seen
			newCount += outcome.Stats.New
			updated += outcome.Stats.Updated
		}
	}
	r.logger.Info("native: run complete",
		"indexers", len(outcomes),
		"ran", ran,
		"skipped", skipped,
		"failed", failed,
		"seen", seen,
		"new", newCount,
		"updated", updated)
}

// RunOnce runs every enabled indexer once, with at most Workers runs in
// flight. Disabled indexers are reported as skipped. The returned outcomes
// always follow ascending indexer ID, regardless of the completion order.
func (r *Runner) RunOnce(ctx context.Context) []RunOutcome {
	ctx = nonNilContext(ctx)
	if ctx.Err() != nil {
		return nil
	}
	indexers, err := r.store.ListIndexers(ctx)
	if err != nil {
		r.logger.Warn("native: list indexers", "error", err)
		return nil
	}
	candidates := sortCandidates(indexers)
	if len(candidates) == 0 {
		return nil
	}

	outcomes := make([]RunOutcome, len(candidates))
	done := make([]bool, len(candidates))

	workers := r.workers
	if workers > len(candidates) {
		workers = len(candidates)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case index, ok := <-jobs:
					if !ok {
						return
					}
					outcomes[index] = r.runOne(ctx, candidates[index])
					done[index] = true
				}
			}
		}()
	}

dispatch:
	for i := range candidates {
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- i:
		}
	}
	close(jobs)
	wg.Wait()

	// On cancellation some candidates may not have been started. They are
	// omitted rather than reported as failures, so a shutdown does not look
	// like a burst of indexer errors.
	result := make([]RunOutcome, 0, len(candidates))
	for i := range outcomes {
		if done[i] {
			result = append(result, outcomes[i])
		}
	}
	return result
}

// RunAll starts a full pass in the background and returns how many enabled
// indexers will run. It is the "Run all" button of the indexer page.
func (r *Runner) RunAll(ctx context.Context) (int, error) {
	indexers, err := r.store.ListIndexers(ctx)
	if err != nil {
		return 0, err
	}
	enabled := 0
	for _, indexerRow := range indexers {
		if indexerRow.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return 0, nil
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Minute)
	go func() {
		defer cancel()
		r.runPass(runCtx)
	}()
	return enabled, nil
}

// TestAll probes every enabled indexer in the background with at most three
// probes in flight and reports the count immediately. Outcomes go to the
// application log, which is visible in the Logs tab.
func (r *Runner) TestAll(ctx context.Context) (int, error) {
	if !r.testingAll.CompareAndSwap(false, true) {
		return 0, ErrTestAllRunning
	}
	indexers, err := r.store.ListIndexers(ctx)
	if err != nil {
		r.testingAll.Store(false)
		return 0, err
	}
	var enabled []store.Indexer
	for _, indexerRow := range indexers {
		if indexerRow.Enabled {
			enabled = append(enabled, indexerRow)
		}
	}
	if len(enabled) == 0 {
		r.testingAll.Store(false)
		return 0, nil
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
	go func() {
		defer r.testingAll.Store(false)
		defer cancel()
		r.logger.Info("native: testing indexers", "count", len(enabled))
		sem := make(chan struct{}, 3)
		var wg sync.WaitGroup
		for _, indexerRow := range enabled {
			wg.Add(1)
			sem <- struct{}{}
			go func(indexerRow store.Indexer) {
				defer wg.Done()
				defer func() { <-sem }()
				if _, err := r.manager.Test(runCtx, indexerRow.ID); err != nil {
					r.logger.Warn("native: indexer test failed",
						"indexer", indexerRow.Slug, "error", r.managerRedacted(indexerRow.ID, err))
					return
				}
				r.logger.Info("native: indexer test ok", "indexer", indexerRow.Slug)
			}(indexerRow)
		}
		wg.Wait()
		r.logger.Info("native: test pass finished", "count", len(enabled))
	}()
	return len(enabled), nil
}

// RunIndexer runs one indexer, or explains why it was skipped. Unknown ids
// return an error; a disabled indexer returns a skipped outcome with a nil
// error. Run failures are returned both in the outcome and as the error.
func (r *Runner) RunIndexer(ctx context.Context, id int64) (RunOutcome, error) {
	ctx = nonNilContext(ctx)
	if id <= 0 {
		return RunOutcome{IndexerID: id}, errors.New("native: invalid indexer id")
	}
	indexerRow, err := r.store.GetIndexer(ctx, id)
	if err != nil {
		return RunOutcome{IndexerID: id}, err
	}
	outcome := r.runOne(ctx, indexerRow)
	return outcome, outcome.Err
}

// runOne executes one indexer. It always reports the outcome through OnResult
// and emits a log line that contains no tracker URL and no secret.
func (r *Runner) runOne(ctx context.Context, indexerRow store.Indexer) (outcome RunOutcome) {
	started := time.Now()
	outcome = RunOutcome{
		IndexerID: indexerRow.ID,
		Slug:      indexerRow.Slug,
		Name:      indexerRow.Name,
	}
	defer func() {
		outcome.Duration = time.Since(started)
		if r.onResult != nil {
			r.onResult(outcome)
		}
		r.logOutcome(outcome)
	}()

	if !indexerRow.Enabled {
		outcome.Skipped = true
		outcome.Reason = ReasonDisabled
		return outcome
	}

	stats, runErr := r.manager.Run(ctx, indexerRow.ID)
	if runErr != nil {
		outcome.Err = runErr
	} else {
		outcome.Stats = stats
	}
	r.afterRun(ctx, indexerRow, outcome)
	return outcome
}

// afterRun applies the notification and auto-disable policies after one run.
func (r *Runner) afterRun(ctx context.Context, indexerRow store.Indexer, outcome RunOutcome) {
	settings, err := r.store.LoadSettings(ctx)
	if err != nil {
		return
	}
	if outcome.Err != nil {
		disabled := false
		if settings.AutoDisableAfter > 0 {
			statuses, err := r.store.RecentIndexerRunStatuses(ctx, indexerRow.ID, settings.AutoDisableAfter)
			if err == nil && len(statuses) >= settings.AutoDisableAfter && allErrors(statuses) && indexerRow.Enabled {
				indexerRow.Enabled = false
				if err := r.store.UpdateIndexer(ctx, indexerRow); err == nil {
					disabled = true
					r.logger.Warn("native: indexer auto-disabled after repeated failures",
						"indexer", indexerRow.Slug, "errors", settings.AutoDisableAfter)
				}
			}
		}
		if settings.NotifyFailures {
			kind := "run_failed"
			if disabled {
				kind = "indexer_disabled"
			}
			r.sendNotification(settings, notify.Event{
				Kind: kind, Source: indexerRow.Name, Error: r.managerRedacted(indexerRow.ID, outcome.Err),
			})
		}
		return
	}
	if outcome.Stats != nil && outcome.Stats.New > 0 && settings.NotifyNewReleases {
		var titles []string
		for _, item := range outcome.Stats.Items {
			if len(titles) >= 5 {
				break
			}
			if item.Title != "" {
				titles = append(titles, item.Title)
			}
		}
		r.sendNotification(settings, notify.Event{
			Kind: "new_releases", Source: indexerRow.Name, Count: outcome.Stats.New, Titles: titles,
		})
	}
}

func allErrors(statuses []string) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, status := range statuses {
		if status != "error" {
			return false
		}
	}
	return true
}

func (r *Runner) sendNotification(settings store.Settings, event notify.Event) {
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
			r.logger.Warn("notification failed", "kind", event.Kind, "error", err)
		}
	}()
}

// managerRedacted exposes the manager's redaction without leaking the raw
// indexer error into the logs.
func (r *Runner) managerRedacted(indexerID int64, err error) string {
	if err == nil {
		return ""
	}
	return r.manager.RedactError(indexerID, err)
}

// maybeBackup creates an automatic backup when the configured interval has
// elapsed. It returns immediately; the SQLite VACUUM INTO runs in the
// background and a second backup cannot start while one is in progress.
func (r *Runner) maybeBackup(ctx context.Context) {
	settings, err := r.store.LoadSettings(ctx)
	if err != nil || !settings.BackupEnabled {
		return
	}
	interval := time.Duration(settings.BackupIntervalHours) * time.Hour
	if interval < 6*time.Hour {
		interval = 6 * time.Hour
	}
	if latest := backup.LatestAutomatic(r.store.Path()); !latest.IsZero() && time.Since(latest) < interval {
		return
	}
	if time.Now().Unix() < r.backupRetry.Load() {
		return
	}
	if !r.backupRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer r.backupRunning.Store(false)
		backupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		info, err := backup.Create(backupCtx, r.store, r.store.Path())
		if err != nil {
			r.backupRetry.Store(time.Now().Add(15 * time.Minute).Unix())
			r.logger.Warn("automatic backup failed", "subsystem", "backup", "error", err)
			return
		}
		r.backupRetry.Store(0)
		r.logger.Info("automatic backup created", "file", info.Name, "bytes", info.Size)
		if removed, err := backup.Prune(r.store.Path(), settings.BackupKeep); err != nil {
			r.logger.Warn("backup rotation failed", "subsystem", "backup", "error", err)
		} else if removed > 0 {
			r.logger.Info("old backups removed", "count", removed)
		}
	}()
}

// maintenance prunes old releases and run history, then refreshes SQLite.
func (r *Runner) maintenance(ctx context.Context) {
	settings, err := r.store.LoadSettings(ctx)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if settings.RetentionDays > 0 {
		before := time.Now().AddDate(0, 0, -settings.RetentionDays)
		if removed, err := r.store.PruneReleases(ctx, before); err != nil {
			r.logger.Warn("retention prune failed", "subsystem", "maintenance", "error", err)
		} else if removed > 0 {
			r.logger.Info("retention prune", "removed", removed, "days", settings.RetentionDays)
		}
	}
	if removed, err := r.store.ClearRunHistory(ctx, time.Now().AddDate(0, 0, -90)); err != nil {
		r.logger.Warn("run history cleanup failed", "subsystem", "maintenance", "error", err)
	} else if removed > 0 {
		r.logger.Info("run history cleanup", "removed", removed)
	}
	if err := r.store.Optimize(ctx); err != nil {
		r.logger.Warn("database optimize failed", "subsystem", "maintenance", "error", err)
	}
}

func (r *Runner) logOutcome(outcome RunOutcome) {
	switch {
	case outcome.Err != nil:
		// The error text can embed tracker hostnames, so it stays in the
		// outcome/history and is never logged here.
		r.logger.Warn("native: indexer run failed",
			"indexer", outcome.Slug,
			"indexer_id", outcome.IndexerID,
			"duration_ms", outcome.Duration.Milliseconds())
	case outcome.Skipped:
		r.logger.Debug("native: indexer skipped",
			"indexer", outcome.Slug,
			"indexer_id", outcome.IndexerID,
			"reason", outcome.Reason)
	case outcome.Stats != nil:
		r.logger.Debug("native: indexer run complete",
			"indexer", outcome.Slug,
			"indexer_id", outcome.IndexerID,
			"seen", outcome.Stats.Seen,
			"new", outcome.Stats.New,
			"updated", outcome.Stats.Updated,
			"duration_ms", outcome.Duration.Milliseconds())
	}
}

// sortCandidates deduplicates by indexer id and sorts ascending, which fixes
// the outcome order independently of the store's name ordering and of the
// completion order.
func sortCandidates(indexers []store.Indexer) []store.Indexer {
	seen := make(map[int64]struct{}, len(indexers))
	out := make([]store.Indexer, 0, len(indexers))
	for _, indexerRow := range indexers {
		if _, ok := seen[indexerRow.ID]; ok {
			continue
		}
		seen[indexerRow.ID] = struct{}{}
		out = append(out, indexerRow)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}
