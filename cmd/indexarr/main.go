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
	logger := newLogger(levelVar, logBuffer)
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
	if settings, err := st.LoadSettings(startupCtx); err == nil && settings.LogLevel != "" {
		levelVar.Set(parseLevel(settings.LogLevel))
	}
	if interrupted, err := st.FinishInterruptedRuns(startupCtx); err == nil && interrupted > 0 {
		logger.Warn("marked interrupted runs", "count", interrupted)
	}
	key, created, err := st.EnsureAPIKey(startupCtx)
	if err != nil {
		startupCancel()
		fatal(err)
	}
	if created {
		logger.Info("generated a Torznab API key", "apikey", key)
	}
	if secretVault != nil {
		st.SetSecretCodec(vaultCodec{secretVault})
		canSeal := true
		if restored {
			opened, verifyErr := verifyRestoredSecrets(startupCtx, st, secretVault)
			switch {
			case verifyErr != nil:
				// Do not seal plaintext rows with a key that cannot open the
				// secrets already in the restored database: that would mix two
				// keys in one file.
				canSeal = false
				logger.Error("restored database: the current vault key does not open the stored secrets", "error", verifyErr)
			case opened == 0:
				logger.Info("restored database: no sealed secrets to verify")
			default:
				logger.Info("restored database: vault key opens the stored secrets", "secrets", opened)
			}
		}
		if canSeal {
			if sealed, err := st.SealLegacySecrets(startupCtx); err != nil {
				logger.Warn("cannot seal legacy source secrets", "error", err)
			} else if sealed > 0 {
				logger.Info("sealed legacy source secrets", "count", sealed)
			}
			// A sealed URL can only be copied into the legacy bridge once the
			// vault is available, so retry the idempotent backfill after start-up.
			if bridged, err := st.BackfillLegacySources(startupCtx); err != nil {
				logger.Warn("cannot bridge sources after unlocking the vault", "error", err)
			} else if bridged > 0 {
				logger.Info("bridged legacy sources", "count", bridged)
			}
		}
	} else if restored {
		logger.Warn("restored database: no vault key available, sealed secrets are not readable")
	}
	startupCancel()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	settings, err := st.LoadSettings(ctx)
	if err != nil {
		logger.Warn("cannot load settings; using defaults", "error", err)
		settings = store.DefaultSettings()
	}

	if written, syncErr := catalog.Sync(ctx, st); syncErr != nil {
		logger.Warn("cannot sync the built-in catalog", "error", syncErr)
	} else if written > 0 {
		logger.Info("built-in catalog synchronized", "definitions", written)
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
	var grabGuard func(context.Context, int64) error
	var grabRecord func(context.Context, int64, int64, string) error
	if indexerManager != nil {
		grabGuard = indexerManager.CheckGrab
		grabRecord = indexerManager.RecordGrab
	}
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
	// The legacy path keeps its own base transport (no SSRF guard) so Jackett
	// and Prowlarr on a LAN address remain reachable, but it shares the same
	// per-host limiter and breaker as the native transport.
	legacyTransport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	engine.SetHTTPClient(&http.Client{
		Transport: politeness.RoundTripper(legacyTransport),
		// The per-run context is the real deadline; this is a safety net.
		Timeout: 4 * time.Minute,
	})
	engine.Start(ctx)

	httpServer := &http.Server{
		Addr: cfg.Listen,
		Handler: web.New(web.Config{
			Store: st, Engine: engine, Logger: logger, Logs: logBuffer, Level: levelVar,
			Version: version, Started: time.Now(), Admin: adminHandler,
			Native: indexerManager, Indexers: indexerService,
			DownloadResolver: downloadResolver,
			Vault:            secretVault, Sync: syncService,
			GrabGuard: grabGuard, GrabRecord: grabRecord,
			Politeness: politeness,
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

// vaultCodec adapts *vault.Vault to store.SecretCodec. The adapter lives here so
// the SQLite layer never imports the vault package.
type vaultCodec struct{ v *vault.Vault }

func (c vaultCodec) SealString(plaintext string) (string, error) { return c.v.SealString(plaintext) }
func (c vaultCodec) OpenString(blob string) (string, error)      { return c.v.OpenString(blob) }

// verifyRestoredSecrets confirms that the current key opens every sealed secret
// in a restored database. It checks the sealed source fields first and then the
// native indexer secrets. It never returns or logs plaintext.
func verifyRestoredSecrets(ctx context.Context, st *store.Store, v *vault.Vault) (int, error) {
	opened, err := st.VerifySecretCodec(ctx)
	if err != nil {
		return opened, err
	}
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
