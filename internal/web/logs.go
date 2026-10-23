package web

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/logbuffer"
	"github.com/buzzqw/indexarr/internal/store"
)

type logsPage struct {
	base
	Entries      []logbuffer.Entry
	Level        string
	RuntimeLevel string
	Query        string
	Component    string
	ProblemCount int
	Limit        int
	Auto         bool
	BufferSize   int
	MaxEntries   int
	DownloadURL  string
	ProblemsURL  string
}

func parseLogLevel(raw string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return slog.LevelDebug, false
	}
}

// logsRedirect keeps /logs working and folds the old page into Attività.
func (s *Server) logsRedirect(w http.ResponseWriter, r *http.Request) {
	redirectTab(w, r, "/activity", "logs")
}

func (s *Server) logsData(r *http.Request) logsPage {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	levelName := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("level")))
	minLevel, ok := parseLogLevel(levelName)
	if !ok {
		levelName = "debug"
		minLevel = slog.LevelDebug
	}
	limit := 300
	switch r.URL.Query().Get("limit") {
	case "100":
		limit = 100
	case "1000":
		limit = 1000
	}
	auto := r.URL.Query().Get("auto") == "1"
	component := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("component")))
	switch component {
	case "", "indexarr", "indexer", "rss", "notify":
	default:
		component = ""
	}
	filter := logbuffer.Filter{Limit: limit, MinLevel: minLevel, Query: query, Component: component}
	entries := s.logs.SnapshotFilter(filter)
	problemCount := len(s.logs.SnapshotFilter(logbuffer.Filter{
		MinLevel: slog.LevelWarn, Query: query, Component: component,
	}))

	values := url.Values{}
	values.Set("level", levelName)
	if query != "" {
		values.Set("q", query)
	}
	if component != "" {
		values.Set("component", component)
	}
	problemValues := url.Values{}
	problemValues.Set("level", "warn")
	if query != "" {
		problemValues.Set("q", query)
	}
	if component != "" {
		problemValues.Set("component", component)
	}
	runtimeLevel := "info"
	if s.level != nil {
		runtimeLevel = strings.ToLower(s.level.Level().String())
	}
	data := logsPage{
		base:         s.newBaseT(r, "activity.logs", "activity"),
		Entries:      entries,
		Level:        levelName,
		RuntimeLevel: runtimeLevel,
		Query:        query,
		Component:    component,
		ProblemCount: problemCount,
		Limit:        limit,
		Auto:         auto,
		BufferSize:   s.logs.Len(),
		MaxEntries:   s.logs.Cap(),
		DownloadURL:  "/logs/download?" + values.Encode(),
		ProblemsURL:  "/activity?tab=logs&" + problemValues.Encode(),
	}
	if auto {
		data.AutoRefresh = 10
	}
	return data
}

func (s *Server) logsDownload(w http.ResponseWriter, r *http.Request) {
	levelName := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("level")))
	minLevel, ok := parseLogLevel(levelName)
	if !ok {
		minLevel = slog.LevelDebug
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	component := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("component")))
	switch component {
	case "", "indexarr", "indexer", "rss", "notify":
	default:
		component = ""
	}
	text := s.logs.DumpFilter(logbuffer.Filter{
		MinLevel: minLevel, Query: query, Component: component,
	})
	filename := "indexarr-logs-" + time.Now().Format("20060102-150405") + ".txt"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(text))
}

func (s *Server) logsClear(w http.ResponseWriter, r *http.Request) {
	s.logs.Clear()
	redirect(w, r, "/logs", "Log buffer cleared", "")
}

func (s *Server) logsSetLevel(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.FormValue("runtime_level")))
	var level slog.Level
	switch name {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		redirect(w, r, "/logs", "", "Log level must be debug, info, warn or error")
		return
	}
	if s.level != nil {
		s.level.Set(level)
	}
	if err := s.store.SetSetting(r.Context(), store.KeyLogLevel, name); err != nil {
		redirect(w, r, "/logs", "", err.Error())
		return
	}
	s.log.Info("log level changed", "level", name)
	redirect(w, r, "/logs", "Log level set to "+name, "")
}
