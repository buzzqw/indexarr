package ingest

import (
	"context"
	"errors"
	"time"

	"github.com/buzzqw/indexarr/internal/backup"
)

// Start launches the workers and the scheduler. It is safe to call once; a
// second call is a no-op.
func (e *Engine) Start(ctx context.Context) {
	e.startOnce.Do(func() {
		workers := 2
		if settings, err := e.store.LoadSettings(ctx); err == nil && settings.Workers > 0 {
			workers = settings.Workers
		}
		if workers > 16 {
			workers = 16
		}
		for i := 0; i < workers; i++ {
			e.wg.Add(1)
			go func() {
				defer e.wg.Done()
				e.worker(ctx)
			}()
		}
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			e.scheduler(ctx)
		}()
	})
}

// Wait blocks until every worker and the scheduler have stopped. Cancel the
// context passed to Start first.
func (e *Engine) Wait() { e.wg.Wait() }

func (e *Engine) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case sourceID := <-e.jobs:
			result, err := e.RunSource(ctx, sourceID)
			switch {
			case errors.Is(err, ErrAlreadyRunning):
				continue
			case err != nil:
				e.log.Warn("run failed", "source_id", sourceID, "error", err)
			default:
				e.log.Info("run complete", "source_id", sourceID,
					"seen", result.Seen, "new", result.New,
					"updated", result.Updated, "blocked", result.Blocked,
					"torrents", result.Torrents)
			}
		}
	}
}

func (e *Engine) scheduler(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	e.dispatch(ctx)
	lastMaintenance := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.dispatch(ctx)
			e.maybeBackup(ctx)
			if time.Since(lastMaintenance) >= 6*time.Hour {
				lastMaintenance = time.Now()
				e.maintenance(ctx)
			}
		}
	}
}

// maybeBackup creates an automatic backup when the configured interval has
// elapsed. It returns immediately; the SQLite VACUUM INTO runs in the
// background and a second backup cannot start while one is in progress.
func (e *Engine) maybeBackup(ctx context.Context) {
	settings, err := e.store.LoadSettings(ctx)
	if err != nil || !settings.BackupEnabled {
		return
	}
	interval := time.Duration(settings.BackupIntervalHours) * time.Hour
	if interval < 6*time.Hour {
		interval = 6 * time.Hour
	}
	if latest := backup.LatestAutomatic(e.store.Path()); !latest.IsZero() && time.Since(latest) < interval {
		return
	}
	if time.Now().Unix() < e.backupRetry.Load() {
		return
	}
	if !e.backupRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer e.backupRunning.Store(false)
		backupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
		defer cancel()
		info, err := backup.Create(backupCtx, e.store, e.store.Path())
		if err != nil {
			e.backupRetry.Store(time.Now().Add(15 * time.Minute).Unix())
			e.log.Warn("automatic backup failed", "error", err)
			return
		}
		e.backupRetry.Store(0)
		e.log.Info("automatic backup created", "file", info.Name, "bytes", info.Size)
		if removed, err := backup.Prune(e.store.Path(), settings.BackupKeep); err != nil {
			e.log.Warn("backup rotation failed", "error", err)
		} else if removed > 0 {
			e.log.Info("old backups removed", "count", removed)
		}
	}()
}

func (e *Engine) dispatch(ctx context.Context) {
	settings, err := e.store.LoadSettings(ctx)
	if err != nil {
		e.log.Error("cannot load settings for the scheduler", "error", err)
		return
	}
	sources, err := e.store.ListSources(ctx)
	if err != nil {
		e.log.Error("cannot list sources", "error", err)
		return
	}
	now := time.Now()
	for _, source := range sources {
		if !source.Enabled || e.IsRunning(source.ID) {
			continue
		}
		interval := source.Interval(settings.DefaultInterval())
		if interval < time.Minute {
			interval = time.Minute
		}
		if last := source.LastRunAt; !last.IsZero() {
			elapsed := now.Sub(last)
			if source.LastError != "" {
				// Back off after a failure, but never wait the whole interval
				// for a source that is down: a short retry recovers quickly.
				retry := interval
				if retry > 15*time.Minute {
					retry = 15 * time.Minute
				}
				if retry < 2*time.Minute {
					retry = 2 * time.Minute
				}
				if elapsed < retry {
					continue
				}
			} else if elapsed < interval {
				continue
			}
		}
		select {
		case e.jobs <- source.ID:
		default:
			// Queue full: the next tick will try again.
		}
	}
}

func (e *Engine) maintenance(ctx context.Context) {
	settings, err := e.store.LoadSettings(ctx)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if settings.RetentionDays > 0 {
		before := time.Now().AddDate(0, 0, -settings.RetentionDays)
		if removed, err := e.store.PruneReleases(ctx, before); err != nil {
			e.log.Warn("retention prune failed", "error", err)
		} else if removed > 0 {
			e.log.Info("retention prune", "removed", removed, "days", settings.RetentionDays)
		}
	}
	if removed, err := e.store.ClearRuns(ctx, time.Now().AddDate(0, 0, -90)); err != nil {
		e.log.Warn("run history cleanup failed", "error", err)
	} else if removed > 0 {
		e.log.Info("run history cleanup", "removed", removed)
	}
	if err := e.store.Optimize(ctx); err != nil {
		e.log.Warn("database optimize failed", "error", err)
	}
}
