// Command indexarr is a self-hosted Torznab indexer: it periodically queries
// RSS feeds, Jackett and Prowlarr, stores releases (metadata, magnets and
// .torrent files) in SQLite and serves them to Sonarr/Radarr and the web UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/buzzqw/indexarr/internal/adminapi"
	"github.com/buzzqw/indexarr/internal/backup"
	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/config"
	"github.com/buzzqw/indexarr/internal/downloadproxy"
	coreengine "github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/family"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/health"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/logbuffer"
	"github.com/buzzqw/indexarr/internal/logfile"
	"github.com/buzzqw/indexarr/internal/logsafe"
	"github.com/buzzqw/indexarr/internal/native"
	"github.com/buzzqw/indexarr/internal/notify"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/syncjob"
	"github.com/buzzqw/indexarr/internal/vault"
	"github.com/buzzqw/indexarr/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// maskSecret keeps enough of a key to identify it without publishing it in
// the journal. Four characters are enough for "which key is this" and useless
// for authentication.
func maskSecret(value string) string {
	if len(value) <= 4 {
		return "…"
	}
	return value[:4] + "…"
}

func logWebInterfaceReady(logger *slog.Logger, base, key string) {
	logger.Info("web interface ready",
		"url", base, "torznab", base+"/api?apikey="+maskSecret(key))
}

// backfillReleaseFiles parses the .torrent payloads already in the database
// and fills release_files. A row is marked parsed even when the torrent has no
// file list, so a malformed payload is not retried on every boot.
func backfillReleaseFiles(ctx context.Context, st *store.Store, logger *slog.Logger) {
	for {
		items, err := st.ReleasesWithoutFiles(ctx, 200)
		if err != nil {
			logger.Warn("file-name backfill: read releases", "error", err)
			return
		}
		if len(items) == 0 {
			return
		}
		for _, item := range items {
			if ctx.Err() != nil {
				return
			}
			var files []store.ReleaseFile
			if meta, parseErr := feed.ParseTorrent(item.Torrent); parseErr == nil {
				for _, file := range meta.Files {
					files = append(files, store.ReleaseFile{Path: file.Path, Size: file.Size})
				}
			}
			if err := st.ReplaceReleaseFiles(ctx, item.ID, files); err != nil {
				logger.Warn("file-name backfill: store files", "release_id", item.ID, "error", err)
			}
		}
	}
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "vault" {
		os.Exit(runVaultCommand(os.Args[2:]))
	}
	configPath := flag.String("config", "indexarr.json", "JSON configuration file (created on first start)")
	listen := flag.String("listen", "", "HTTP listen address, overrides the configuration")
	dbPath := flag.String("db", "", "SQLite database path, overrides the configuration")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("indexarr %s\n", version)
		return
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal(err)
	}
	if strings.TrimSpace(*listen) != "" {
		cfg.Listen = strings.TrimSpace(*listen)
	}
	if strings.TrimSpace(*dbPath) != "" {
		absolute, err := filepath.Abs(strings.TrimSpace(*dbPath))
		if err != nil {
			fatal(err)
		}
		cfg.DBPath = absolute
	}
	logBuffer := logbuffer.New(logbuffer.DefaultMaxEntries)
	levelVar := new(slog.LevelVar)
	levelVar.Set(parseLevel(cfg.LogLevel))
	logOut, logFile, logFileErr := openLogOutput(cfg)
	if logFile != nil {
		defer logFile.Close()
	}
	logger := newLogger(levelVar, logBuffer, logOut)
	if logFileErr != nil {
		logger.Warn("log file unavailable; logging to stdout only", "path", cfg.LogFile, "error", logFileErr)
	}
	if cfg.MemoryLimitMB > 0 {
		debug.SetMemoryLimit(int64(cfg.MemoryLimitMB) << 20)
		logger.Info("memory limit", "mb", cfg.MemoryLimitMB)
	}
	coreengine.SetProductVersion(version)
	logger.Info("starting indexarr", "version", version, "database", cfg.DBPath, "listen", cfg.Listen)

	// Resolve the vault before touching the database: a restore staged as
	// <db>.restore is applied next, and its secrets must be checked with the
	// same key afterwards. A missing key never blocks the legacy path.
	secretVault, keySource, vaultErr := vault.Resolve(vault.ResolveOptions{
		EnvKey:        os.Getenv("INDEXARR_MASTER_KEY"),
		EnvKeyFile:    os.Getenv("INDEXARR_MASTER_KEY_FILE"),
		ConfigPath:    cfg.VaultKeyPath,
		DBPath:        cfg.DBPath,
		UserConfigDir: os.UserConfigDir,
	})
	if vaultErr != nil {
		logger.Warn("secrets vault unavailable; native indexers and sealed source secrets are disabled", "error", vaultErr)
	} else {
		logger.Info("secrets vault ready", "key_source", keySource.Kind,
			"key_path", keySource.Path, "key_in_data_dir", keySource.InDataDir)
		if keySource.InDataDir {
			logger.Warn("vault key is in the data directory: move it outside, for example with 'indexarr vault move-key -to <path>'",
				"key_source", keySource.Kind, "key_path", keySource.Path)
		}
	}

	if addr := strings.TrimSpace(os.Getenv("INDEXARR_PPROF")); addr != "" {
		logger.Warn("pprof endpoint enabled", "addr", addr)
		go func() {
			if err := http.ListenAndServe(addr, nil); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("pprof server stopped", "error", err)
			}
		}()
	}
	restored := false
	if applied, err := backup.ApplyPending(cfg.DBPath); err != nil {
		logger.Error("cannot apply pending restore", "error", err)
		rejected := cfg.DBPath + ".restore.rejected"
		_ = os.Remove(rejected)
		_ = os.Rename(cfg.DBPath+".restore", rejected)
	} else if applied {
		restored = true
		logger.Info("database restored from pending backup")
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fatal(err)
	}
	defer st.Close()
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	// From here on the settings-level secrets (Telegram token, webhook URL)
	// are sealed in app_secrets. The migration moves the plaintext rows of an
	// older installation once, then the plaintext key is gone.
	if secretVault != nil {
		st.AttachVault(secretVault)
		if moved, migrateErr := st.MigrateSettingsSecrets(startupCtx); migrateErr != nil {
			logger.Warn("cannot move the notification secrets into the vault", "error", migrateErr)
		} else if moved > 0 {
			logger.Info("notification secrets moved into the vault", "secrets", moved)
		}
	}

	if settings, err := st.LoadSettings(startupCtx); err == nil && settings.LogLevel != "" {
		levelVar.Set(parseLevel(settings.LogLevel))
	}
	if indexers, listErr := st.ListIndexers(startupCtx); listErr == nil {
		for _, indexer := range indexers {
			if strings.Contains(indexer.Tags, "__preview") {
				_ = st.DeleteIndexer(startupCtx, indexer.ID)
			}
		}
	}
	key, created, err := st.EnsureAPIKey(startupCtx)
	if err != nil {
		startupCancel()
		fatal(err)
	}
	if created {
		logger.Info("generated a Torznab API key", "apikey", maskSecret(key))
	}
	if secretVault != nil && restored {
		opened, verifyErr := verifyRestoredSecrets(startupCtx, st, secretVault)
		switch {
		case verifyErr != nil:
			logger.Error("restored database: the current vault key does not open the stored secrets", "error", verifyErr)
		case opened == 0:
			logger.Info("restored database: no sealed secrets to verify")
		default:
			logger.Info("restored database: vault key opens the stored secrets", "secrets", opened)
		}
	}
	startupCancel()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	settings, err := st.LoadSettings(ctx)
	if err != nil {
		logger.Warn("cannot load settings; using defaults", "error", err)
		settings = store.DefaultSettings()
	}

	notifier := notify.NewDispatcher(st, logger)
	if written, syncErr := catalog.Sync(ctx, st); syncErr != nil {
		logger.Warn("cannot sync the built-in catalog", "error", syncErr)
	} else if written > 0 {
		logger.Info("built-in catalog synchronized", "definitions", written)
		notifier.DispatchAsync(ctx, settings, notify.Event{
			Kind:   notify.KindCatalogUpdated,
			Source: "built-in catalog",
			Count:  written,
			Path:   "/settings?section=advanced",
		})
	}
	if paired, pairErr := catalog.PairKnownChannels(ctx, st); pairErr != nil {
		logger.Warn("cannot pair known feed/search channels", "error", pairErr)
	} else if paired > 0 {
		logger.Info("paired feed/search channels", "indexers", paired)
	}

	// One per-host budget is shared by the native transport and the legacy
	// RSS/HTML/Torznab client, so a host never receives the rate twice.
	politeness := coreengine.NewPoliteness(
		settings.HostRate,
		settings.HostBurst,
		settings.BreakerThreshold,
		settings.BreakerCooldown(),
		coreengine.RealClock{},
	)
	transport := coreengine.NewTransport(coreengine.TransportConfig{
		Timeout:          settings.FetchTimeout(),
		UserAgent:        settings.UserAgent,
		RatePerSecond:    settings.HostRate,
		Burst:            settings.HostBurst,
		FailureThreshold: settings.BreakerThreshold,
		Cooldown:         settings.BreakerCooldown(),
		Politeness:       politeness,
		Solver:           coreengine.NewFlareSolverrSolver(settings.FlareSolverrURL, time.Duration(settings.FlareSolverrTimeoutSeconds)*time.Second),
	})
	registry := coreengine.NewRegistry()
	if err := family.Register(registry); err != nil {
		logger.Warn("cannot register family connectors", "error", err)
	}
	indexerManager, err := indexer.New(indexer.Config{
		Store:             st,
		Vault:             secretVault,
		Registry:          registry,
		Transport:         transport,
		Logger:            logger,
		MaxTorrentsPerRun: settings.MaxTorrentsPerRun,
	})
	if err != nil {
		logger.Error("native indexer runtime disabled", "error", err)
		indexerManager = nil
	}
	if indexerManager != nil {
		// Close browser sessions nobody uses: a Cloudflare-protected indexer
		// queried once would otherwise keep a Chromium alive for hours.
		go indexerManager.StartSessionSweeper(ctx)
		// Searches a flood-limited site had not taken yet when the previous
		// process stopped are sent again at the site's pace.
		go indexerManager.ResumePendingSearches(ctx)
	}

	indexerService, err := indexersvc.New(indexersvc.Config{
		Store: st, Vault: secretVault, Manager: indexerManager,
	})
	if err != nil {
		logger.Warn("native indexer pages disabled", "error", err)
		indexerService = nil
	}

	var syncService *syncjob.Service
	if secretVault != nil {
		baseURL := strings.TrimSpace(settings.PublicURL)
		if baseURL == "" {
			baseURL = displayURL(cfg.Listen)
			logger.Warn("public_url is empty; app sync will advertise a local address",
				"base_url", baseURL)
		}
		syncService, err = syncjob.New(syncjob.Config{
			Store: st, Vault: secretVault, Logger: logger, BaseURL: baseURL,
		})
		if err != nil {
			logger.Warn("app sync disabled", "error", err)
			syncService = nil
		} else {
			syncService.Start(ctx)
		}
	}

	var downloadResolver func(context.Context, int64) ([]byte, string, string, error)
	var grabGuard func(context.Context, int64) error
	var grabRecord func(context.Context, int64, int64, string) error
	if indexerManager != nil {
		grabGuard = indexerManager.CheckGrab
		grabRecord = indexerManager.RecordGrab
	}
	if indexerManager != nil && secretVault != nil {
		resolver, resolverErr := downloadproxy.New(downloadproxy.Config{
			Store: st, Vault: secretVault, Manager: indexerManager, Notifier: notifier,
		})
		if resolverErr != nil {
			logger.Warn("download proxy disabled", "error", resolverErr)
		} else {
			downloadResolver = func(ctx context.Context, id int64) ([]byte, string, string, error) {
				payload, err := resolver.Resolve(ctx, id)
				if err != nil {
					return nil, "", "", err
				}
				if len(payload.Body) == 0 {
					if magnet := strings.TrimSpace(payload.Magnet); magnet != "" {
						return []byte(magnet), "application/x-magnet", "", nil
					}
					if link := strings.TrimSpace(payload.Ed2k); link != "" {
						return []byte(link), "application/x-ed2k", "", nil
					}
					return nil, "", "", errors.New("payload unavailable")
				}
				return payload.Body, payload.ContentType, payload.Filename, nil
			}
		}
	}

	var nativeRunner *native.Runner
	if indexerManager != nil {
		nativeRunner, err = native.New(native.Config{
			Store:    st,
			Manager:  indexerManager,
			Logger:   logger,
			Interval: settings.DefaultInterval(),
			Workers:  settings.Workers,
			Notifier: notifier,
		})
		if err != nil {
			logger.Warn("native scheduler disabled", "error", err)
			nativeRunner = nil
		} else {
			nativeRunner.Start(ctx)
		}
	}

	// The catalog health job checks the configured sites every six hours and
	// keeps the definition states in sync with the indexer history.
	var healthChecker *health.Checker
	if indexerManager != nil {
		healthChecker, err = health.New(health.Config{
			Store: st, Manager: indexerManager, Logger: logger, Notifier: notifier,
		})
		if err != nil {
			logger.Warn("health checker disabled", "error", err)
			healthChecker = nil
		} else {
			healthChecker.Start(ctx)
		}
	}

	// Intelligent deduplication runs as an idempotent background job. The
	// first pass starts shortly after boot (so a fresh upgrade cleans the
	// existing rows) and repeats every six hours; the enrichment step then
	// recovers the missing infohashes within a per-site budget.
	if st != nil {
		go dedupeLoop(ctx, st, indexerManager, logger)
		go backfillReleaseFiles(ctx, st, logger)
	}

	var adminHandler http.Handler
	adminServer, err := adminapi.New(adminapi.Config{
		Store: st, Vault: secretVault, Manager: indexerManager, Sync: syncService,
		Logger: logger, Version: version, Notifier: notifier,
	})
	if err != nil {
		logger.Warn("admin API disabled", "error", err)
	} else {
		adminHandler = adminServer.Handler()
	}

	httpServer := &http.Server{
		Addr: cfg.Listen,
		Handler: web.New(web.Config{
			Store: st, Logger: logger, Logs: logBuffer, Level: levelVar,
			Version: version, Started: time.Now(), Admin: adminHandler,
			Native: indexerManager, Indexers: indexerService, Scheduler: nativeRunner,
			DownloadResolver: downloadResolver,
			Vault:            secretVault, Sync: syncService,
			VaultKey: keySource.Info(),
			LogFile:  logFilePath(logFile), LogMaxMB: cfg.LogMaxMB, LogMaxFiles: cfg.LogMaxFiles,
			GrabGuard: grabGuard, GrabRecord: grabRecord,
			Politeness: politeness,
			Restart: func() {
				if err := restartProcess(stop); err != nil {
					logger.Error("cannot restart for restore", "error", err)
				}
			},
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		base := displayURL(cfg.Listen)
		logWebInterfaceReady(logger, base, key)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server stopped", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("HTTP shutdown", "error", err)
	}
	if nativeRunner != nil {
		nativeRunner.Wait()
	}
	if syncService != nil {
		syncService.Wait()
	}
	if healthChecker != nil {
		healthChecker.Wait()
	}
	// Close the FlareSolverr browsers owned by the cached connectors. A
	// stopped indexarr must not leave Chromium running behind it.
	if indexerManager != nil {
		sessionCtx, sessionCancel := context.WithTimeout(context.Background(), 10*time.Second)
		indexerManager.CloseAll(sessionCtx)
		sessionCancel()
	}
	logger.Info("stopped")
}

// verifyRestoredSecrets confirms that the current key opens every sealed secret
// in a restored database. It never returns or logs plaintext.
func verifyRestoredSecrets(ctx context.Context, st *store.Store, v *vault.Vault) (int, error) {
	opened := 0
	indexers, err := st.ListIndexers(ctx)
	if err != nil {
		return opened, err
	}
	for _, indexer := range indexers {
		secrets, err := st.ListSecrets(ctx, indexer.ID)
		if err != nil {
			return opened, err
		}
		for _, secret := range secrets {
			if _, err := v.Open(secret.Nonce, secret.Ciphertext); err != nil {
				return opened, fmt.Errorf("indexer %s: sealed secret cannot be opened with the configured key: %w", indexer.Slug, err)
			}
			opened++
		}
	}
	return opened, nil
}

func runVaultCommand(args []string) int {
	if len(args) == 0 {
		vaultUsage()
		return 2
	}
	switch args[0] {
	case "export-key":
		fs := flag.NewFlagSet("vault export-key", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		configPath := fs.String("config", "indexarr.json", "JSON configuration file")
		dbPath := fs.String("db", "", "SQLite database path, overrides the configuration")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		cfg, err := config.Load(*configPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "indexarr:", err)
			return 1
		}
		if strings.TrimSpace(*dbPath) != "" {
			absolute, err := filepath.Abs(strings.TrimSpace(*dbPath))
			if err != nil {
				fmt.Fprintln(os.Stderr, "indexarr:", err)
				return 1
			}
			cfg.DBPath = absolute
		}
		encoded, source, err := vault.ExportKey(vault.ResolveOptions{
			EnvKey:        os.Getenv("INDEXARR_MASTER_KEY"),
			EnvKeyFile:    os.Getenv("INDEXARR_MASTER_KEY_FILE"),
			ConfigPath:    cfg.VaultKeyPath,
			DBPath:        cfg.DBPath,
			UserConfigDir: os.UserConfigDir,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "indexarr:", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "warning: the master key is secret; keep this output offline and out of logs\n")
		fmt.Fprintf(os.Stderr, "key source: %s", source.Kind)
		if source.Path != "" {
			fmt.Fprintf(os.Stderr, " (%s)", source.Path)
		}
		if source.InDataDir {
			fmt.Fprintf(os.Stderr, "; warning: the key is in the data directory, move it with 'indexarr vault move-key -to <path>'")
		}
		fmt.Fprintln(os.Stderr)
		fmt.Println(encoded)
		return 0
	case "move-key":
		fs := flag.NewFlagSet("vault move-key", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		configPath := fs.String("config", "indexarr.json", "JSON configuration file")
		dbPath := fs.String("db", "", "SQLite database path, overrides the configuration")
		target := fs.String("to", "", "new master key path (required)")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if strings.TrimSpace(*target) == "" {
			fmt.Fprintln(os.Stderr, "indexarr: vault move-key requires -to <path>")
			return 2
		}
		cfg, err := config.Load(*configPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "indexarr:", err)
			return 1
		}
		if strings.TrimSpace(*dbPath) != "" {
			absolute, err := filepath.Abs(strings.TrimSpace(*dbPath))
			if err != nil {
				fmt.Fprintln(os.Stderr, "indexarr:", err)
				return 1
			}
			cfg.DBPath = absolute
		}
		moved, err := vault.MoveLegacyKey(cfg.DBPath, *target)
		if err != nil {
			fmt.Fprintln(os.Stderr, "indexarr:", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "moved the legacy master key to %s\n", moved)
		fmt.Fprintln(os.Stderr, "set vault_key_path in indexarr.json (or INDEXARR_MASTER_KEY_FILE) to that path")
		return 0
	default:
		vaultUsage()
		return 2
	}
}

func vaultUsage() {
	fmt.Fprintln(os.Stderr, `usage: indexarr vault <command>

commands:
  export-key [-config file] [-db path]   print the resolved master key as base64
  move-key   [-config file] [-db path] -to <path>
                                         move the legacy <db>.key file to path`)
}

func parseLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// openLogOutput returns where the text log goes: stdout, plus the rotating log
// file unless the configuration turns it off. A file that cannot be opened
// leaves stdout alone; the caller logs why.
func openLogOutput(cfg config.Config) (io.Writer, *logfile.Rotator, error) {
	if cfg.LogFile == config.LogFileOff {
		return os.Stdout, nil, nil
	}
	file, err := logfile.Open(cfg.LogFile, int64(cfg.LogMaxMB)<<20, cfg.LogMaxFiles)
	if err != nil {
		return os.Stdout, nil, err
	}
	return io.MultiWriter(os.Stdout, file), file, nil
}

// logFilePath is the path shown on the Logs page, "" when there is no file.
func logFilePath(file *logfile.Rotator) string {
	if file == nil {
		return ""
	}
	return file.Path()
}

// newLogger writes the normal text log to out (stdout and the rotating file)
// and keeps the same records in the in-memory buffer used by the Logs page.
// The secret filter runs first, so neither the file nor the journal ever see a
// key. The level variable is shared with the web interface, so the Logs page
// can change verbosity without a restart.
func newLogger(levelVar *slog.LevelVar, buffer *logbuffer.Buffer, out io.Writer) *slog.Logger {
	base := slog.NewTextHandler(out, &slog.HandlerOptions{Level: levelVar})
	return slog.New(logsafe.NewHandler(logbuffer.NewHandler(base, buffer)))
}

// displayURL turns a listen address into a clickable local URL.
func displayURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// mergeDuplicatesWithRetry absorbs the short SQLite lock that a live write
// can hold while the periodic cleanup starts. A single lock is expected and
// must not produce a warning.
func mergeDuplicatesWithRetry(ctx context.Context, st *store.Store) (store.DedupeStats, error) {
	var stats store.DedupeStats
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		stats, err = st.MergeDuplicates(ctx)
		if err == nil || !isLockedError(err) {
			return stats, err
		}
		wait := time.Duration(1<<attempt) * 250 * time.Millisecond
		select {
		case <-ctx.Done():
			return stats, ctx.Err()
		case <-time.After(wait):
		}
	}
	return stats, err
}

func isLockedError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "database is locked") || strings.Contains(message, "sqlite_busy")
}

// dedupeLoop keeps the cache compact without blocking a request. Both steps
// are idempotent: a second pass on a clean cache removes zero rows and the
// enrichment job skips rows that already received a hash.
func dedupeLoop(ctx context.Context, st *store.Store, manager *indexer.Manager, logger *slog.Logger) {
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			runCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			stats, err := mergeDuplicatesWithRetry(runCtx, st)
			if err != nil {
				logger.Warn("dedupe: cleanup failed", "error", err)
			} else if stats.Removed > 0 {
				logger.Info("dedupe: duplicates merged",
					"before", stats.Before, "after", stats.After,
					"removed", stats.Removed,
					"by_hash", stats.HashMerged, "by_title_size", stats.TitleMerged)
			}
			if manager != nil {
				if _, err := manager.EnrichMissingInfohashes(runCtx, indexer.EnrichTargets()); err != nil {
					logger.Warn("dedupe: infohash enrichment failed", "error", err)
				}
			}
			cancel()
			timer.Reset(6 * time.Hour)
		}
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "indexarr:", err)
	os.Exit(1)
}
