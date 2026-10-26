// Package native schedules periodic runs of every native indexer.
//
// Every configured indexer is native now: a Jackett or Prowlarr site is an
// ordinary Torznab indexer pointing at the manager's per-indexer endpoint. The
// runner owns the periodic pass, automatic backups, retention, notifications
// and the auto-disable policy.
package native

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buzzqw/indexarr/internal/backup"
	"github.com/buzzqw/indexarr/internal/engine"
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
	ReasonDisabled   = "disabled"
	ReasonSearchOnly = "search_only"
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
	// Notifier applies the notification switches and the anti-spam window.
	// When nil, New builds one on the same store.
	Notifier *notify.Dispatcher
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
	notifier *notify.Dispatcher

	startOnce     sync.Once
	wg            sync.WaitGroup
	backupRunning atomic.Bool
	backupRetry   atomic.Int64
	testingAll    atomic.Bool
	lastSeeders   time.Time
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
	notifier := cfg.Notifier
	if notifier == nil {
		notifier = notify.NewDispatcher(cfg.Store, logger)
	}

	return &Runner{
		store:    cfg.Store,
		manager:  cfg.Manager,
		logger:   logger,
		interval: resolveInterval(cfg.Store, cfg.Interval, logger),
		workers:  workers,
		onResult: cfg.OnResult,
		notifier: notifier,
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
	// The tick is one minute because it is also the shortest interval a user
	// or a definition may request. The global interval still governs indexers
	// without an override: the tick only decides when due work is checked, not
	// how often each indexer runs.
	tick := minInterval
	r.logger.Info("native: scheduler started",
		"interval", r.interval.String(), "tick", tick.String(), "workers", r.workers)

	r.runDuePass(ctx)
	lastMaintenance := time.Now()
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("native: scheduler stopped")
			return
		case <-ticker.C:
			r.runDuePass(ctx)
			r.maybeBackup(ctx)
			if settings, err := r.store.LoadSettings(ctx); err == nil &&
				time.Since(r.lastSeeders) >= settings.SeedersTTL() {
				r.lastSeeders = time.Now()
				r.refreshSeeders(ctx, settings.SeedersTTL())
			}
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

// runDuePass runs only the indexers whose own interval has elapsed. Manual
// actions (RunOnce, RunAll, RunIndexer) deliberately bypass this filter.
func (r *Runner) runDuePass(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	indexers, err := r.store.ListIndexers(ctx)
	if err != nil {
		r.logger.Warn("native: list indexers", "error", err)
		return
	}
	indexers = r.latestIndexers(ctx, indexers)
	settings, err := r.store.LoadSettings(ctx)
	if err != nil {
		r.logger.Warn("native: load settings, using the default interval", "error", err)
		settings = store.DefaultSettings()
	}
	lastRuns, err := r.store.RunRetryStates(ctx)
	if err != nil {
		r.logger.Warn("native: load retry states", "error", err)
		lastRuns = map[int64]store.RunRetryState{}
	}
	candidates := dueIndexers(indexers, lastRuns, settings.DefaultInterval(), time.Now())
	outcomes := r.runCandidates(ctx, sortCandidates(candidates))
	if ctx.Err() != nil {
		return
	}
	r.logSummary(outcomes)
}

// latestIndexers drops the search-only sites from a periodic pass. They have
// no list of new releases: the live Torznab fan-out queries them when an app
// searches. A definition that cannot be parsed stays in the list so its error
// remains visible instead of silently disappearing from the scheduler.
func (r *Runner) latestIndexers(ctx context.Context, indexers []store.Indexer) []store.Indexer {
	out := make([]store.Indexer, 0, len(indexers))
	for _, indexerRow := range indexers {
		latest, err := r.manager.LatestSupported(ctx, indexerRow)
		switch {
		case err != nil:
			r.logger.Warn("native: latest capability",
				"indexer", indexerRow.Slug, "error", r.managerRedacted(indexerRow.ID, err))
			out = append(out, indexerRow)
		case latest:
			out = append(out, indexerRow)
		}
	}
	return out
}

// retryDelays is the escalating schedule for a source that keeps failing.
// After the 96 hour step is used and the run fails again, the scheduler stops
// retrying: the source waits for the operator to fix it and press Test.
var retryDelays = []time.Duration{
	time.Hour, 2 * time.Hour, 3 * time.Hour, 6 * time.Hour,
	12 * time.Hour, 24 * time.Hour, 48 * time.Hour, 96 * time.Hour,
}

// RetryDelay returns the wait before the next attempt after the given number
// of consecutive failures. The boolean is false when the schedule is over.
func RetryDelay(failures int) (time.Duration, bool) {
	if failures <= 0 || failures > len(retryDelays) {
		return 0, false
	}
	return retryDelays[failures-1], true
}

// RetryExhausted reports whether the 96 hour step already failed, so the
// scheduler waits for the operator instead of retrying again.
func RetryExhausted(failures int) bool {
	return failures > len(retryDelays)
}

// dueIndexers filters the enabled indexers whose next run is due. A positive
// IntervalSeconds overrides the global interval; zero uses the global one. A
// source with consecutive failed runs follows retryDelays instead of its
// normal cadence; after the last step it is left alone until an edit or a
// successful test resets the streak.
func dueIndexers(indexers []store.Indexer, states map[int64]store.RunRetryState, global time.Duration, now time.Time) []store.Indexer {
	if global < minInterval {
		global = minInterval
	}
	out := make([]store.Indexer, 0, len(indexers))
	for _, indexerRow := range indexers {
		if !indexerRow.Enabled {
			continue
		}
		interval := global
		if indexerRow.IntervalSeconds > 0 {
			interval = time.Duration(indexerRow.IntervalSeconds) * time.Second
			if interval < minInterval {
				interval = minInterval
			}
		}
		state := states[indexerRow.ID]
		failures := state.Failures
		// An edit after the last failure is the operator saying "I fixed it":
		// resume the normal interval instead of the frozen backoff.
		if failures > 0 && indexerRow.UpdatedAt.After(state.LastRun) {
			failures = 0
		}
		if failures > len(retryDelays) {
			// The 96 hour step already failed: wait for the user. Manual
			// RunOnce/RunAll still work, and Test resets the streak.
			continue
		}
		due := interval
		if failures > 0 {
			due = retryDelays[failures-1]
		}
		if storedSourceConfig(indexerRow.ConfigJSON) && failures == 0 {
			// Stored sources get a small deterministic stagger so several
			// sites never fire at the same minute; a retry is not staggered.
			due += time.Duration(storedJitter(indexerRow.ID)) * time.Second
		}
		if last := state.LastRun; !last.IsZero() && now.Before(last.Add(due)) {
			continue
		}
		out = append(out, indexerRow)
	}
	return out
}

// storedSourceConfig reads the memorise flag from config_json. The native
// package only needs the boolean; the indexer package owns the full config.
func storedSourceConfig(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return false
	}
	value := strings.TrimSpace(values["store_latest"])
	return strings.EqualFold(value, "true") || value == "1"
}

// storedJitter returns 0..299 seconds, stable per indexer so a restart does not
// reshuffle the schedule.
func storedJitter(id int64) int64 {
	if id <= 0 {
		return 0
	}
	return (id*97 + 13) % 300
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
	return r.runCandidates(ctx, sortCandidates(r.latestIndexers(ctx, indexers)))
}

// runCandidates runs an explicit candidate list and preserves the ascending
// indexer ID order in the returned outcomes.
func (r *Runner) runCandidates(ctx context.Context, candidates []store.Indexer) []RunOutcome {
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
	// New releases are grouped per pass: one message for the whole run, not
	// one per indexer.
	r.notifyNewReleases(ctx, result)
	return result
}

// RunAll starts a full pass in the background and returns how many enabled
// indexers will run. It is the "Run all" button of the indexer page.
func (r *Runner) RunAll(ctx context.Context) (int, error) {
	indexers, err := r.store.ListIndexers(ctx)
	if err != nil {
		return 0, err
	}
	indexers = r.latestIndexers(ctx, indexers)
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
	return r.startTests(ctx, enabled)
}

// TestMany probes only the given indexers. The overview uses it for the
// "test all problems" button: the operator fixes a few sources and checks
// exactly those, without touching healthy sites.
func (r *Runner) TestMany(ctx context.Context, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if !r.testingAll.CompareAndSwap(false, true) {
		return 0, ErrTestAllRunning
	}
	indexers, err := r.store.ListIndexers(ctx)
	if err != nil {
		r.testingAll.Store(false)
		return 0, err
	}
	wanted := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	selected := make([]store.Indexer, 0, len(ids))
	for _, indexerRow := range indexers {
		if !indexerRow.Enabled {
			continue
		}
		if _, ok := wanted[indexerRow.ID]; ok {
			selected = append(selected, indexerRow)
		}
	}
	return r.startTests(ctx, selected)
}

// startTests starts the background probes and reports how many were queued.
// The caller must have set testingAll.
func (r *Runner) startTests(ctx context.Context, rows []store.Indexer) (int, error) {
	if len(rows) == 0 {
		r.testingAll.Store(false)
		return 0, nil
	}
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
	go func() {
		defer r.testingAll.Store(false)
		defer cancel()
		r.logger.Info("native: testing indexers", "count", len(rows))
		sem := make(chan struct{}, 3)
		var wg sync.WaitGroup
		for _, indexerRow := range rows {
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
		r.logger.Info("native: test pass finished", "count", len(rows))
	}()
	return len(rows), nil
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
	r.notifyNewReleases(ctx, []RunOutcome{outcome})
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
	if latest, err := r.manager.LatestSupported(ctx, indexerRow); err == nil && !latest {
		outcome.Skipped = true
		outcome.Reason = ReasonSearchOnly
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

// afterRun applies the auto-disable policy and the per-indexer state
// notification after one run. A message is sent only when the condition
// changes (ok -> error, error -> blocked, error -> ok, ...), never on every
// failing run: that is the anti-spam rule of the state event.
func (r *Runner) afterRun(ctx context.Context, indexerRow store.Indexer, outcome RunOutcome) {
	settings, err := r.store.LoadSettings(ctx)
	if err != nil {
		return
	}
	status := notify.StateOK
	switch {
	case outcome.Skipped:
		// A disabled or search-only indexer says nothing about the site.
		return
	case outcome.Err != nil:
		status = notify.StateError
		if errors.Is(outcome.Err, engine.ErrChallenge) {
			status = notify.StateBlocked
		}
		if settings.AutoDisableAfter > 0 {
			statuses, err := r.store.RecentIndexerRunStatuses(ctx, indexerRow.ID, settings.AutoDisableAfter)
			if err == nil && len(statuses) >= settings.AutoDisableAfter && allErrors(statuses) && indexerRow.Enabled {
				indexerRow.Enabled = false
				if err := r.store.UpdateIndexer(ctx, indexerRow); err == nil {
					status = notify.StateDisabled
					r.logger.Warn("native: indexer auto-disabled after repeated failures",
						"indexer", indexerRow.Slug, "errors", settings.AutoDisableAfter)
				}
			}
		}
	}
	previous, err := r.store.IndexerNotifyState(ctx, indexerRow.ID)
	if err != nil {
		r.logger.Warn("native: read the notified state", "indexer", indexerRow.Slug, "error", err)
		previous = ""
	}
	if previous == status {
		return
	}
	if err := r.store.SetIndexerNotifyState(ctx, indexerRow.ID, status); err != nil {
		r.logger.Warn("native: store the notified state", "indexer", indexerRow.Slug, "error", err)
	}
	if previous == "" && status == notify.StateOK {
		// The first healthy run of a new indexer is not a state change.
		return
	}
	r.notifier.DispatchAsync(ctx, settings, notify.Event{
		Kind:     notify.KindIndexerState,
		Source:   indexerRow.Name,
		Previous: previous,
		Current:  status,
		Error:    r.managerRedacted(indexerRow.ID, outcome.Err),
		Path:     "/indexers",
	})
}

// notifyNewReleases groups every indexer of one pass into a single message:
// the anti-spam rule for a new-release burst is the grouping itself.
func (r *Runner) notifyNewReleases(ctx context.Context, outcomes []RunOutcome) {
	if len(outcomes) == 0 {
		return
	}
	settings, err := r.store.LoadSettings(ctx)
	if err != nil {
		return
	}
	total := 0
	var names []string
	var titles []string
	for _, outcome := range outcomes {
		if outcome.Stats == nil || outcome.Stats.New <= 0 {
			continue
		}
		total += outcome.Stats.New
		names = append(names, outcome.Name)
		for _, item := range outcome.Stats.Items {
			if len(titles) >= 10 {
				break
			}
			if strings.TrimSpace(item.Title) != "" {
				titles = append(titles, item.Title)
			}
		}
	}
	if total == 0 {
		return
	}
	r.notifier.DispatchAsync(ctx, settings, notify.Event{
		Kind:   notify.KindNewReleases,
		Source: strings.Join(names, ", "),
		Count:  total,
		Titles: titles,
		Path:   "/releases",
	})
}

func allErrors(statuses []string) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, status := range statuses {
		// A blocked run (Cloudflare challenge) is a failure for the
		// auto-disable policy even though its history status is distinct.
		if status != "error" && status != "blocked" {
			return false
		}
	}
	return true
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
			r.notifier.DispatchAsync(ctx, settings, notify.Event{
				Kind:   notify.KindBackupFailed,
				Source: "automatic backup",
				Error:  err.Error(),
				Path:   "/settings?section=backup",
			})
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

// refreshSeeders walks the oldest provenance rows and asks the source that
// supplied each release for fresh swarm counters. Manager.Search applies the
// per-indexer daily query limit and the shared host rate limiter; a failure or
// a quota response only leaves that row unchanged.
func (r *Runner) refreshSeeders(ctx context.Context, ttl time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	candidates, err := r.store.SeedersCandidates(ctx, time.Now(), ttl, 50)
	if err != nil {
		r.logger.Warn("seeders refresh: list candidates", "error", err)
		return
	}
	updated := 0
	for _, row := range candidates {
		if ctx.Err() != nil {
			break
		}
		indexerRow, getErr := r.store.GetIndexer(ctx, row.IndexerID)
		if getErr != nil {
			continue
		}
		if indexer.LiveExcluded(indexerRow) {
			_ = r.store.MarkSeedersChecked(ctx, row.ReleaseID, row.IndexerID, time.Now())
			continue
		}
		connector, _, connErr := r.manager.Connector(ctx, row.IndexerID)
		if connErr != nil {
			_ = r.store.MarkSeedersChecked(ctx, row.ReleaseID, row.IndexerID, time.Now())
			continue
		}
		if connector.Capabilities().LocalOnly {
			_ = r.store.MarkSeedersChecked(ctx, row.ReleaseID, row.IndexerID, time.Now())
			continue
		}
		items, searchErr := r.manager.Search(ctx, row.IndexerID, engine.SearchRequest{
			Mode: engine.ModeSearch, Query: row.Title, Limit: 20,
		})
		if searchErr != nil {
			_ = r.store.MarkSeedersChecked(ctx, row.ReleaseID, row.IndexerID, time.Now())
			continue
		}
		checked := time.Now()
		for _, item := range items {
			if store.NormalizeSearch(item.Title) != store.NormalizeSearch(row.Title) {
				continue
			}
			if err := r.store.UpdateSourceSwarm(ctx, row.ReleaseID, row.IndexerID,
				item.Seeders, item.Leechers, checked); err == nil {
				updated++
			}
			break
		}
		_ = r.store.MarkSeedersChecked(ctx, row.ReleaseID, row.IndexerID, checked)
	}
	if updated > 0 {
		r.logger.Info("seeders refresh", "updated", updated, "ttl", ttl.String())
	}
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
	// Stored sources have their own window. The general retention above is
	// off by default; memorised new releases must not grow forever, while
	// favorites and downloads are protected by the store query.
	if settings.StoredRetentionDays > 0 {
		before := time.Now().AddDate(0, 0, -settings.StoredRetentionDays)
		if removed, err := r.store.PruneStoredReleases(ctx, before); err != nil {
			r.logger.Warn("stored retention prune failed", "subsystem", "maintenance", "error", err)
		} else if removed > 0 {
			r.logger.Info("stored retention prune", "removed", removed, "days", settings.StoredRetentionDays)
		}
	}
	historyDays := settings.HistoryRetentionDays
	// The setting is 30 by default; zero is an explicit "keep forever".
	if historyDays > 0 {
		before := time.Now().AddDate(0, 0, -historyDays)
		pruned, err := r.store.PruneActivity(ctx, before)
		if err != nil {
			r.logger.Warn("activity retention cleanup failed", "subsystem", "maintenance", "error", err)
		} else if pruned.History > 0 || pruned.Samples > 0 || pruned.Searches > 0 {
			r.logger.Info("activity retention cleanup",
				"history", pruned.History, "samples", pruned.Samples,
				"searches", pruned.Searches, "days", historyDays)
		}
	}
	// historyDays == 0 is an explicit "keep forever": no fallback cleanup.
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
