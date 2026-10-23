// Package native schedules periodic runs of the native indexers.
//
// The legacy sources scheduler keeps running the bridged indexers through the
// ingest package. This package is deliberately narrower: it lists the indexer
// rows, skips the disabled ones and the ones that still resolve to the legacy
// Torznab or RSS connector, and runs everything else through
// indexer.Manager.Run. In particular it never touches the sources table.
package native

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/indexer"
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
	ReasonDisabled          = "disabled"
	ReasonLegacyBridge      = "legacy bridge"
	ReasonInvalidDefinition = "invalid definition"
)

var (
	// ErrStoreRequired is returned by New without a store.
	ErrStoreRequired = errors.New("native: store is required")

	// ErrManagerRequired is returned by New without an indexer manager.
	ErrManagerRequired = errors.New("native: manager is required")
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
	Skipped   bool // legacy/bridge or disabled
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

	startOnce sync.Once
	wg        sync.WaitGroup
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
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			r.logger.Info("native: scheduler stopped")
			return
		case <-ticker.C:
			r.runPass(ctx)
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

// RunOnce runs every enabled native indexer once, with at most Workers runs in
// flight. Legacy, disabled and invalid indexers are reported as skipped. The
// returned outcomes always follow ascending indexer ID, regardless of the
// completion order.
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

// RunIndexer runs one indexer, or explains why it was skipped. Unknown ids
// return an error; a legacy, disabled or invalid indexer returns a skipped
// outcome with a nil error. Run failures are returned both in the outcome and
// as the error.
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

// runOne classifies and, when appropriate, executes one indexer. It always
// reports the outcome through OnResult and emits a log line that contains no
// tracker URL and no secret.
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

	native, reason, err := r.classify(ctx, indexerRow)
	if err != nil {
		outcome.Err = err
		return outcome
	}
	if !native {
		outcome.Skipped = true
		outcome.Reason = reason
		return outcome
	}

	stats, runErr := r.manager.Run(ctx, indexerRow.ID)
	if errors.Is(runErr, indexer.ErrLegacyConnector) {
		// Defense in depth: the manager may resolve a legacy connector even
		// when the stored metadata looked native.
		outcome.Skipped = true
		outcome.Reason = ReasonLegacyBridge
		return outcome
	}
	if runErr != nil {
		outcome.Err = runErr
		return outcome
	}
	outcome.Stats = stats
	return outcome
}

// classify decides whether an indexer is native. It returns a skip reason when
// it is not, and an error only when the definition row itself cannot be read.
func (r *Runner) classify(ctx context.Context, indexerRow store.Indexer) (bool, string, error) {
	def, err := r.store.GetDefinition(ctx, indexerRow.DefinitionID)
	if err != nil {
		return false, "", err
	}

	family := strings.TrimSpace(def.Family)
	if isLegacyConnector(family) {
		return false, ReasonLegacyBridge, nil
	}

	parsed, parseErr := definition.Parse([]byte(def.Body))
	if parseErr != nil {
		// A broken body with no family hint cannot be classified safely. It
		// is skipped; the rest of the pass keeps running.
		if family == "" {
			r.logger.Warn("native: invalid definition",
				"indexer", indexerRow.Slug, "definition", def.ID)
			return false, ReasonInvalidDefinition, nil
		}
		// A family connector may not need the definition itself. Let the
		// manager decide and report the parse error on this indexer only.
		return true, "", nil
	}
	if isLegacyConnector(parsed.Connector) {
		return false, ReasonLegacyBridge, nil
	}
	return true, "", nil
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

// isLegacyConnector reports whether a connector name belongs to the legacy
// bridge. Comparison is case-insensitive because stored metadata is authored
// by hand.
func isLegacyConnector(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "torznab", "rss":
		return true
	default:
		return false
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
