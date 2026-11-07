package upkeep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/buzzqw/indexarr/internal/notify"
	"github.com/buzzqw/indexarr/internal/store"
)

// Step identifiers, in the order a run executes them. They are stable: the
// web UI translates "maintenance.step.<id>".
const (
	StepIntegrity  = "integrity"
	StepForeign    = "foreign_keys"
	StepTitleIndex = "title_index"
	StepDedupe     = "dedupe"
	StepNoLinks    = "no_links"
	StepOrphans    = "orphans"
	StepFTS        = "fts_optimize"
	StepAnalyze    = "analyze"
	StepReindex    = "reindex"
	StepVacuum     = "vacuum"
	StepCheckpoint = "checkpoint"
)

// Step outcomes.
const (
	StatusOK        = "ok"
	StatusWarn      = "warn"
	StatusError     = "error"
	StatusSkipped   = "skipped"
	StatusPostponed = "postponed"
)

// Run triggers.
const (
	TriggerIdle   = "idle"
	TriggerManual = "manual"
)

const (
	dailyEvery  = 24 * time.Hour
	weeklyEvery = 7 * 24 * time.Hour
	// runTimeout bounds a whole pass. On the reference database (760 MB,
	// 95,000 releases, NVMe) a weekly pass took 25 seconds on the live service;
	// slow storage is given a wide margin.
	runTimeout = 30 * time.Minute
	// vacuumFreeShare is the share of free pages that makes a daily VACUUM
	// worth its full rewrite; below it compaction waits for the weekly pass.
	vacuumFreeShare = 0.10
	// overdueAfter is when a busy installation stops waiting for the full
	// idle window: a server whose apps poll every few minutes would never be
	// quiet for a quarter of an hour, so after two days a short pause is
	// enough.
	overdueAfter = 48 * time.Hour
	overdueIdle  = 2 * time.Minute
)

// StepResult is one line of a run report. Count is the number the step is
// about (rows removed, violations found); Detail carries a short technical
// text such as SQLite's integrity verdict.
type StepResult struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Count      int64  `json:"count,omitempty"`
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// Report is the outcome of one run.
type Report struct {
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt time.Time    `json:"finished_at"`
	Trigger    string       `json:"trigger"`
	Weekly     bool         `json:"weekly"`
	Status     string       `json:"status"`
	Steps      []StepResult `json:"steps"`
	// DBBefore and DBAfter are the database file sizes around the run.
	DBBefore int64 `json:"db_before"`
	DBAfter  int64 `json:"db_after"`
}

// Duration is the length of the run.
func (r Report) Duration() time.Duration {
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() {
		return 0
	}
	return r.FinishedAt.Sub(r.StartedAt)
}

// Status is what the web UI shows about the routine.
type Status struct {
	Enabled     bool
	IdleMinutes int
	Running     bool
	Requested   bool
	DailyAt     time.Time
	WeeklyAt    time.Time
	NextDue     time.Time
	IdleFor     time.Duration
	LastActive  time.Time
	Last        *Report
}

// Config wires a Routine.
type Config struct {
	Store    *store.Store
	Activity *Activity
	Logger   *slog.Logger
	Notifier *notify.Dispatcher
	// Busy reports other work that must not overlap a pass, such as an
	// automatic backup being written or a full "run all". Optional.
	Busy func() bool
}

// Routine is the idle maintenance job.
type Routine struct {
	store    *store.Store
	activity *Activity
	logger   *slog.Logger
	notifier *notify.Dispatcher
	busy     func() bool

	running   atomic.Bool
	requested atomic.Bool
	mu        sync.Mutex
	state     store.MaintenanceState
	loaded    bool
	now       func() time.Time
	createdAt time.Time
}

// New builds a Routine. Store is required.
func New(cfg Config) (*Routine, error) {
	if cfg.Store == nil {
		return nil, errors.New("upkeep: store is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Routine{
		store:     cfg.Store,
		activity:  cfg.Activity,
		logger:    logger.With("component", "maintenance"),
		notifier:  cfg.Notifier,
		busy:      cfg.Busy,
		now:       time.Now,
		createdAt: time.Now(),
	}, nil
}

// Start checks once a minute whether a pass is due and runs it in the same
// goroutine, so two passes never overlap.
func (r *Routine) Start(ctx context.Context) {
	if r == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.Tick(ctx)
			}
		}
	}()
}

// Request asks for a pass at the next tick, without waiting for an idle
// window or for the daily schedule.
func (r *Routine) Request() {
	if r == nil {
		return
	}
	r.requested.Store(true)
}

// Tick runs a pass when one is requested, or when the automatic routine is
// enabled, the daily pass is due and indexarr has been idle long enough. It
// returns the report of the pass, or nil when nothing ran.
func (r *Routine) Tick(ctx context.Context) *Report {
	if r == nil || ctx.Err() != nil {
		return nil
	}
	if r.busy != nil && r.busy() {
		return nil
	}
	state, err := r.loadState(ctx)
	if err != nil {
		r.logger.Warn("maintenance: load state", "error", err)
		return nil
	}
	now := r.now()
	trigger := ""
	if r.requested.Load() {
		trigger = TriggerManual
	} else {
		settings, err := r.store.LoadSettings(ctx)
		if err != nil || !settings.MaintenanceAuto {
			return nil
		}
		if !state.DailyAt.IsZero() && now.Sub(state.DailyAt) < dailyEvery {
			return nil
		}
		idle := r.idleNeeded(settings, state, now)
		if r.activity.IdleFor(now) < idle {
			return nil
		}
		trigger = TriggerIdle
	}
	if !r.running.CompareAndSwap(false, true) {
		return nil
	}
	defer r.running.Store(false)
	r.requested.Store(false)
	report := r.run(ctx, trigger, state, now)
	return &report
}

// idleNeeded is the quiet time a due pass waits for: the configured window,
// or overdueIdle when no pass has completed for overdueAfter (counted from
// the start of the process when there was never one).
func (r *Routine) idleNeeded(settings store.Settings, state store.MaintenanceState, now time.Time) time.Duration {
	since := state.DailyAt
	if since.Before(r.createdAt) {
		since = r.createdAt
	}
	if now.Sub(since) >= overdueAfter {
		return overdueIdle
	}
	return time.Duration(settings.MaintenanceIdleMinutes) * time.Minute
}

// Status describes the routine for the web UI.
func (r *Routine) Status(ctx context.Context) Status {
	var out Status
	if r == nil {
		return out
	}
	settings, err := r.store.LoadSettings(ctx)
	if err != nil {
		settings = store.DefaultSettings()
	}
	state, _ := r.loadState(ctx)
	now := r.now()
	out.Enabled = settings.MaintenanceAuto
	out.IdleMinutes = settings.MaintenanceIdleMinutes
	out.Running = r.running.Load()
	out.Requested = r.requested.Load()
	out.DailyAt = state.DailyAt
	out.WeeklyAt = state.WeeklyAt
	if !state.DailyAt.IsZero() {
		out.NextDue = state.DailyAt.Add(dailyEvery)
	}
	out.LastActive = r.activity.LastAt()
	out.IdleFor = r.activity.IdleFor(now)
	if state.Report != "" {
		var report Report
		if json.Unmarshal([]byte(state.Report), &report) == nil {
			out.Last = &report
		}
	}
	return out
}

func (r *Routine) loadState(ctx context.Context) (store.MaintenanceState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded {
		return r.state, nil
	}
	state, err := r.store.LoadMaintenanceState(ctx)
	if err != nil {
		return state, err
	}
	r.state = state
	r.loaded = true
	return state, nil
}

func (r *Routine) saveState(ctx context.Context, state store.MaintenanceState) {
	r.mu.Lock()
	r.state = state
	r.loaded = true
	r.mu.Unlock()
	if err := r.store.SaveMaintenanceState(ctx, state); err != nil {
		r.logger.Warn("maintenance: save state", "error", err)
	}
}

// run executes one pass. An idle pass stops before the next step as soon as
// a client is served again; the steps it did not reach are reported as
// postponed and the daily pass stays due, so the next idle window finishes
// it. A manual pass always runs to the end.
//
// now is the scheduling clock (it decides which passes are due and is what
// the state remembers); the report measures real time.
func (r *Routine) run(parent context.Context, trigger string, state store.MaintenanceState, now time.Time) Report {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), runTimeout)
	defer cancel()
	started := time.Now()
	weekly := state.WeeklyAt.IsZero() || now.Sub(state.WeeklyAt) >= weeklyEvery
	report := Report{StartedAt: started, Trigger: trigger, Weekly: weekly, Status: StatusOK}
	if dbReport, err := r.store.LoadDBReport(ctx); err == nil {
		report.DBBefore = dbReport.PageCount * dbReport.PageSize
	}
	r.logger.Info("maintenance: started", "trigger", trigger, "weekly", weekly)

	interrupted := func() bool {
		if trigger == TriggerManual {
			return parent.Err() != nil
		}
		return parent.Err() != nil || r.activity.LastAt().After(started)
	}

	broken := false
	for _, step := range r.steps(weekly) {
		if broken {
			report.Steps = append(report.Steps, StepResult{ID: step.id, Status: StatusSkipped})
			continue
		}
		if report.Status == StatusPostponed || interrupted() {
			report.Status = StatusPostponed
			report.Steps = append(report.Steps, StepResult{ID: step.id, Status: StatusPostponed})
			continue
		}
		begin := time.Now()
		result := step.run(ctx)
		result.ID = step.id
		result.DurationMS = time.Since(begin).Milliseconds()
		report.Steps = append(report.Steps, result)
		switch result.Status {
		case StatusError:
			report.Status = StatusError
			r.logger.Warn("maintenance: step failed", "step", step.id, "detail", result.Detail)
			if step.guard {
				// A database that fails its integrity check is left alone:
				// deleting or rewriting pages could make a repair harder.
				broken = true
			}
		case StatusWarn:
			if report.Status == StatusOK {
				report.Status = StatusWarn
			}
		}
	}

	report.FinishedAt = time.Now()
	if dbReport, err := r.store.LoadDBReport(ctx); err == nil {
		report.DBAfter = dbReport.PageCount * dbReport.PageSize
	}
	if encoded, err := json.Marshal(report); err == nil {
		state.Report = string(encoded)
	}
	if report.Status != StatusPostponed {
		state.DailyAt = now
		if weekly {
			state.WeeklyAt = now
		}
	}
	r.saveState(ctx, state)
	r.logger.Info("maintenance: finished", "trigger", trigger, "status", report.Status,
		"duration_ms", report.Duration().Milliseconds(),
		"db_before", report.DBBefore, "db_after", report.DBAfter)
	if report.Status == StatusError {
		r.notifyFailure(parent, report)
	}
	return report
}

type step struct {
	id string
	// guard marks a check whose failure stops every later step.
	guard bool
	run   func(context.Context) StepResult
}

func (r *Routine) steps(weekly bool) []step {
	st := r.store
	steps := []step{
		{id: StepIntegrity, guard: true, run: func(ctx context.Context) StepResult {
			check, label := st.QuickCheck, "quick_check"
			if weekly {
				check, label = st.IntegrityCheck, "integrity_check"
			}
			verdict, err := check(ctx)
			if err != nil {
				return failed(err)
			}
			if strings.TrimSpace(strings.ToLower(verdict)) != "ok" {
				return StepResult{Status: StatusError, Detail: label + ": " + clip(verdict, 300)}
			}
			return StepResult{Status: StatusOK, Detail: label}
		}},
		{id: StepForeign, run: func(ctx context.Context) StepResult {
			violations, err := st.ForeignKeyCheck(ctx)
			if err != nil {
				return failed(err)
			}
			if violations > 0 {
				return StepResult{Status: StatusWarn, Count: violations}
			}
			return StepResult{Status: StatusOK}
		}},
		{id: StepTitleIndex, run: func(ctx context.Context) StepResult {
			if err := retryLocked(ctx, func() error { return st.FTSCheck(ctx) }); err == nil {
				return StepResult{Status: StatusOK}
			}
			// The title index is derived from the releases table: rebuilding
			// it is always safe and repairs a search that misses rows.
			if err := retryLocked(ctx, func() error { return st.FTSRebuild(ctx) }); err != nil {
				return failed(err)
			}
			return StepResult{Status: StatusWarn, Detail: "rebuilt"}
		}},
		{id: StepDedupe, run: func(ctx context.Context) StepResult {
			var stats store.DedupeStats
			err := retryLocked(ctx, func() error {
				var err error
				stats, err = st.MergeDuplicates(ctx)
				return err
			})
			if err != nil {
				return failed(err)
			}
			return StepResult{Status: StatusOK, Count: stats.Removed}
		}},
		{id: StepNoLinks, run: func(ctx context.Context) StepResult {
			var removed int64
			err := retryLocked(ctx, func() error {
				var err error
				removed, err = st.DeleteReleasesWithoutLinks(ctx)
				return err
			})
			if err != nil {
				return failed(err)
			}
			return StepResult{Status: StatusOK, Count: removed}
		}},
		{id: StepOrphans, run: func(ctx context.Context) StepResult {
			var removed int64
			err := retryLocked(ctx, func() error {
				var err error
				removed, err = st.DeleteOrphans(ctx)
				return err
			})
			if err != nil {
				return failed(err)
			}
			return StepResult{Status: StatusOK, Count: removed}
		}},
		{id: StepFTS, run: func(ctx context.Context) StepResult {
			return simple(retryLocked(ctx, func() error { return st.FTSOptimize(ctx) }))
		}},
		{id: StepAnalyze, run: func(ctx context.Context) StepResult {
			return simple(retryLocked(ctx, func() error { return st.Analyze(ctx) }))
		}},
	}
	if weekly {
		steps = append(steps, step{id: StepReindex, run: func(ctx context.Context) StepResult {
			return simple(retryLocked(ctx, func() error { return st.Reindex(ctx) }))
		}})
	}
	steps = append(steps,
		step{id: StepVacuum, run: func(ctx context.Context) StepResult {
			return r.vacuum(ctx, weekly)
		}},
		step{id: StepCheckpoint, run: func(ctx context.Context) StepResult {
			busy, frames, done, err := st.Checkpoint(ctx)
			if err != nil {
				return failed(err)
			}
			if busy != 0 {
				// A reader kept part of the log: SQLite finishes it later.
				return StepResult{Status: StatusOK, Count: done, Detail: fmt.Sprintf("%d/%d", done, frames)}
			}
			return StepResult{Status: StatusOK, Count: done}
		}},
	)
	return steps
}

// vacuum rewrites the file when it is worth it: on the weekly pass, or when
// at least a tenth of the pages are free. It needs room for a full copy of
// the database next to it, and skips (without failing) when the disk lacks it.
func (r *Routine) vacuum(ctx context.Context, weekly bool) StepResult {
	report, err := r.store.LoadDBReport(ctx)
	if err != nil {
		return failed(err)
	}
	size := report.PageCount * report.PageSize
	share := 0.0
	if report.PageCount > 0 {
		share = float64(report.FreelistCount) / float64(report.PageCount)
	}
	if !weekly && share < vacuumFreeShare {
		return StepResult{Status: StatusSkipped, Detail: "not_needed"}
	}
	if free := freeBytes(r.store.Path()); free >= 0 && free < size+size/5 {
		return StepResult{Status: StatusWarn, Detail: "no_space"}
	}
	if err := retryLocked(ctx, func() error { return r.store.Vacuum(ctx) }); err != nil {
		return failed(err)
	}
	after, err := r.store.LoadDBReport(ctx)
	if err != nil {
		return StepResult{Status: StatusOK}
	}
	saved := size - after.PageCount*after.PageSize
	if saved < 0 {
		saved = 0
	}
	return StepResult{Status: StatusOK, Count: saved}
}

func (r *Routine) notifyFailure(ctx context.Context, report Report) {
	if r.notifier == nil {
		return
	}
	settings, err := r.store.LoadSettings(ctx)
	if err != nil {
		return
	}
	detail := ""
	for _, step := range report.Steps {
		if step.Status == StatusError {
			detail = step.ID
			if step.Detail != "" {
				detail += ": " + step.Detail
			}
			break
		}
	}
	r.notifier.DispatchAsync(ctx, settings, notify.Event{
		Kind:   notify.KindMaintenanceFailed,
		Source: "maintenance",
		Error:  detail,
		Path:   "/settings?section=maintenance",
	})
}

func failed(err error) StepResult {
	return StepResult{Status: StatusError, Detail: clip(err.Error(), 300)}
}

func simple(err error) StepResult {
	if err != nil {
		return failed(err)
	}
	return StepResult{Status: StatusOK}
}

func clip(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

// retryLocked repeats fn while SQLite reports a lock held by another writer,
// for at most four attempts with a growing pause.
func retryLocked(ctx context.Context, fn func() error) error {
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		err = fn()
		if err == nil || !isLocked(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(1<<attempt) * 500 * time.Millisecond):
		}
	}
	return err
}

func isLocked(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "sqlite_busy")
}
