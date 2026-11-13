package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/buzzqw/indexarr/internal/backup"
	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/config"
	"github.com/buzzqw/indexarr/internal/downloadproxy"
	"github.com/buzzqw/indexarr/internal/health"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/native"
	"github.com/buzzqw/indexarr/internal/notify"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/syncjob"
	"github.com/buzzqw/indexarr/internal/upkeep"
	"github.com/buzzqw/indexarr/internal/vault"
)

// loadStartupConfig reads the command line and the configuration file. The
// -listen and -db options override the file. It reports -version separately,
// so main can print it and exit before anything is opened.
func loadStartupConfig() (config.Config, bool) {
	configPath := flag.String("config", "indexarr.json", "JSON configuration file (created on first start)")
	listen := flag.String("listen", "", "HTTP listen address, overrides the configuration")
	dbPath := flag.String("db", "", "SQLite database path, overrides the configuration")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		return config.Config{}, true
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
	return cfg, false
}

// resolveVault finds the master key (environment, key file, configuration or
// the default places) and opens the secrets vault. Without a key indexarr
// still starts: the vault is nil and the features that need it say so.
func resolveVault(cfg config.Config, logger *slog.Logger) (*vault.Vault, vault.KeySource) {
	secretVault, keySource, vaultErr := vault.Resolve(vault.ResolveOptions{
		EnvKey:        os.Getenv("INDEXARR_MASTER_KEY"),
		EnvKeyFile:    os.Getenv("INDEXARR_MASTER_KEY_FILE"),
		ConfigPath:    cfg.VaultKeyPath,
		DBPath:        cfg.DBPath,
		UserConfigDir: os.UserConfigDir,
	})
	if vaultErr != nil {
		logger.Warn("secrets vault unavailable; native indexers and sealed source secrets are disabled", "error", vaultErr)
		return secretVault, keySource
	}
	logger.Info("secrets vault ready", "key_source", keySource.Kind,
		"key_path", keySource.Path, "key_in_data_dir", keySource.InDataDir)
	if keySource.InDataDir {
		logger.Warn("vault key is in the data directory: move it outside, for example with 'indexarr vault move-key -to <path>'",
			"key_source", keySource.Kind, "key_path", keySource.Path)
	}
	return secretVault, keySource
}

// startPprof serves the Go profiler on INDEXARR_PPROF when it is set.
func startPprof(logger *slog.Logger) {
	addr := strings.TrimSpace(os.Getenv("INDEXARR_PPROF"))
	if addr == "" {
		return
	}
	logger.Warn("pprof endpoint enabled", "addr", addr)
	go func() {
		if err := http.ListenAndServe(addr, nil); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("pprof server stopped", "error", err)
		}
	}()
}

// applyPendingRestore puts a backup staged as <db>.restore in place of the
// database. A restore that cannot be applied is set aside as
// <db>.restore.rejected, so it is not tried again at every start.
func applyPendingRestore(dbPath string, logger *slog.Logger) bool {
	applied, err := backup.ApplyPending(dbPath)
	if err != nil {
		logger.Error("cannot apply pending restore", "error", err)
		rejected := dbPath + ".restore.rejected"
		_ = os.Remove(rejected)
		_ = os.Rename(dbPath+".restore", rejected)
		return false
	}
	if applied {
		logger.Info("database restored from pending backup")
	}
	return applied
}

// prepareStore runs the one-off start-up work on the opened database, within
// thirty seconds: settings secrets move into the vault, the saved log level
// applies, the catalog previews of a stopped run are dropped, the Torznab API
// key exists, and a restored database is checked against the current vault
// key. It returns the API key.
func prepareStore(st *store.Store, secretVault *vault.Vault, restored bool, levelVar *slog.LevelVar, logger *slog.Logger) string {
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer startupCancel()
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
	return key
}

// syncCatalog writes the definitions of the built-in catalog into the
// database, notifies when some changed, and pairs the feed and search
// channels of the sites that publish both.
func syncCatalog(ctx context.Context, st *store.Store, notifier *notify.Dispatcher, settings store.Settings, logger *slog.Logger) {
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
}

// downloadResolverFunc returns the payload of a release for the web
// interface: the file body, its content type and its file name. A magnet or
// an eD2k link comes back as the body, with its own content type.
type downloadResolverFunc = func(context.Context, int64) ([]byte, string, string, error)

// newDownloadResolver builds the download proxy. It needs the native runtime
// and the vault; without them it returns nil and the interface offers no
// proxied download.
func newDownloadResolver(st *store.Store, secretVault *vault.Vault, manager *indexer.Manager, notifier *notify.Dispatcher, logger *slog.Logger) downloadResolverFunc {
	if manager == nil || secretVault == nil {
		return nil
	}
	resolver, err := downloadproxy.New(downloadproxy.Config{
		Store: st, Vault: secretVault, Manager: manager, Notifier: notifier,
	})
	if err != nil {
		logger.Warn("download proxy disabled", "error", err)
		return nil
	}
	return func(ctx context.Context, id int64) ([]byte, string, string, error) {
		payload, err := resolver.Resolve(ctx, id)
		if err != nil {
			return nil, "", "", err
		}
		if len(payload.Body) > 0 {
			return payload.Body, payload.ContentType, payload.Filename, nil
		}
		if magnet := strings.TrimSpace(payload.Magnet); magnet != "" {
			return []byte(magnet), "application/x-magnet", "", nil
		}
		if link := strings.TrimSpace(payload.Ed2k); link != "" {
			return []byte(link), "application/x-ed2k", "", nil
		}
		return nil, "", "", errors.New("payload unavailable")
	}
}

// startAppSync starts the job that pushes the indexers to Sonarr, Radarr and
// the other apps. It needs the vault (the app API keys are sealed); the
// address it advertises is public_url, or the local listen address.
func startAppSync(ctx context.Context, st *store.Store, secretVault *vault.Vault, settings store.Settings, listen string, logger *slog.Logger) *syncjob.Service {
	if secretVault == nil {
		return nil
	}
	baseURL := strings.TrimSpace(settings.PublicURL)
	if baseURL == "" {
		baseURL = displayURL(listen)
		logger.Warn("public_url is empty; app sync will advertise a local address",
			"base_url", baseURL)
	}
	syncService, err := syncjob.New(syncjob.Config{
		Store: st, Vault: secretVault, Logger: logger, BaseURL: baseURL,
	})
	if err != nil {
		logger.Warn("app sync disabled", "error", err)
		return nil
	}
	syncService.Start(ctx)
	return syncService
}

// shutdown stops the HTTP server within fifteen seconds, waits for the
// background jobs to finish their current pass, and closes the FlareSolverr
// browsers owned by the cached connectors: a stopped indexarr must not leave
// Chromium running behind it.
func shutdown(httpServer *http.Server, nativeRunner *native.Runner, syncService *syncjob.Service,
	healthChecker *health.Checker, indexerManager *indexer.Manager, routine *upkeep.Routine,
	jobs *sync.WaitGroup, logger *slog.Logger) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Warn("HTTP shutdown", "error", err)
	}
	// The maintenance pass and the background jobs hold the same database:
	// wait for them before the manager closes the sessions and the deferred
	// st.Close() runs, so a VACUUM or a merge is never cut off mid-write.
	if routine != nil {
		routine.Wait()
	}
	if jobs != nil {
		jobs.Wait()
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
	if indexerManager != nil {
		sessionCtx, sessionCancel := context.WithTimeout(context.Background(), 10*time.Second)
		indexerManager.CloseAll(sessionCtx)
		sessionCancel()
	}
}
