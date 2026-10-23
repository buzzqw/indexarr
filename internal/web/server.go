package web

import (
	"bytes"
	"context"
	"crypto/subtle"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/categories"
	"github.com/buzzqw/indexarr/internal/ingest"
	"github.com/buzzqw/indexarr/internal/logbuffer"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/torznabapi"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed static
var staticFiles embed.FS

var templates = map[string]*template.Template{}

var pageNames = []string{
	"dashboard", "releases", "release", "sources", "source_form", "source_bulk",
	"runs", "run", "rules", "logs", "settings", "maintenance", "discover", "error",
}

var templateFuncMap = template.FuncMap{
	"bytes":    humanBytes,
	"date":     humanDate,
	"dateOnly": humanDateOnly,
	"ago":      humanAgo,
	"catName":  categories.Name,
	"num": func(value int64) string {
		if value < 0 {
			return "?"
		}
		return strconv.FormatInt(value, 10)
	},
	"add":  func(a, b int) int { return a + b },
	"sub":  func(a, b int) int { return a - b },
	"mulf": func(a float64, b float64) float64 { return a * b },
	"pct": func(value, total int64) string {
		if total <= 0 {
			return "0%"
		}
		return strconv.FormatInt(value*100/total, 10) + "%"
	},
	"eq": func(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) },
	"truncate": func(text string, max int) string {
		runes := []rune(text)
		if len(runes) <= max {
			return text
		}
		return string(runes[:max]) + "…"
	},
	"categoryGroups": categories.Groups,
}

// partialTemplates are HTML fragments rendered without the layout, for live
// updates (the releases search).
var partialTemplates = map[string]*template.Template{}

func init() {
	for _, name := range pageNames {
		files := []string{"templates/layout.html", "templates/" + name + ".html"}
		if name == "releases" {
			files = append(files, "templates/releases_results.html")
		}
		tmpl := template.New("layout.html").Funcs(templateFuncMap)
		tmpl = template.Must(tmpl.ParseFS(templateFiles, files...))
		templates[name] = tmpl
	}
	partialTemplates["releases_results"] = template.Must(
		template.New("releases_results").Funcs(templateFuncMap).
			ParseFS(templateFiles, "templates/releases_results.html"))
}

// base is the data every page shares with the layout.
type base struct {
	Title        string
	Section      string
	Version      string
	AssetVersion string
	CSRF         string
	Message      string
	Error        string
	TorznabURL   string
	Uptime       string
	AutoRefresh  int
}

// Config wires the web interface to the rest of the application.
type Config struct {
	Store   *store.Store
	Engine  *ingest.Engine
	Logger  *slog.Logger
	Logs    *logbuffer.Buffer
	Level   *slog.LevelVar
	Version string
	Started time.Time
	Restart func()
	// Admin is the versioned /api/v2 handler. It authenticates with the
	// Torznab API key itself, so it is mounted outside the UI session.
	Admin http.Handler
}

// Server serves the web interface, the Torznab API and the health endpoint.
type Server struct {
	store   *store.Store
	engine  *ingest.Engine
	log     *slog.Logger
	version string
	started time.Time
	torznab *torznabapi.Server
	static  http.Handler
	logs    *logbuffer.Buffer
	level   *slog.LevelVar
	restart func()
	admin   http.Handler
}

// New builds the HTTP server.
func New(cfg Config) *Server {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	logs := cfg.Logs
	if logs == nil {
		logs = logbuffer.New(logbuffer.DefaultMaxEntries)
	}
	level := cfg.Level
	if level == nil {
		level = new(slog.LevelVar)
		level.Set(slog.LevelInfo)
	}
	srv := &Server{
		store:   cfg.Store,
		engine:  cfg.Engine,
		log:     logger,
		version: cfg.Version,
		started: cfg.Started,
		torznab: torznabapi.New(cfg.Store, logger, cfg.Version),
		static:  http.StripPrefix("/static/", http.FileServer(http.FS(sub))),
		logs:    logs,
		level:   level,
		restart: cfg.Restart,
		admin:   cfg.Admin,
	}
	if cfg.Engine != nil {
		srv.torznab.SetLiveSearch(cfg.Engine.SearchLive)
	}
	return srv
}

// Handler builds the complete route table.
func (s *Server) Handler() http.Handler {
	ui := http.NewServeMux()
	ui.HandleFunc("GET /{$}", s.dashboard)
	ui.HandleFunc("GET /releases", s.releasesPage)
	ui.HandleFunc("GET /releases/results", s.releasesResults)
	ui.HandleFunc("POST /releases/saved", s.savedSearchAdd)
	ui.HandleFunc("POST /releases/saved/{id}/delete", s.savedSearchDelete)
	ui.HandleFunc("GET /releases/{id}", s.releasePage)
	ui.HandleFunc("GET /releases/{id}/torrent", s.releaseTorrent)
	ui.HandleFunc("POST /releases/{id}/favorite", s.releaseFavorite)
	ui.HandleFunc("POST /releases/{id}/hidden", s.releaseHide)
	ui.HandleFunc("POST /releases/{id}/block", s.releaseBlock)
	ui.HandleFunc("POST /releases/{id}/send", s.releaseSend)
	ui.HandleFunc("POST /releases/{id}/delete", s.releaseDelete)
	ui.HandleFunc("POST /releases/delete", s.releasesDelete)
	ui.HandleFunc("GET /sources", s.sourcesPage)
	ui.HandleFunc("GET /sources/new", s.sourceNew)
	ui.HandleFunc("POST /sources/quick", s.sourcesQuickAdd)
	ui.HandleFunc("GET /sources/discover", s.sourcesDiscover)
	ui.HandleFunc("GET /sources/bulk", s.sourceBulkNew)
	ui.HandleFunc("POST /sources/bulk", s.sourceBulkCreate)
	ui.HandleFunc("POST /sources", s.sourceCreate)
	ui.HandleFunc("POST /sources/bulk-update", s.sourcesBulk)
	ui.HandleFunc("POST /sources/bulk-delete", s.sourcesBulkDelete)
	ui.HandleFunc("GET /sources/export", s.sourcesExport)
	ui.HandleFunc("POST /sources/import", s.sourcesImport)
	ui.HandleFunc("GET /sources/{id}/edit", s.sourceEdit)
	ui.HandleFunc("POST /sources/{id}", s.sourceUpdate)
	ui.HandleFunc("POST /sources/{id}/clone", s.sourceClone)
	ui.HandleFunc("POST /sources/{id}/delete", s.sourceDelete)
	ui.HandleFunc("POST /sources/{id}/run", s.sourceRun)
	ui.HandleFunc("POST /sources/{id}/test", s.sourceTest)
	ui.HandleFunc("POST /sources/{id}/cancel", s.sourceCancel)
	ui.HandleFunc("POST /sources/test-all", s.sourcesTestAll)
	ui.HandleFunc("POST /sources/run-all", s.sourcesRunAll)
	ui.HandleFunc("GET /runs", s.runsPage)
	ui.HandleFunc("GET /runs/{id}", s.runPage)
	ui.HandleFunc("POST /runs/clear", s.runsClear)
	ui.HandleFunc("GET /rules", s.rulesPage)
	ui.HandleFunc("POST /rules", s.rulesAdd)
	ui.HandleFunc("POST /rules/{id}/delete", s.rulesDelete)
	ui.HandleFunc("GET /logs", s.logsPage)
	ui.HandleFunc("GET /logs/download", s.logsDownload)
	ui.HandleFunc("POST /logs/clear", s.logsClear)
	ui.HandleFunc("POST /logs/level", s.logsSetLevel)
	ui.HandleFunc("GET /settings", s.settingsPage)
	ui.HandleFunc("POST /settings", s.settingsSave)
	ui.HandleFunc("POST /settings/apikey", s.settingsRegenerateKey)
	ui.HandleFunc("POST /settings/apikeys", s.settingsAPIKeyAdd)
	ui.HandleFunc("POST /settings/apikeys/{id}/delete", s.settingsAPIKeyDelete)
	ui.HandleFunc("POST /settings/flaresolverr/test", s.settingsTestFlareSolverr)
	ui.HandleFunc("GET /maintenance", s.maintenancePage)
	ui.HandleFunc("POST /maintenance", s.maintenanceAction)
	ui.HandleFunc("GET /maintenance/backup", s.maintenanceBackup)
	ui.HandleFunc("POST /maintenance/backup-now", s.maintenanceBackupNow)
	ui.HandleFunc("GET /maintenance/backups/{name}", s.maintenanceBackupDownload)
	ui.HandleFunc("POST /maintenance/backups/{name}/delete", s.maintenanceBackupDelete)
	ui.HandleFunc("POST /maintenance/restore", s.maintenanceRestore)

	outer := http.NewServeMux()
	// Torznab clients authenticate with their own API key: the UI's Basic
	// authentication must not wrap these routes.
	outer.Handle("/api", s.torznab)
	outer.Handle("/torznab/api", s.torznab)
	outer.Handle("/dl/", s.torznab)
	if s.admin != nil {
		outer.Handle("/api/v2/", s.admin)
	}
	outer.HandleFunc("GET /healthz", s.healthz)
	outer.HandleFunc("GET /metrics", s.metrics)
	outer.HandleFunc("GET /sw.js", s.serviceWorker)
	outer.Handle("GET /static/", http.HandlerFunc(s.serveStatic))
	outer.Handle("/", s.authenticate(s.withCSRF(ui)))
	return outer
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	// The JS and CSS are small and change with the binary; no-cache avoids a
	// browser keeping an old live-search script after an update.
	if strings.HasSuffix(r.URL.Path, ".webmanifest") {
		w.Header().Set("Content-Type", "application/manifest+json")
	}
	w.Header().Set("Cache-Control", "no-cache")
	s.static.ServeHTTP(w, r)
}

func (s *Server) serviceWorker(w http.ResponseWriter, r *http.Request) {
	data, err := staticFiles.ReadFile("static/sw.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Service-Worker-Allowed", "/")
	_, _ = w.Write(data)
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		settings, err := s.store.LoadSettings(r.Context())
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), settingsContextKey{}, settings))
		if settings.UIUser == "" {
			next.ServeHTTP(w, r)
			return
		}
		user, password, ok := r.BasicAuth()
		userOK := subtleCompare(user, settings.UIUser)
		passwordOK := verifyPassword(settings.UIPasswordHash, password)
		if !ok || !userOK || !passwordOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="indexarr"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) withCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if cookie, err := r.Cookie("indexarr_csrf"); err == nil {
			token = cookie.Value
		}
		if len(token) != 64 {
			fresh, err := newCSRFToken()
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			token = fresh
			http.SetCookie(w, &http.Cookie{
				Name:     "indexarr_csrf",
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				Secure:   requestIsHTTPS(r),
			})
		}
		if r.Method == http.MethodPost {
			// Multipart uploads (backup restore) carry the CSRF token in the
			// query string, so the middleware does not have to read the body.
			provided := r.URL.Query().Get("csrf")
			if provided == "" && !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data") {
				if err := r.ParseForm(); err != nil {
					http.Error(w, "invalid form", http.StatusBadRequest)
					return
				}
				provided = r.FormValue("csrf")
			}
			if token == "" || !subtleCompare(provided, token) {
				http.Error(w, "invalid CSRF token: reload the page and try again", http.StatusForbidden)
				return
			}
		}
		ctx := context.WithValue(r.Context(), csrfContextKey, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func subtleCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

type settingsContextKey struct{}

func settingsFromContext(ctx context.Context) (store.Settings, bool) {
	settings, ok := ctx.Value(settingsContextKey{}).(store.Settings)
	return settings, ok
}

func (s *Server) newBase(r *http.Request, title, section string) base {
	out := base{
		Title:        title,
		Section:      section,
		Version:      s.version,
		AssetVersion: strconv.FormatInt(s.started.Unix(), 10),
		CSRF:         csrfToken(r.Context()),
		Message:      r.URL.Query().Get("msg"),
		Error:        r.URL.Query().Get("err"),
		Uptime:       humanAgo(s.started),
	}
	settings, loaded := settingsFromContext(r.Context())
	if !loaded {
		settings, _ = s.store.LoadSettings(r.Context())
	}
	out.TorznabURL = torznabapi.BaseURL(r, settings) + "/api"
	return out
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data any, status int) {
	tmpl, ok := templates[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		s.log.Error("render page", "component", "indexarr", "subsystem", "web", "page", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	data := struct {
		base
		Status int
	}{base: s.newBase(r, http.StatusText(status), ""), Status: status}
	data.Error = message
	s.render(w, r, "error", data, status)
}

// renderPartial writes one HTML fragment without the layout, used by the live
// search to swap the results table.
func (s *Server) renderPartial(w http.ResponseWriter, name string, data any) {
	tmpl, ok := partialTemplates[name]
	if !ok {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("render partial", "component", "indexarr", "subsystem", "web", "partial", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// redirect sends a 303 with a one-line message rendered by the layout.
func redirect(w http.ResponseWriter, r *http.Request, path, message, problem string) {
	values := url.Values{}
	if message != "" {
		values.Set("msg", message)
	}
	if problem != "" {
		values.Set("err", problem)
	}
	target := path
	if encoded := values.Encode(); encoded != "" {
		target += "?" + encoded
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.LoadStats(r.Context())
	if err != nil {
		http.Error(w, `{"status":"error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `{"status":"ok","version":%q,"uptime_seconds":%d,"releases":%d,"sources":%d,"db_bytes":%d}`,
		s.version, int64(time.Since(s.started).Seconds()), stats.Releases, stats.Sources, stats.DBBytes)
}

func humanBytes(value int64) string {
	if value <= 0 {
		return "0 B"
	}
	const unit = 1024
	if value < unit {
		return strconv.FormatInt(value, 10) + " B"
	}
	div, exp := int64(unit), 0
	for n := value / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGTPE"[exp])
}

// downloadFilename delegates the safe name to the Torznab package, so the
// browser download and the Torznab enclosure agree.
func downloadFilename(title string) string {
	return torznabapi.TorrentFilename(title)
}

func humanDate(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.Local().Format("2006-01-02 15:04")
}

func humanDateOnly(value time.Time) string {
	if value.IsZero() {
		return "—"
	}
	return value.Local().Format("2006-01-02")
}

func humanAgo(value time.Time) string {
	if value.IsZero() {
		return "never"
	}
	elapsed := time.Since(value)
	if elapsed < 0 {
		elapsed = 0
	}
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return strconv.Itoa(int(elapsed.Minutes())) + "m ago"
	case elapsed < 24*time.Hour:
		return strconv.Itoa(int(elapsed.Hours())) + "h ago"
	default:
		return strconv.Itoa(int(elapsed.Hours()/24)) + "d ago"
	}
}

// pager carries the pagination links of the releases list.
type pager struct {
	Page      int
	Pages     int
	Total     int64
	Offset    int
	Limit     int
	FirstItem int
	LastItem  int
	Prev      string
	Next      string
}

func newPager(r *http.Request, path string, total int64, limit, offset int) pager {
	page := offset/limit + 1
	pages := int((total + int64(limit) - 1) / int64(limit))
	if pages < 1 {
		pages = 1
	}
	first := offset + 1
	if total == 0 {
		first = 0
	}
	last := offset + limit
	if last > int(total) {
		last = int(total)
	}
	out := pager{
		Page:      page,
		Pages:     pages,
		Total:     total,
		Offset:    offset,
		Limit:     limit,
		FirstItem: first,
		LastItem:  last,
	}
	if page > 1 {
		out.Prev = pageLink(r, path, page-1)
	}
	if page < pages {
		out.Next = pageLink(r, path, page+1)
	}
	return out
}

func pageLink(r *http.Request, path string, page int) string {
	values := r.URL.Query()
	values.Set("page", strconv.Itoa(page))
	return path + "?" + values.Encode()
}
