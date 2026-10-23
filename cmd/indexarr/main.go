// Command indexarr is a self-hosted Torznab indexer: it periodically queries
// RSS feeds, Jackett and Prowlarr, stores releases (metadata, magnets and
// .torrent files) in SQLite and serves them to Sonarr/Radarr and the web UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
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
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/ingest"
	"github.com/buzzqw/indexarr/internal/logbuffer"
	"github.com/buzzqw/indexarr/internal/native"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/syncjob"
	"github.com/buzzqw/indexarr/internal/vault"
	"github.com/buzzqw/indexarr/internal/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
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
	logger := newLogger(levelVar, logBuffer)
	logger.Info("starting indexarr", "version", version, "database", cfg.DBPath, "listen", cfg.Listen)
	if addr := strings.TrimSpace(os.Getenv("INDEXARR_PPROF")); addr != "" {
		logger.Warn("pprof endpoint enabled", "addr", addr)
		go func() {
			if err := http.ListenAndServe(addr, nil); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("pprof server stopped", "error", err)
			}
		}()
	}
	if applied, err := backup.ApplyPending(cfg.DBPath); err != nil {
		logger.Error("cannot apply pending restore", "error", err)
		rejected := cfg.DBPath + ".restore.rejected"
		_ = os.Remove(rejected)
		_ = os.Rename(cfg.DBPath+".restore", rejected)
	} else if applied {
		logger.Info("database restored from pending backup")
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fatal(err)
	}
	defer st.Close()

	startupCtx, startupCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if settings, err := st.LoadSettings(startupCtx); err == nil && settings.LogLevel != "" {
		levelVar.Set(parseLevel(settings.LogLevel))
	}
	if interrupted, err := st.FinishInterruptedRuns(startupCtx); err == nil && interrupted > 0 {
		logger.Warn("marked interrupted runs", "count", interrupted)
	}
	key, created, err := st.EnsureAPIKey(startupCtx)
	startupCancel()
	if err != nil {
		fatal(err)
	}
	if created {
		logger.Info("generated a Torznab API key", "apikey", key)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	settings, err := st.LoadSettings(ctx)
	if err != nil {
		logger.Warn("cannot load settings; using defaults", "error", err)
		settings = store.DefaultSettings()
	}

	// The vault protects indexer secrets in the database and in backups. A
	// missing key never blocks the legacy sources: only native indexers that
	// actually have secrets become unavailable.
	secretVault, vaultErr := vault.FromEnvOrFile("INDEXARR_MASTER_KEY", cfg.DBPath+".key")
	if vaultErr != nil {
		logger.Warn("secrets vault unavailable; native indexers with secrets are disabled", "error", vaultErr)
	}
	if written, syncErr := catalog.Sync(ctx, st); syncErr != nil {
		logger.Warn("cannot sync the built-in catalog", "error", syncErr)
	} else if written > 0 {
		logger.Info("built-in catalog synchronized", "definitions", written)
	}

	transport := coreengine.NewTransport(coreengine.TransportConfig{
		Timeout:   settings.FetchTimeout(),
		UserAgent: settings.UserAgent,
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
	if indexerManager != nil && secretVault != nil {
		resolver, resolverErr := downloadproxy.New(downloadproxy.Config{
			Store: st, Vault: secretVault, Manager: indexerManager,
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
		})
		if err != nil {
			logger.Warn("native scheduler disabled", "error", err)
			nativeRunner = nil
		} else {
			nativeRunner.Start(ctx)
		}
	}

	var adminHandler http.Handler
	adminServer, err := adminapi.New(adminapi.Config{
		Store: st, Vault: secretVault, Manager: indexerManager, Sync: syncService,
		Logger: logger, Version: version,
	})
	if err != nil {
		logger.Warn("admin API disabled", "error", err)
	} else {
		adminHandler = adminServer.Handler()
	}

	engine := ingest.New(st, logger)
	engine.Start(ctx)

	httpServer := &http.Server{
		Addr: cfg.Listen,
		Handler: web.New(web.Config{
			Store: st, Engine: engine, Logger: logger, Logs: logBuffer, Level: levelVar,
			Version: version, Started: time.Now(), Admin: adminHandler,
			Native: indexerManager, Indexers: indexerService,
			DownloadResolver: downloadResolver,
			Restart: func() {
				if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
					logger.Error("cannot restart for restore", "error", err)
				}
			},
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		base := displayURL(cfg.Listen)
		logger.Info("web interface ready",
			"url", base, "torznab", base+"/api?apikey="+key)
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
	engine.Wait()
	if nativeRunner != nil {
		nativeRunner.Wait()
	}
	if syncService != nil {
		syncService.Wait()
	}
	logger.Info("stopped")
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

// newLogger writes the normal text log to stdout and keeps the same records in
// the in-memory buffer used by the Logs page. The level variable is shared with
// the web interface, so the Logs page can change verbosity without a restart.
func newLogger(levelVar *slog.LevelVar, buffer *logbuffer.Buffer) *slog.Logger {
	base := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: levelVar})
	return slog.New(logbuffer.NewHandler(base, buffer))
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

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "indexarr:", err)
	os.Exit(1)
}
