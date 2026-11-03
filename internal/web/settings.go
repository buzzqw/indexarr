package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/backup"
	"github.com/buzzqw/indexarr/internal/flaresolverr"
	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/torznabapi"
	"github.com/buzzqw/indexarr/internal/vault"
)

type settingsAPIKey struct {
	ID         int64
	Name       string
	Masked     string
	CreatedAt  time.Time
	LastUsedAt time.Time
	Requests   int64
}

type settingsSection struct {
	ID     string
	Label  string
	URL    string
	Active bool
}

// settingsLanguageOption is one entry of the message-language select. An
// empty Code is the "same as the interface" choice.
type settingsLanguageOption struct {
	Code     string
	Name     string
	Selected bool
}

type settingsPage struct {
	base
	Section       string
	Sections      []settingsSection
	Settings      store.Settings
	FieldErrors   map[string]string
	MaxTorrentMB  int64
	TorznabBase   string
	TorznabMasked string
	// The notification secrets never reach the HTML: only whether one is
	// stored, so the field can show a masked placeholder.
	WebhookStored       bool
	TelegramTokenStored bool
	AppriseKeyStored    bool
	DiscordURLStored    bool
	SlackURLStored      bool
	GotifyTokenStored   bool
	NtfyTokenStored     bool
	PushoverTokenStored bool
	PushoverUserStored  bool
	SMTPPasswordStored  bool
	// NotifyLanguages are the choices of the message language: an empty code
	// means "same as the interface", whose name is NotifyInterfaceName.
	NotifyLanguages     []settingsLanguageOption
	NotifyInterfaceName string
	AdditionalKeys      []settingsAPIKey
	BackupDir           string
	VaultKey            vault.Info
	LogLevel            string
	// Rules
	Rules []store.BlocklistRule
	// Maintenance and backups
	Stats               store.Stats
	Growth              store.DatabaseGrowth
	Report              store.DBReport
	DBPath              string
	SQLiteVersion       string
	RetentionDays       int
	DefaultPurgeDays    int
	Backups             []backup.Info
	BackupEnabled       bool
	BackupIntervalHours int
	BackupKeep          int
	Indexers            []store.Indexer
	UsefulSources       []usefulSourceView
	UsefulTotal         int64
	UsefulDays          int
	// Advanced
	Definitions   []store.Definition
	CatalogRows   []definitionRow
	LocalRows     []definitionRow
	StoredSources int
}

var settingsSections = []settingsSection{
	{ID: "general", Label: "settings.section.general"},
	{ID: "notifications", Label: "settings.section.notifications"},
	{ID: "security", Label: "settings.section.security"},
	{ID: "network", Label: "settings.section.network"},
	{ID: "updates", Label: "settings.section.updates"},
	{ID: "rules", Label: "settings.section.rules"},
	{ID: "backup", Label: "settings.section.backup"},
	{ID: "maintenance", Label: "settings.section.maintenance"},
	{ID: "advanced", Label: "settings.section.advanced"},
}

func settingsSectionID(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	for _, section := range settingsSections {
		if section.ID == raw {
			return raw
		}
	}
	return "general"
}

// settingsData builds the page model without the API key: the key is only
// available through /settings/secrets/* after an explicit fetch. The masked
// URL contains no secret either.
func (s *Server) settingsData(r *http.Request, settings store.Settings, section string) settingsPage {
	ctx := r.Context()
	keys, _ := s.store.ListAPIKeys(ctx)
	views := make([]settingsAPIKey, 0, len(keys))
	for _, key := range keys {
		views = append(views, settingsAPIKey{
			ID: key.ID, Name: key.Name, Masked: "••••••••",
			CreatedAt: key.CreatedAt, LastUsedAt: key.LastUsedAt, Requests: key.Requests,
		})
	}
	baseURL := torznabapi.BaseURL(r, settings) + "/api"
	public := settings
	// Secrets never travel in the initial HTML. The API key, Telegram token
	// and download-client password are only submitted when the operator enters
	// a new value; an empty field leaves the stored one untouched.
	public.APIKey = ""
	public.NotifyWebhookURL = ""
	public.NotifyTelegramToken = ""
	public.NotifyAppriseKey = ""
	public.NotifyDiscordURL = ""
	public.NotifySlackURL = ""
	public.NotifyGotifyToken = ""
	public.NotifyNtfyToken = ""
	public.NotifyPushoverToken = ""
	public.NotifyPushoverUser = ""
	public.NotifySMTPPassword = ""
	public.DownloadClientPassword = ""
	section = settingsSectionID(section)
	sections := make([]settingsSection, 0, len(settingsSections))
	for _, item := range settingsSections {
		item.URL = "/settings?section=" + item.ID
		item.Active = item.ID == section
		sections = append(sections, item)
	}
	data := settingsPage{
		base:                s.newBaseT(r, "shell.settings", "settings"),
		Section:             section,
		Sections:            sections,
		Settings:            public,
		FieldErrors:         map[string]string{},
		MaxTorrentMB:        settings.MaxTorrentBytes / (1 << 20),
		TorznabBase:         baseURL,
		TorznabMasked:       baseURL + "?apikey=••••••",
		WebhookStored:       strings.TrimSpace(settings.NotifyWebhookURL) != "",
		TelegramTokenStored: strings.TrimSpace(settings.NotifyTelegramToken) != "",
		AppriseKeyStored:    strings.TrimSpace(settings.NotifyAppriseKey) != "",
		DiscordURLStored:    strings.TrimSpace(settings.NotifyDiscordURL) != "",
		SlackURLStored:      strings.TrimSpace(settings.NotifySlackURL) != "",
		GotifyTokenStored:   strings.TrimSpace(settings.NotifyGotifyToken) != "",
		NtfyTokenStored:     strings.TrimSpace(settings.NotifyNtfyToken) != "",
		PushoverTokenStored: strings.TrimSpace(settings.NotifyPushoverToken) != "",
		PushoverUserStored:  strings.TrimSpace(settings.NotifyPushoverUser) != "",
		SMTPPasswordStored:  strings.TrimSpace(settings.NotifySMTPPassword) != "",
		NotifyLanguages:     notifyLanguageOptions(settings),
		NotifyInterfaceName: interfaceLanguageForNotifications(r, settings).Name(),
		AdditionalKeys:      views,
		BackupDir:           backup.Dir(s.store.Path()),
		VaultKey:            s.vaultKey,
		LogLevel:            s.levelName(),
		Definitions:         nil,
	}
	if section == "rules" {
		data.Rules, _ = s.store.ListBlocklist(ctx)
	}
	if section == "backup" || section == "maintenance" {
		data.Stats, _ = s.store.CachedStats(ctx)
		data.Growth, _ = s.store.Growth(ctx, time.Now())
		data.Report, _ = s.store.LoadDBReport(ctx)
		data.DBPath = s.store.Path()
		_ = s.store.DB().QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&data.SQLiteVersion)
		data.Backups, _ = backup.List(s.store.Path())
		data.Indexers, _ = s.store.ListIndexers(ctx)
		data.RetentionDays = settings.RetentionDays
		data.DefaultPurgeDays = settings.RetentionDays
		if data.DefaultPurgeDays <= 0 {
			data.DefaultPurgeDays = 30
		}
		data.BackupEnabled = settings.BackupEnabled
		data.BackupIntervalHours = settings.BackupIntervalHours
		data.BackupKeep = settings.BackupKeep
		data.UsefulDays = 30
		data.UsefulSources, data.UsefulTotal = s.usefulSources(r, data.UsefulDays)
	}
	if section == "advanced" {
		data.Definitions, _ = s.store.ListDefinitions(ctx)
		data.CatalogRows, data.LocalRows = s.definitionRows(ctx, nil)
		data.StoredSources, _ = s.store.StoredSourceCount(ctx)
	}
	return data
}

// levelName exposes the runtime log level without importing slog in templates.
func (s *Server) levelName() string {
	if s.level == nil {
		return "info"
	}
	switch s.level.Level() {
	case slog.LevelDebug:
		return "debug"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	default:
		return "info"
	}
}

// notifyLanguageOptions builds the message-language select: the "same as the
// interface" entry first, then every interface catalog by its endonym.
func notifyLanguageOptions(settings store.Settings) []settingsLanguageOption {
	current := strings.ToLower(strings.TrimSpace(settings.NotifyLanguage))
	options := make([]settingsLanguageOption, 0, len(i18n.Supported())+1)
	options = append(options, settingsLanguageOption{Code: "", Selected: current == ""})
	for _, lang := range i18n.Supported() {
		options = append(options, settingsLanguageOption{
			Code:     lang.HTML(),
			Name:     lang.Name(),
			Selected: string(lang) == current,
		})
	}
	return options
}

// interfaceLanguageForNotifications is the language "same as the interface"
// means for the background jobs: the language this browser chose with the
// header selector, or the saved one when the browser never chose. Reading the
// cookie matters on the first page after a mismatch: rememberInterfaceLanguage
// heals the stored setting during the render, but the page data was calculated
// from the value loaded before that write.
func interfaceLanguageForNotifications(r *http.Request, settings store.Settings) i18n.Language {
	if cookie, err := r.Cookie(languageCookie); err == nil {
		if raw := strings.TrimSpace(cookie.Value); raw != "" {
			if lang, ok := i18n.Lookup(raw); ok {
				return lang
			}
		}
	}
	return i18n.Normalize(settings.UILanguage)
}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	section := settingsSectionID(r.URL.Query().Get("section"))
	s.render(w, r, "settings", s.settingsData(r, settings, section), http.StatusOK)
}

// settingsPublicURL accepts the address proposed from r.Host. It is a POST so
// a page view never changes the configuration.
func (s *Server) settingsPublicURL(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimRight(strings.TrimSpace(r.FormValue("public_url")), "/")
	if raw == "" {
		raw = suggestedPublicURL(r)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		redirect(w, r, "/", "", s.translate(r, "settings.error.publicURL"))
		return
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		redirect(w, r, "/", "", err.Error())
		return
	}
	settings.PublicURL = raw
	if err := s.store.SaveSettings(r.Context(), settings); err != nil {
		redirect(w, r, "/", "", err.Error())
		return
	}
	redirect(w, r, "/", s.translate(r, "settings.publicURLSaved"), "")
}

// definitionsRedirect folds the old top-level tool into Settings → Advanced.
// The old Definitions, Rules and Maintenance pages live in Settings now. The
// redirects keep the query, so a message passed to the old address still shows.
func (s *Server) definitionsRedirect(w http.ResponseWriter, r *http.Request) {
	settingsSectionRedirect(w, r, "advanced", "#definitions")
}

func (s *Server) rulesRedirect(w http.ResponseWriter, r *http.Request) {
	settingsSectionRedirect(w, r, "rules", "")
}

func (s *Server) maintenanceRedirect(w http.ResponseWriter, r *http.Request) {
	settingsSectionRedirect(w, r, "maintenance", "")
}

func settingsSectionRedirect(w http.ResponseWriter, r *http.Request, section, fragment string) {
	values := r.URL.Query()
	values.Set("section", section)
	http.Redirect(w, r, "/settings?"+values.Encode()+fragment, http.StatusFound)
}

// normalizeHTTPURL accepts a full http(s) URL or a bare host:port, which is
// what people type for a LAN service such as FlareSolverr. It returns the
// cleaned value and false when the input is not a usable absolute URL.
func normalizeHTTPURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	if strings.ContainsAny(raw, " \t\r\n") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", false
	}
	return strings.TrimRight(raw, "/"), true
}

// renderSettingsInvalid re-renders the section that failed, keeping the values
// the operator typed. A field key puts the same message next to the input.
func (s *Server) renderSettingsInvalid(w http.ResponseWriter, r *http.Request, settings store.Settings, section, field, message string) {
	page := s.settingsData(r, settings, section)
	page.Error = message
	if field != "" {
		page.FieldErrors[field] = message
	}
	s.render(w, r, "settings", page, http.StatusBadRequest)
}

func (s *Server) settingsRegenerateKey(w http.ResponseWriter, r *http.Request) {
	key, err := store.NewAPIKey()
	if err != nil {
		redirect(w, r, "/settings", "", err.Error())
		return
	}
	if err := s.store.SetSetting(r.Context(), store.KeyAPIKey, key); err != nil {
		redirect(w, r, "/settings", "", err.Error())
		return
	}
	redirect(w, r, "/settings", s.translate(r, "settings.keyRegenerated"), "")
}

func (s *Server) settingsAPIKeyAdd(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	key, err := s.store.AddAPIKey(r.Context(), name)
	if err != nil {
		redirect(w, r, "/settings", "", err.Error())
		return
	}
	s.log.Info("additional API key created", "name", key.Name)
	redirect(w, r, "/settings", s.translate(r, "settings.keyCreated", key.Name), "")
}

func (s *Server) settingsAPIKeyDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/settings", "", s.translate(r, "settings.error.invalidKeyID"))
		return
	}
	if _, err := s.store.DeleteAPIKey(r.Context(), id); err != nil {
		redirect(w, r, "/settings", "", err.Error())
		return
	}
	redirect(w, r, "/settings", s.translate(r, "settings.keyRevoked"), "")
}

func (s *Server) settingsTestFlareSolverr(w http.ResponseWriter, r *http.Request) {
	current, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	in := current
	raw := strings.TrimSpace(r.FormValue("flaresolverr_url"))
	if raw == "" {
		in.FlareSolverrURL = ""
		s.renderSettingsInvalid(w, r, in, "network", "flaresolverr_url", s.translate(r, "settings.error.flaresolverrEmpty"))
		return
	}
	endpoint, ok := normalizeHTTPURL(raw)
	if !ok {
		in.FlareSolverrURL = raw
		s.renderSettingsInvalid(w, r, in, "network", "flaresolverr_url", s.translate(r, "settings.error.flaresolverrInvalid"))
		return
	}
	in.FlareSolverrURL = endpoint
	timeout := flaresolverr.DefaultTimeout
	if value, parseErr := strconv.Atoi(strings.TrimSpace(r.FormValue("flaresolverr_timeout_seconds"))); parseErr == nil && value >= 5 && value <= 300 {
		timeout = time.Duration(value) * time.Second
		in.FlareSolverrTimeoutSeconds = value
	}
	// The test shares the network form: keep the other fields the operator
	// typed instead of silently restoring the stored values after the redirect.
	if value, parseErr := s.formFloat(r, "host_rate", current.HostRate, 0, 100); parseErr == nil {
		in.HostRate = value
	}
	if value, parseErr := s.formInt(r, "host_burst", current.HostBurst, 1, 100); parseErr == nil {
		in.HostBurst = value
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), timeout+40*time.Second)
	defer cancel()
	client := &http.Client{Timeout: timeout + 40*time.Second}
	body, status, err := flaresolverr.Get(ctx, client, endpoint, "https://example.com/", timeout)
	if err != nil {
		s.log.Warn("FlareSolverr test failed", "component", "indexarr", "subsystem", "flaresolverr", "endpoint", endpoint, "error", err)
		s.renderSettingsInvalid(w, r, in, "network", "flaresolverr_url", s.translate(r, "settings.flaresolverrTestFailed", err.Error()))
		return
	}
	if status != http.StatusOK {
		s.log.Warn("FlareSolverr test returned an HTTP error", "component", "indexarr", "subsystem", "flaresolverr", "endpoint", endpoint, "status", status)
		s.renderSettingsInvalid(w, r, in, "network", "flaresolverr_url", s.translate(r, "settings.flaresolverrHTTP", status))
		return
	}
	// A successful test saves the address it just verified. The operator never
	// has to remember a second Save after an explicit Test click, and pressing
	// Enter in the field follows the normal Save path instead.
	if err := s.store.SaveSettings(r.Context(), in); err != nil {
		s.renderSettingsInvalid(w, r, in, "network", "flaresolverr_url", err.Error())
		return
	}
	if s.native != nil {
		s.native.InvalidateAll()
	}
	s.log.Info("FlareSolverr test ok", "endpoint", endpoint, "bytes", len(body))
	redirect(w, r, "/settings?section=network", s.translate(r, "settings.flaresolverrOK", len(body)), "")
}

// settingsTestNotification sends one real message to the configured channels,
// bypassing the switches and the anti-spam window, and reports the outcome on
// the page. The delivery error is scrubbed by the notify package, so a token
// can never appear in the flash message or in the log.
func (s *Server) settingsTestNotification(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 20*time.Second)
	defer cancel()
	if err := s.notifier.SendTest(ctx, settings); err != nil {
		s.log.Warn("test notification failed", "subsystem", "notify", "error", err)
		redirect(w, r, "/settings?section=notifications", "",
			s.translate(r, "settings.testNotificationFailed", err.Error()))
		return
	}
	s.log.Info("test notification sent", "subsystem", "notify")
	redirect(w, r, "/settings?section=notifications", s.translate(r, "settings.testNotificationOK"), "")
}

// validEmailList accepts one address or a comma/semicolon separated list.
func validEmailList(raw string) error {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' })
	found := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, err := mail.ParseAddress(part); err != nil {
			return err
		}
		found = true
	}
	if !found {
		return fmt.Errorf("empty address list")
	}
	return nil
}

func (s *Server) formInt(r *http.Request, key string, fallback, min, max int) (int, error) {
	raw := strings.TrimSpace(r.FormValue(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s", s.translate(r, "settings.error.number", key))
	}
	if value < min || value > max {
		return 0, fmt.Errorf("%s", s.translate(r, "settings.error.intRange", key, min, max))
	}
	return value, nil
}

func (s *Server) formFloat(r *http.Request, key string, fallback, min, max float64) (float64, error) {
	raw := strings.TrimSpace(r.FormValue(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("%s", s.translate(r, "settings.error.number", key))
	}
	if value < min || value > max {
		return 0, fmt.Errorf("%s", s.translate(r, "settings.error.floatRange", key, min, max))
	}
	return value, nil
}

type runsPage struct {
	base
	Runs       []store.RunHistory
	Indexers   []store.Indexer
	IndexerID  int64
	OnlyErrors bool
}

// runsRedirect keeps /runs working and folds the old page into Attività.
func (s *Server) runsRedirect(w http.ResponseWriter, r *http.Request) {
	redirectTab(w, r, "/activity", "runs")
}

func (s *Server) runsData(r *http.Request) (runsPage, error) {
	indexerID := int64Param(r.URL.Query().Get("indexer"), 0)
	if indexerID == 0 {
		indexerID = int64Param(r.URL.Query().Get("source"), 0)
	}
	onlyErrors := r.URL.Query().Get("errors") == "1"
	runs, err := s.store.ListRunHistory(r.Context(), indexerID, onlyErrors, 200)
	if err != nil {
		return runsPage{}, err
	}
	indexers, _ := s.store.ListIndexers(r.Context())
	return runsPage{
		base:       s.newBase(r, "Run", "activity"),
		Runs:       runs,
		Indexers:   indexers,
		IndexerID:  indexerID,
		OnlyErrors: onlyErrors,
	}, nil
}

func (s *Server) runsClear(w http.ResponseWriter, r *http.Request) {
	removed, err := s.store.ClearRunHistory(r.Context(), time.Now().AddDate(0, 0, -30))
	if err != nil {
		redirect(w, r, "/runs", "", err.Error())
		return
	}
	redirect(w, r, "/runs", fmt.Sprintf("%d run(s) older than 30 days removed", removed), "")
}

// usefulSourceView is one bar of the "most useful sources" panel: a source
// name, its kind (feed, web, indexer), the downloads it served and the share
// of the total.
type usefulSourceView struct {
	Name         string
	DefinitionID string
	TypeLabel    string
	Downloads    int64
	Share        int
}

func (s *Server) usefulSources(r *http.Request, days int) ([]usefulSourceView, int64) {
	if days <= 0 {
		days = 30
	}
	stats, err := s.store.SourceDownloadStats(r.Context(), time.Now().AddDate(0, 0, -days))
	if err != nil {
		s.log.Warn("maintenance: source download stats", "error", err)
		return nil, 0
	}
	var total int64
	for _, item := range stats {
		total += item.Downloads
	}
	out := make([]usefulSourceView, 0, len(stats))
	for _, item := range stats {
		share := 0
		if total > 0 {
			share = int(math.Round(float64(item.Downloads) * 100 / float64(total)))
		}
		if share < 1 && item.Downloads > 0 {
			share = 1
		}
		out = append(out, usefulSourceView{
			Name:         item.Name,
			DefinitionID: item.DefinitionID,
			TypeLabel:    s.usefulSourceType(r, item.DefinitionID),
			Downloads:    item.Downloads,
			Share:        share,
		})
	}
	return out, total
}

func (s *Server) usefulSourceType(r *http.Request, definitionID string) string {
	id := strings.ToLower(strings.TrimSpace(definitionID))
	switch {
	case id == "rss" || id == "feed" || strings.Contains(id, "rss"):
		return s.translate(r, "maintenance.usefulFeed")
	case strings.Contains(id, "torznab") || strings.Contains(id, "newznab"):
		return s.translate(r, "maintenance.usefulIndexer")
	default:
		return s.translate(r, "maintenance.usefulWeb")
	}
}

func (s *Server) maintenanceAction(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimSpace(r.FormValue("action"))
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	switch action {
	case "vacuum":
		if err := s.store.Vacuum(ctx); err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", s.translate(r, "maintenance.vacuumed"), "")
	case "integrity":
		result, err := s.store.IntegrityCheck(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", s.translate(r, "maintenance.integrity", result), "")
	case "quick_check":
		result, err := s.store.QuickCheck(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", s.translate(r, "maintenance.quick", result), "")
	case "foreign_keys":
		violations, err := s.store.ForeignKeyCheck(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		message := s.translate(r, "maintenance.foreignOK")
		if violations > 0 {
			message = s.translate(r, "maintenance.foreignBad", violations)
		}
		redirect(w, r, "/settings?section=maintenance", message, "")
	case "optimize":
		if err := s.store.Optimize(ctx); err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", s.translate(r, "maintenance.optimized"), "")
	case "checkpoint":
		busy, logFrames, checkpointed, err := s.store.Checkpoint(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		message := s.translate(r, "maintenance.checkpoint", checkpointed)
		if busy != 0 {
			message = s.translate(r, "maintenance.checkpointPending",
				logFrames-checkpointed, logFrames)
		}
		redirect(w, r, "/settings?section=maintenance", message, "")
	case "analyze":
		if err := s.store.Analyze(ctx); err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", s.translate(r, "maintenance.analyzed"), "")
	case "reindex":
		if err := s.store.Reindex(ctx); err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", s.translate(r, "maintenance.reindexed"), "")
	case "reset_errors":
		removed, err := s.store.ResetIndexerErrors(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", s.translate(r, "maintenance.errorsCleared", removed), "")
	case "prune":
		settings, err := s.store.LoadSettings(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		if settings.RetentionDays <= 0 {
			redirect(w, r, "/settings?section=maintenance", "", s.translate(r, "maintenance.needRetention"))
			return
		}
		before := time.Now().AddDate(0, 0, -settings.RetentionDays)
		removed, err := s.store.PruneReleases(ctx, before)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", s.translate(r, "maintenance.pruned", removed, settings.RetentionDays), "")
	case "clear_torrents":
		removed, err := s.store.ClearTorrents(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", s.translate(r, "maintenance.torrentsCleared", removed), "")
	case "dedupe":
		stats, err := s.store.MergeDuplicates(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", fmt.Sprintf(
			"%d duplicate release(s) merged (%d by hash, %d by title and size); %d rows remain",
			stats.Removed, stats.HashMerged, stats.TitleMerged, stats.After), "")
	case "clear_runs":
		removed, err := s.store.ClearRunHistory(ctx, time.Now().AddDate(0, 0, -30))
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", s.translate(r, "maintenance.runsCleared", removed), "")
	case "purge_days":
		days, err := intInRange(r.FormValue("days"), 30, 36500)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		removed, err := s.store.PruneReleases(ctx, time.Now().AddDate(0, 0, -days))
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance",
			s.translate(r, "maintenance.purgedDays", removed, days), "")
	case "purge_indexer":
		indexerID, err := strconv.ParseInt(strings.TrimSpace(r.FormValue("indexer_id")), 10, 64)
		if err != nil || indexerID <= 0 {
			redirect(w, r, "/settings?section=maintenance", "", s.translate(r, "maintenance.chooseIndexer"))
			return
		}
		indexerRow, err := s.store.GetIndexer(ctx, indexerID)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", s.translate(r, "maintenance.chooseIndexer"))
			return
		}
		removed, err := s.store.DeleteReleasesByIndexer(ctx, indexerID)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance",
			s.translate(r, "maintenance.purgedIndexer", removed, indexerRow.Name), "")
	case "purge_no_links":
		removed, err := s.store.DeleteReleasesWithoutLinks(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance",
			s.translate(r, "maintenance.purgedNoLinks", removed), "")
	case "purge_hidden":
		removed, err := s.store.DeleteHiddenReleases(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", s.translate(r, "maintenance.purgedHidden", removed), "")
	case "clear_runs_days":
		days, err := intInRange(r.FormValue("days"), 30, 36500)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		removed, err := s.store.ClearRunHistory(ctx, time.Now().AddDate(0, 0, -days))
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance",
			s.translate(r, "maintenance.clearedRunsDays", removed, days), "")
	case "reset_stats":
		reset, err := s.store.ResetUsageStats(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance",
			s.translate(r, "maintenance.statsReset", reset.Searches, reset.History), "")
	case "reset_api_usage":
		removed, err := s.store.ResetAPIUsage(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance",
			s.translate(r, "maintenance.apiReset", removed), "")
	case "delete_all":
		if r.FormValue("confirm") != "DELETE" {
			redirect(w, r, "/settings?section=maintenance", "", s.translate(r, "maintenance.deleteConfirmWord"))
			return
		}
		removed, err := s.store.DeleteAllReleases(ctx)
		if err != nil {
			redirect(w, r, "/settings?section=maintenance", "", err.Error())
			return
		}
		redirect(w, r, "/settings?section=maintenance", s.translate(r, "maintenance.allDeleted", removed), "")
	default:
		redirect(w, r, "/settings?section=maintenance", "", s.translate(r, "maintenance.unknown"))
	}
}

func (s *Server) maintenanceBackup(w http.ResponseWriter, r *http.Request) {
	file, err := os.CreateTemp("", "indexarr-backup-*.db")
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	target := file.Name()
	_ = file.Close()
	_ = os.Remove(target)
	defer os.Remove(target)
	if err := s.store.BackupTo(r.Context(), target); err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	name := "indexarr-backup-" + time.Now().Format("20060102-150405") + ".db"
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, filepath.Clean(target))
}

// intInRange parses an optional positive integer form field. An empty value
// uses fallback; anything outside 1..max is rejected.
func intInRange(raw string, fallback, max int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > max {
		return 0, fmt.Errorf("enter a number between 1 and %d", max)
	}
	return value, nil
}

// settingsSecret returns a secret only after an explicit same-origin fetch.
// The value is never part of the server-rendered settings HTML.
func (s *Server) settingsSecret(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.TrimSpace(r.Header.Get("X-Requested-With")) != "fetch" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	idValue := strings.TrimSpace(r.PathValue("id"))
	var value string
	switch {
	case name == "apikey":
		value = settings.APIKey
	case name == "webhook":
		value = settings.NotifyWebhookURL
	case name == "telegram_token":
		value = settings.NotifyTelegramToken
	case name == "apprise_key":
		value = settings.NotifyAppriseKey
	case name == "discord_webhook":
		value = settings.NotifyDiscordURL
	case name == "slack_webhook":
		value = settings.NotifySlackURL
	case name == "gotify_token":
		value = settings.NotifyGotifyToken
	case name == "ntfy_token":
		value = settings.NotifyNtfyToken
	case name == "pushover_token":
		value = settings.NotifyPushoverToken
	case name == "pushover_user":
		value = settings.NotifyPushoverUser
	case name == "smtp_password":
		value = settings.NotifySMTPPassword
	case name == "torznab":
		if settings.APIKey == "" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		value = torznabapi.BaseURL(r, settings) + "/api?apikey=" + url.QueryEscape(settings.APIKey)
	case name == "key" || idValue != "":
		id, parseErr := strconv.ParseInt(idValue, 10, 64)
		if parseErr != nil || id <= 0 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		keys, listErr := s.store.ListAPIKeys(r.Context())
		if listErr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		for _, key := range keys {
			if key.ID == id {
				value = key.Key
				break
			}
		}
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if value == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(struct {
		Value string `json:"value"`
	}{Value: value})
}
