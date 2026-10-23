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
	"sync"
	"time"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/categories"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/logbuffer"
	"github.com/buzzqw/indexarr/internal/native"
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

// templateCache compiles the page templates lazily, one language at a time.
// English is compiled at startup because it is the default and the fallback for
// every unknown tag; the other catalogs are parsed on first use and then
// reused. An installation that only uses one language therefore pays for one
// set of parse trees instead of seven (the parse trees used to be about 80% of
// the live heap).
type templateCache struct {
	// sets is written only by newTemplateCache and never modified afterwards,
	// so concurrent reads are safe without a lock.
	sets map[i18n.Language]*lazyTemplateSet
}

// lazyTemplateSet compiles one language at most once, even under concurrent
// first requests. Each language has its own Once, so requests in Italian and
// French do not serialise behind the same lock.
type lazyTemplateSet struct {
	lang i18n.Language
	once sync.Once
	set  *templateSet
}

// get returns the compiled set for lang, compiling it on first use. Unknown
// languages fall back to English, matching i18n.Detect.
func (l *lazyTemplateSet) get() *templateSet {
	l.once.Do(func() {
		l.set = compileTemplateSet(l.lang)
	})
	return l.set
}

func newTemplateCache() *templateCache {
	langs := i18n.Supported()
	cache := &templateCache{sets: make(map[i18n.Language]*lazyTemplateSet, len(langs))}
	for _, lang := range langs {
		cache.sets[lang] = &lazyTemplateSet{lang: lang}
	}
	// English is both the default and the fallback: compile it eagerly so the
	// first request never pays for the whole UI and every fallback is ready.
	cache.get(i18n.EN)
	return cache
}

func (c *templateCache) get(lang i18n.Language) *templateSet {
	if entry, ok := c.sets[lang]; ok {
		return entry.get()
	}
	return c.sets[i18n.EN].get()
}

var templateSets = newTemplateCache()

var pageNames = []string{
	"dashboard", "releases", "release",
	"run", "rules", "settings", "maintenance", "discover", "error",
	"definitions", "definition", "definition_form", "indexers", "indexer", "indexer_form",
	"applications", "application_form", "import", "activity", "setup",
	"download_client_form",
}

var templateFuncMap = template.FuncMap{
	"bytes":      humanBytes,
	"date":       humanDate,
	"dateOnly":   humanDateOnly,
	"ago":        humanAgo,
	"catName":    categories.Name,
	"catNameInt": func(id int) string { return categories.Name(strconv.Itoa(id)) },
	"clientKind": downloadClientKindLabel,
	"num": func(value int64) string {
		if value < 0 {
			return "?"
		}
		return strconv.FormatInt(value, 10)
	},
	"sizeOrDash": func(value int64) string {
		if value <= 0 {
			return "—"
		}
		return humanBytes(value)
	},
	"numOrDash": func(value int64) string {
		if value < 0 {
			return "—"
		}
		return strconv.FormatInt(value, 10)
	},
	"textOrDash": func(value string) string {
		if strings.TrimSpace(value) == "" {
			return "—"
		}
		return value
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
	// safeURL marks an already validated link scheme as trusted. Magnet links
	// are otherwise replaced with #ZgotmplZ by html/template.
	"safeURL": func(raw string) template.URL {
		lower := strings.ToLower(strings.TrimSpace(raw))
		for _, prefix := range []string{"http://", "https://", "magnet:?"} {
			if strings.HasPrefix(lower, prefix) {
				return template.URL(raw)
			}
		}
		return ""
	},
	"truncate": func(text string, max int) string {
		runes := []rune(text)
		if len(runes) <= max {
			return text
		}
		return string(runes[:max]) + "…"
	},
	// redactURL hides userinfo, the query and secret-like path segments of a
	// source URL. The legacy pages use it so an rsskey never reaches the HTML.
	"redactURL":      feed.RedactURL,
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

// compileTemplateSet parses the page and partial templates for one language.
// It is called at most once per language by lazyTemplateSet.get.
func compileTemplateSet(lang i18n.Language) *templateSet {
	funcs := make(template.FuncMap, len(templateFuncMap)+10)
	for name, fn := range templateFuncMap {
		funcs[name] = fn
	}
	funcs["T"] = func(key string, args ...any) string {
		return i18n.T(lang, key, args...)
	}
	funcs["stateLabel"] = func(state string) string {
		return stateLabel(lang, state)
	}
	funcs["phaseLabel"] = func(name string) string {
		return phaseLabel(lang, name)
	}
	funcs["kindLabel"] = func(kind string) string {
		switch strings.ToLower(strings.TrimSpace(kind)) {
		case "run":
			return i18n.T(lang, "idx.data.kind.run")
		case "search":
			return i18n.T(lang, "idx.data.kind.search")
		case "latest":
			return i18n.T(lang, "idx.data.kind.latest")
		case "test":
			return i18n.T(lang, "idx.data.kind.test")
		case "grab":
			return i18n.T(lang, "idx.data.kind.grab")
		case "health":
			return i18n.T(lang, "idx.data.kind.health")
		default:
			return kind
		}
	}
	funcs["phaseMessage"] = func(message string) string {
		switch strings.ToLower(strings.TrimSpace(message)) {
		case "not required":
			return i18n.T(lang, "idx.phase.notRequired")
		case "skipped":
			return i18n.T(lang, "idx.phase.skipped")
		default:
			return message
		}
	}
	funcs["advanced"] = advancedSection
	funcs["statusLabel"] = func(status string) string {
		return statusLabel(lang, status)
	}
	funcs["syncLevelLabel"] = func(level string) string {
		return syncLevelLabel(lang, level)
	}
	funcs["ago"] = func(value time.Time) string {
		return humanAgoLang(lang, value)
	}
	funcs["until"] = func(value time.Time) string {
		return humanUntilLang(lang, value)
	}

	set := &templateSet{
		pages:    map[string]*template.Template{},
		partials: map[string]*template.Template{},
	}
	for _, name := range pageNames {
		files := []string{"templates/layout.html", "templates/components.html", "templates/" + name + ".html"}
		switch name {
		case "releases":
			files = append(files, "templates/releases_results.html")
		case "activity":
			// Activity embeds the Run, Log, Feed and Searches panels.
			files = append(files, "templates/runs.html", "templates/logs.html",
				"templates/activity_feed.html", "templates/activity_searches.html")
		case "indexer":
			// The detail page embeds the Data tab and the Sonarr/Radarr try form.
			files = append(files, "templates/indexer_data.html", "templates/indexer_try.html")
		case "settings":
			// Settings → Advanced keeps the Definitions table.
			files = append(files, "templates/settings_definitions.html")
		}
		tmpl := template.New("layout.html").Funcs(funcs)
		tmpl = template.Must(tmpl.ParseFS(templateFiles, files...))
		set.pages[name] = tmpl
	}
	set.partials["releases_results"] = template.Must(
		template.New("releases_results").Funcs(funcs).
			ParseFS(templateFiles, "templates/releases_results.html"))
	return set
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
	LanguageName string
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
	// LiveRefresh is the polling interval for pages that must reload only
	// while the browser tab is visible. app.js checks document.visibilityState.
	LiveRefresh int
	// Nav badges come from HomeStatus: they only appear when something needs
	// attention, never as a plain counter.
	NavIndexerProblems int
	NavAppProblems     int
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

// phaseLabel names one test phase in the page language. Unknown future phases
// keep their technical name instead of disappearing.
func phaseLabel(lang i18n.Language, name string) string {
	key := "idx.phase." + strings.ToLower(strings.TrimSpace(name))
	label := i18n.T(lang, key)
	if label == key || strings.TrimSpace(label) == "" {
		return name
	}
	return label
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
	// Scheduler is the periodic runner. It powers "run all" and "test all".
	// Optional: the individual buttons still work through Native.
	Scheduler *native.Runner
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
	// VaultKey describes where the master key lives, without key material.
	// It is shown in Settings → Security.
	VaultKey vault.Info
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
	store    *store.Store
	log      *slog.Logger
	version  string
	started  time.Time
	torznab  *torznabapi.Server
	static   http.Handler
	logs     *logbuffer.Buffer
	level    *slog.LevelVar
	restart  func()
	admin    http.Handler
	native   *indexer.Manager
	sched    *native.Runner
	svc      *indexersvc.Service
	vault    *vault.Vault
	vaultKey vault.Info
	sync     *syncjob.Service
	polite   *engine.Politeness
	// importPlans holds short-lived previews between /import and /import/apply.
	// Secrets stay in this process memory and never reach the HTML.
	importPlans *importPlanStore
	// catalogIndex supplies the site catalog. It is catalog.Index in
	// production; tests replace it with a synthetic index to prove that the
	// add page never parses the embedded definitions.
	catalogIndex func() ([]catalog.IndexEntry, error)
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
	// Embedders and tests may pass a working vault without the key-location
	// metadata. In that case the vault is available, just without a path.
	if cfg.Vault != nil && cfg.VaultKey.Kind == "" {
		cfg.VaultKey.Available = true
		cfg.VaultKey.Kind = "config"
	}
	srv := &Server{
		store:        cfg.Store,
		log:          logger,
		version:      cfg.Version,
		started:      cfg.Started,
		torznab:      torznabapi.New(cfg.Store, logger, cfg.Version),
		static:       http.StripPrefix("/static/", http.FileServer(http.FS(sub))),
		logs:         logs,
		level:        level,
		restart:      cfg.Restart,
		admin:        cfg.Admin,
		native:       cfg.Native,
		sched:        cfg.Scheduler,
		svc:          cfg.Indexers,
		vault:        cfg.Vault,
		vaultKey:     cfg.VaultKey,
		sync:         cfg.Sync,
		polite:       cfg.Politeness,
		importPlans:  newImportPlanStore(),
		catalogIndex: catalog.Index,
	}
	if cfg.Native != nil {
		srv.torznab.SetLiveSearch(cfg.Native.SearchLive)
		srv.torznab.SetLatestIndexers(cfg.Native.LatestIndexerIDs)
		srv.torznab.SetCapabilities(cfg.Native.CapabilitiesFor)
	}
	srv.torznab.SetDownloadResolver(cfg.DownloadResolver)
	if cfg.GrabGuard != nil {
		srv.torznab.SetGrabGuard(cfg.GrabGuard, cfg.GrabRecord)
	}
	// Existing installations configured one client in Settings. Move it into
	// the new table on first start, sealing the password with the vault. If the
	// vault is unavailable the legacy fallback in resolveDownloadClient still
	// works, so this is best-effort and never blocks startup.
	srv.migrateLegacyDownloadClient()
	return srv
}

// migrateLegacyDownloadClient is additive: it does nothing when a client row
// already exists or when the old setting is empty.
func (s *Server) migrateLegacyDownloadClient() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clients, err := s.store.ListDownloadClients(ctx)
	if err != nil || len(clients) > 0 {
		return
	}
	settings, err := s.store.LoadSettings(ctx)
	if err != nil || settings.DownloadClient == "" || settings.DownloadClient == "none" {
		return
	}
	sealed := ""
	if settings.DownloadClientPassword != "" {
		sealed, err = s.sealSecret(settings.DownloadClientPassword)
		if err != nil {
			s.log.Warn("cannot import the legacy download client: secrets vault unavailable", "error", err)
			return
		}
	}
	_, err = s.store.CreateDownloadClient(ctx, store.DownloadClient{
		Name:        downloadClientKindLabel(settings.DownloadClient),
		Kind:        settings.DownloadClient,
		Protocol:    store.DefaultDownloadClientProtocol(settings.DownloadClient),
		URL:         settings.DownloadClientURL,
		Username:    settings.DownloadClientUser,
		PasswordEnc: sealed,
		Category:    settings.DownloadClientCategory,
		SavePath:    settings.DownloadClientSavePath,
		Paused:      settings.DownloadClientPaused,
		Enabled:     true,
		IsDefault:   true,
	})
	if err != nil {
		s.log.Warn("cannot import the legacy download client", "error", err)
		return
	}
	s.log.Info("legacy download client imported into App", "kind", settings.DownloadClient)
}

// Handler builds the complete route table.
func (s *Server) Handler() http.Handler {
	ui := http.NewServeMux()
	ui.HandleFunc("GET /{$}", s.dashboard)
	ui.HandleFunc("GET /setup", s.setupPage)
	ui.HandleFunc("POST /setup/address", s.setupAddress)
	ui.HandleFunc("POST /setup/skip", s.setupSkip)
	ui.HandleFunc("POST /setup/finish", s.setupFinish)
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
	// The legacy sources pages are gone. Old bookmarks and links keep working:
	// every /sources path redirects to the unified indexers page.
	ui.HandleFunc("/sources", s.sourcesRedirect)
	ui.HandleFunc("/sources/", s.sourcesRedirect)
	ui.HandleFunc("GET /definitions", s.definitionsRedirect)
	ui.HandleFunc("GET /definitions/new", s.definitionNew)
	ui.HandleFunc("POST /definitions", s.definitionCreate)
	ui.HandleFunc("GET /definitions/{id}", s.definitionPage)
	ui.HandleFunc("GET /indexers", s.indexersUnifiedPage)
	ui.HandleFunc("GET /indexers/new", s.indexerUnifiedNew)
	ui.HandleFunc("POST /indexers", s.indexerUnifiedCreate)
	ui.HandleFunc("POST /indexers/test-all", s.indexersTestAll)
	ui.HandleFunc("POST /indexers/run-all", s.indexersRunAll)
	ui.HandleFunc("POST /indexers/bulk", s.indexersBulk)
	ui.HandleFunc("POST /indexers/catalog/add", s.indexerCatalogAdd)
	ui.HandleFunc("GET /indexers/export", s.indexersExport)
	ui.HandleFunc("POST /indexers/import", s.indexersImport)
	ui.HandleFunc("GET /indexers/discover", s.indexersDiscover)
	ui.HandleFunc("POST /indexers/quick", s.indexerQuickAdd)
	ui.HandleFunc("GET /indexers/{id}", s.indexerUnifiedDetail)
	ui.HandleFunc("GET /indexers/{id}/edit", s.indexerUnifiedEdit)
	ui.HandleFunc("POST /indexers/{id}", s.indexerUnifiedUpdate)
	ui.HandleFunc("POST /indexers/{id}/delete", s.indexerUnifiedDelete)
	ui.HandleFunc("POST /indexers/{id}/toggle", s.indexerToggle)
	ui.HandleFunc("POST /indexers/{id}/duplicate", s.indexerDuplicate)
	ui.HandleFunc("POST /indexers/{id}/test", s.indexerUnifiedTest)
	ui.HandleFunc("POST /indexers/{id}/run", s.indexerUnifiedRun)
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
	ui.HandleFunc("POST /import/apply", s.importApply)
	ui.HandleFunc("GET /activity", s.activityPage)
	ui.HandleFunc("GET /runs", s.runsRedirect)
	ui.HandleFunc("GET /runs/{id}", s.runPage)
	ui.HandleFunc("POST /runs/clear", s.runsClear)
	ui.HandleFunc("GET /rules", s.rulesRedirect)
	ui.HandleFunc("POST /rules", s.rulesAdd)
	ui.HandleFunc("POST /rules/{id}/delete", s.rulesDelete)
	ui.HandleFunc("GET /logs", s.logsRedirect)
	ui.HandleFunc("GET /logs/download", s.logsDownload)
	ui.HandleFunc("POST /logs/clear", s.logsClear)
	ui.HandleFunc("POST /logs/level", s.logsSetLevel)
	ui.HandleFunc("GET /settings", s.settingsPage)
	ui.HandleFunc("GET /settings/secrets/{name}", s.settingsSecret)
	ui.HandleFunc("GET /settings/secrets/key/{id}", s.settingsSecret)
	ui.HandleFunc("POST /settings", s.settingsSave)
	ui.HandleFunc("POST /settings/apikey", s.settingsRegenerateKey)
	ui.HandleFunc("POST /settings/apikeys", s.settingsAPIKeyAdd)
	ui.HandleFunc("POST /settings/apikeys/{id}/delete", s.settingsAPIKeyDelete)
	ui.HandleFunc("POST /settings/flaresolverr/test", s.settingsTestFlareSolverr)
	ui.HandleFunc("POST /settings/public-url", s.settingsPublicURL)
	ui.HandleFunc("POST /settings/setup/reopen", s.setupReopen)
	ui.HandleFunc("GET /download-clients", s.downloadClientsRedirect)
	ui.HandleFunc("GET /download-clients/new", s.downloadClientNew)
	ui.HandleFunc("POST /download-clients", s.downloadClientCreate)
	ui.HandleFunc("GET /download-clients/{id}/edit", s.downloadClientEdit)
	ui.HandleFunc("POST /download-clients/{id}", s.downloadClientUpdate)
	ui.HandleFunc("POST /download-clients/{id}/delete", s.downloadClientDelete)
	ui.HandleFunc("POST /download-clients/{id}/test", s.downloadClientTest)
	ui.HandleFunc("POST /download-clients/{id}/default", s.downloadClientDefault)
	ui.HandleFunc("GET /maintenance", s.maintenanceRedirect)
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
		LanguageName: lang.Name(),
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
	// One status source for the page and the sidebar badge.
	status := s.HomeStatus(r.Context())
	if status.Level != "danger" {
		out.NavIndexerProblems = status.ProblemIndexers
		out.NavAppProblems = status.ProblemApps
	}
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
	set := templateSets.get(requestLanguage(r))
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
	set := templateSets.get(requestLanguage(r))
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
		if strings.Contains(target, "?") {
			target += "&" + encoded
		} else {
			target += "?" + encoded
		}
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
	fmt.Fprintf(w, `{"status":"ok","version":%q,"uptime_seconds":%d,"releases":%d,"indexers":%d,"db_bytes":%d}`,
		s.version, int64(time.Since(s.started).Seconds()), stats.Releases, stats.Indexers, stats.DBBytes)
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

// humanUntilLang renders a future instant ("in 56 min") for the next update.
func humanUntilLang(lang i18n.Language, value time.Time) string {
	if value.IsZero() {
		return i18n.T(lang, "common.never")
	}
	remaining := time.Until(value)
	if remaining <= 0 {
		return i18n.T(lang, "time.justNow")
	}
	switch {
	case remaining < time.Minute:
		return i18n.T(lang, "time.justNow")
	case remaining < time.Hour:
		return i18n.T(lang, "time.inMinutes", int(remaining.Minutes()))
	case remaining < 24*time.Hour:
		return i18n.T(lang, "time.inHours", int(remaining.Hours()))
	default:
		return i18n.T(lang, "time.inDays", int(remaining.Hours()/24))
	}
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
