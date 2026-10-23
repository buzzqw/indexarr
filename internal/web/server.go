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
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/ingest"
	"github.com/buzzqw/indexarr/internal/logbuffer"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/syncjob"
	"github.com/buzzqw/indexarr/internal/torznabapi"
	"github.com/buzzqw/indexarr/internal/vault"
)

//go:embed templates/*.html
var templateFiles embed.FS

//go:embed static
var staticFiles embed.FS

// languageCookie stores the language chosen with GET /language/{code}.
const (
	languageCookie       = "indexarr_lang"
	languageCookieMaxAge = 365 * 24 * 60 * 60
)

// templateSet holds the templates compiled for one language. The T function is
// bound to the language at parse time, so {{T "key"}} resolves without any
// per-execution plumbing.
type templateSet struct {
	pages    map[string]*template.Template
	partials map[string]*template.Template
}

var templateSets = map[i18n.Language]*templateSet{}

var pageNames = []string{
	"dashboard", "releases", "release", "source_form", "source_bulk",
	"run", "rules", "settings", "maintenance", "discover", "error",
	"definitions", "definition", "definition_form", "indexers", "indexer", "indexer_form",
	"applications", "application_form", "import", "activity",
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
	"stateClass": func(state string) string {
		switch state {
		case "verificata", "stabile":
			return "ok"
		case "degradata":
			return "danger"
		case "bozza":
			return "warn"
		default:
			return ""
		}
	},
	"duration": func(value time.Duration) string {
		return value.Round(time.Millisecond).String()
	},
}

// init compiles one template set per language. The T function is bound to the
// catalog so no runtime lookup or context plumbing is needed while executing.
func init() {
	for _, lang := range i18n.Supported() {
		funcs := make(template.FuncMap, len(templateFuncMap)+4)
		for name, fn := range templateFuncMap {
			funcs[name] = fn
		}
		funcs["T"] = func(key string, args ...any) string {
			return i18n.T(lang, key, args...)
		}
		funcs["stateLabel"] = func(state string) string {
			return stateLabel(lang, state)
		}
		funcs["statusLabel"] = func(status string) string {
			return statusLabel(lang, status)
		}
		funcs["syncLevelLabel"] = func(level string) string {
			return syncLevelLabel(lang, level)
		}
		funcs["ago"] = func(value time.Time) string {
			return humanAgoLang(lang, value)
		}

		set := &templateSet{
			pages:    map[string]*template.Template{},
			partials: map[string]*template.Template{},
		}
		for _, name := range pageNames {
			files := []string{"templates/layout.html", "templates/" + name + ".html"}
			switch name {
			case "releases":
				files = append(files, "templates/releases_results.html")
			case "indexers":
				// The unified page embeds the legacy sources panel.
				files = append(files, "templates/sources.html")
			case "activity":
				// Activity embeds the Run and Log panels.
				files = append(files, "templates/runs.html", "templates/logs.html")
			}
			tmpl := template.New("layout.html").Funcs(funcs)
			tmpl = template.Must(tmpl.ParseFS(templateFiles, files...))
			set.pages[name] = tmpl
		}
		set.partials["releases_results"] = template.Must(
			template.New("releases_results").Funcs(funcs).
				ParseFS(templateFiles, "templates/releases_results.html"))
		templateSets[lang] = set
	}
}

// languageOption is one entry of the language selector.
type languageOption struct {
	Code   string
	Name   string
	Active bool
}

// base is the data every page shares with the layout.
type base struct {
	Title        string
	Section      string
	Lang         string
	Languages    []languageOption
	Version      string
	AssetVersion string
	CSRF         string
	Message      string
	Error        string
	TorznabURL   string
	PublicURL    string
	PublicURLSet bool
	Uptime       string
	AutoRefresh  int
}

func requestLanguage(r *http.Request) i18n.Language {
	cookie := ""
	if value, err := r.Cookie(languageCookie); err == nil {
		cookie = value.Value
	}
	return i18n.Detect(r.Header.Get("Accept-Language"), cookie, r.URL.Query().Get("lang"))
}

func languageOptions(current i18n.Language) []languageOption {
	langs := i18n.Supported()
	out := make([]languageOption, 0, len(langs))
	for _, lang := range langs {
		out = append(out, languageOption{
			Code:   lang.HTML(),
			Name:   lang.Name(),
			Active: lang == current,
		})
	}
	return out
}

func stateLabel(lang i18n.Language, state string) string {
	switch state {
	case "bozza":
		return i18n.T(lang, "state.draft")
	case "verificata":
		return i18n.T(lang, "state.verified")
	case "stabile":
		return i18n.T(lang, "state.stable")
	case "degradata":
		return i18n.T(lang, "state.degraded")
	case "":
		return i18n.T(lang, "state.unknown")
	default:
		return state
	}
}

func statusLabel(lang i18n.Language, status string) string {
	switch status {
	case "ok":
		return i18n.T(lang, "common.ok")
	case "error":
		return i18n.T(lang, "common.error")
	case "running":
		return i18n.T(lang, "common.running")
	case "":
		return i18n.T(lang, "common.waiting")
	default:
		return status
	}
}

func syncLevelLabel(lang i18n.Language, level string) string {
	switch level {
	case "disabled", "add-remove", "full":
		return i18n.T(lang, "applications.level."+level)
	default:
		return level
	}
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
	// Native is the runtime manager of the native indexers. It is optional:
	// without it the pages still list and configure indexers but cannot run
	// tests or imports.
	Native *indexer.Manager
	// Indexers is the service behind the native indexer pages. It is optional
	// and the pages degrade with a clear message when it is nil.
	Indexers *indexersvc.Service
	// Admin is the versioned /api/v2 handler. It authenticates with the
	// Torznab API key itself, so it is mounted outside the UI session.
	Admin http.Handler
	// DownloadResolver fetches a payload that is not stored yet (an opaque
	// release_sources.download_ref). It returns body, content type and
	// filename. Optional: without it the proxy only serves stored payloads.
	DownloadResolver func(context.Context, int64) ([]byte, string, string, error)
	// Vault seals the API keys of the downstream applications. It is
	// optional: without it the application pages still list the configured
	// entries but cannot store a new key.
	Vault *vault.Vault
	// Sync reconciles the native indexers with the downstream applications.
	// It is optional: the pages degrade with a clear message when it is nil.
	Sync *syncjob.Service
	// GrabGuard checks the per-indexer daily grab limit before serving a
	// payload; GrabRecord records a successful grab. Both are optional.
	GrabGuard  func(context.Context, int64) error
	GrabRecord func(context.Context, int64, int64, string) error
	// Politeness is the shared per-host budget and metric source. It is
	// optional: without it the Activity page shows no network counters.
	Politeness *engine.Politeness
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
	native  *indexer.Manager
	svc     *indexersvc.Service
	vault   *vault.Vault
	sync    *syncjob.Service
	polite  *engine.Politeness
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
		native:  cfg.Native,
		svc:     cfg.Indexers,
		vault:   cfg.Vault,
		sync:    cfg.Sync,
		polite:  cfg.Politeness,
	}
	if cfg.Engine != nil {
		srv.torznab.SetLiveSearch(cfg.Engine.SearchLive)
	}
	srv.torznab.SetDownloadResolver(cfg.DownloadResolver)
	if cfg.GrabGuard != nil {
		srv.torznab.SetGrabGuard(cfg.GrabGuard, cfg.GrabRecord)
	}
	return srv
}

// Handler builds the complete route table.
func (s *Server) Handler() http.Handler {
	ui := http.NewServeMux()
	ui.HandleFunc("GET /{$}", s.dashboard)
	ui.HandleFunc("GET /language/{code}", s.setLanguage)
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
	ui.HandleFunc("GET /sources", s.sourcesRedirect)
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
	ui.HandleFunc("GET /definitions", s.definitionsPage)
	ui.HandleFunc("GET /definitions/new", s.definitionNew)
	ui.HandleFunc("POST /definitions", s.definitionCreate)
	ui.HandleFunc("GET /definitions/{id}", s.definitionPage)
	ui.HandleFunc("GET /indexers", s.indexersPage)
	ui.HandleFunc("GET /indexers/new", s.indexerNew)
	ui.HandleFunc("POST /indexers", s.indexerCreate)
	ui.HandleFunc("GET /indexers/{id}", s.indexerPage)
	ui.HandleFunc("GET /indexers/{id}/edit", s.indexerEdit)
	ui.HandleFunc("POST /indexers/{id}", s.indexerUpdate)
	ui.HandleFunc("POST /indexers/{id}/delete", s.indexerDelete)
	ui.HandleFunc("POST /indexers/{id}/test", s.indexerTest)
	ui.HandleFunc("POST /indexers/{id}/run", s.indexerRun)
	ui.HandleFunc("GET /applications", s.applicationsPage)
	ui.HandleFunc("GET /applications/new", s.applicationNew)
	ui.HandleFunc("POST /applications", s.applicationCreate)
	ui.HandleFunc("POST /applications/sync", s.applicationsSyncAll)
	ui.HandleFunc("GET /applications/{id}/edit", s.applicationEdit)
	ui.HandleFunc("POST /applications/{id}", s.applicationUpdate)
	ui.HandleFunc("POST /applications/{id}/delete", s.applicationDelete)
	ui.HandleFunc("POST /applications/{id}/sync", s.applicationSync)
	ui.HandleFunc("GET /import", s.importPage)
	ui.HandleFunc("POST /import/prowlarr", s.importProwlarr)
	ui.HandleFunc("POST /import/jackett", s.importJackett)
	ui.HandleFunc("GET /activity", s.activityPage)
	ui.HandleFunc("GET /runs", s.runsRedirect)
	ui.HandleFunc("GET /runs/{id}", s.runPage)
	ui.HandleFunc("POST /runs/clear", s.runsClear)
	ui.HandleFunc("GET /rules", s.rulesPage)
	ui.HandleFunc("POST /rules", s.rulesAdd)
	ui.HandleFunc("POST /rules/{id}/delete", s.rulesDelete)
	ui.HandleFunc("GET /logs", s.logsRedirect)
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
	outer.Handle("/api/v2.0/", s.torznab)
	outer.Handle("/newznab/api", s.torznab)
	outer.Handle("/torznab/api", s.torznab)
	outer.Handle("/torznab/", s.torznab)
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

// setLanguage stores the chosen language in a cookie and sends the browser
// back to the page it came from. Unknown codes redirect to the dashboard
// without touching the cookie.
func (s *Server) setLanguage(w http.ResponseWriter, r *http.Request) {
	lang, ok := i18n.Lookup(r.PathValue("code"))
	if !ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     languageCookie,
		Value:    string(lang),
		Path:     "/",
		MaxAge:   languageCookieMaxAge,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsHTTPS(r),
	})
	target := "/"
	if referer := strings.TrimSpace(r.Header.Get("Referer")); referer != "" {
		if parsed, err := url.Parse(referer); err == nil &&
			parsed.Host == r.Host &&
			(parsed.Scheme == "http" || parsed.Scheme == "https") &&
			!strings.HasPrefix(parsed.Path, "/language/") {
			target = parsed.RequestURI()
		}
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *Server) newBase(r *http.Request, title, section string) base {
	lang := requestLanguage(r)
	out := base{
		Title:        title,
		Section:      section,
		Lang:         lang.HTML(),
		Languages:    languageOptions(lang),
		Version:      s.version,
		AssetVersion: strconv.FormatInt(s.started.Unix(), 10),
		CSRF:         csrfToken(r.Context()),
		Message:      r.URL.Query().Get("msg"),
		Error:        r.URL.Query().Get("err"),
		Uptime:       humanAgoLang(lang, s.started),
	}
	settings, loaded := settingsFromContext(r.Context())
	if !loaded {
		settings, _ = s.store.LoadSettings(r.Context())
	}
	out.PublicURL = settings.PublicURL
	out.PublicURLSet = strings.TrimSpace(settings.PublicURL) != ""
	out.TorznabURL = torznabapi.BaseURL(r, settings) + "/api"
	return out
}

// newBaseT is newBase with a catalog key as the page title, so <title> follows
// the interface language instead of hardcoding an English label in a handler.
func (s *Server) newBaseT(r *http.Request, key, section string, args ...any) base {
	return s.newBase(r, i18n.T(requestLanguage(r), key, args...), section)
}

// translate exposes the request catalog to handlers for flash messages and
// validation problems. Backend errors still carry their own technical text.
func (s *Server) translate(r *http.Request, key string, args ...any) string {
	return i18n.T(requestLanguage(r), key, args...)
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, name string, data any, status int) {
	set, ok := templateSets[requestLanguage(r)]
	if !ok {
		set = templateSets[i18n.EN]
	}
	tmpl, ok := set.pages[name]
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
func (s *Server) renderPartial(w http.ResponseWriter, r *http.Request, name string, data any) {
	set, ok := templateSets[requestLanguage(r)]
	if !ok {
		set = templateSets[i18n.EN]
	}
	tmpl, ok := set.partials[name]
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

// redirectTab sends a permanent-style 302 to a tabbed page, keeping the query
// string (filters, flash messages) of the old URL.
func redirectTab(w http.ResponseWriter, r *http.Request, path, tab string) {
	values := r.URL.Query()
	values.Set("tab", tab)
	http.Redirect(w, r, path+"?"+values.Encode(), http.StatusFound)
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
	return humanAgoLang(i18n.EN, value)
}

// humanAgoLang renders the relative time used by every list. The labels come
// from the catalog so the page language is consistent with its numbers.
func humanAgoLang(lang i18n.Language, value time.Time) string {
	if value.IsZero() {
		return i18n.T(lang, "common.never")
	}
	elapsed := time.Since(value)
	if elapsed < 0 {
		elapsed = 0
	}
	switch {
	case elapsed < time.Minute:
		return i18n.T(lang, "time.justNow")
	case elapsed < time.Hour:
		return i18n.T(lang, "time.minutesAgo", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return i18n.T(lang, "time.hoursAgo", int(elapsed.Hours()))
	default:
		return i18n.T(lang, "time.daysAgo", int(elapsed.Hours()/24))
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
