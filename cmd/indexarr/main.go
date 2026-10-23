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

	"github.com/buzzqw/indexarr/internal/backup"
	"github.com/buzzqw/indexarr/internal/config"
	"github.com/buzzqw/indexarr/internal/ingest"
	"github.com/buzzqw/indexarr/internal/logbuffer"
	"github.com/buzzqw/indexarr/internal/store"
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

	engine := ingest.New(st, logger)
	engine.Start(ctx)

	httpServer := &http.Server{
		Addr: cfg.Listen,
		Handler: web.New(web.Config{
			Store: st, Engine: engine, Logger: logger, Logs: logBuffer, Level: levelVar,
			Version: version, Started: time.Now(),
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
