package web

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/appsync"
	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/store"
)

// applicationOption is one entry of a select in the application form.
type applicationOption struct {
	Value string
	Label string
}

// applicationKindOptions lists the downstream applications indexarr can keep
// in sync. The values match syncjob/appsync.
var applicationKindOptions = []applicationOption{
	{"sonarr", "Sonarr"},
	{"radarr", "Radarr"},
	{"lidarr", "Lidarr"},
	{"readarr", "Readarr"},
	{"whisparr", "Whisparr"},
	{"mylar3", "Mylar3"},
	{"lazylibrarian", "LazyLibrarian"},
}

// applicationSyncLevelOptions are the three levels accepted by the store. The
// template explains what each one does.
var applicationSyncLevelOptions = []applicationOption{
	{"disabled", "Disabled"},
	{"add-remove", "Add/remove only"},
	{"full", "Full sync"},
}

func applicationKindLabel(kind string) string {
	for _, option := range applicationKindOptions {
		if option.Value == kind {
			return option.Label
		}
	}
	return kind
}

func applicationSyncLevelLabel(level string) string {
	for _, option := range applicationSyncLevelOptions {
		if option.Value == level {
			return option.Label
		}
	}
	return level
}

func validApplicationKind(kind string) bool {
	for _, option := range applicationKindOptions {
		if option.Value == kind {
			return true
		}
	}
	return false
}

func validApplicationSyncLevel(level string) bool {
	for _, option := range applicationSyncLevelOptions {
		if option.Value == level {
			return true
		}
	}
	return false
}

type applicationRow struct {
	Application    store.Application
	KindLabel      string
	SyncLevelLabel string
	HasKey         bool
	Snapshots      []snapshotRow
}

// snapshotRow adds the translation key of the reason to a stored snapshot.
type snapshotRow struct {
	store.AppSnapshot
	ReasonKey string
}

func snapshotReasonLabel(lang i18n.Language, reason string) string {
	return i18n.T(lang, "snapshot.reason."+reason)
}

type applicationsPage struct {
	base
	Rows        []applicationRow
	Clients     []downloadClientRow
	Unavailable bool
	TorznabURL  string
}

func (s *Server) applicationsPage(w http.ResponseWriter, r *http.Request) {
	apps, err := s.store.ListApplications(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	clients, err := s.store.ListDownloadClients(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	data := applicationsPage{
		base: s.newBaseT(r, "applications.title", "applications"),
	}
	data.TorznabURL = data.base.TorznabURL
	if s.sync == nil {
		data.Unavailable = true
	}
	lang := requestLanguage(r)
	for _, app := range apps {
		row := applicationRow{
			Application:    app,
			KindLabel:      applicationKindLabel(app.Kind),
			SyncLevelLabel: applicationSyncLevelLabel(app.SyncLevel),
			HasKey:         len(app.APIKeyEnc) > 0,
		}
		snapshots, err := s.store.ListAppSnapshots(r.Context(), app.ID)
		if err != nil {
			s.renderError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		for _, snapshot := range snapshots {
			row.Snapshots = append(row.Snapshots, snapshotRow{
				AppSnapshot: snapshot,
				ReasonKey:   snapshotReasonLabel(lang, snapshot.Reason),
			})
		}
		data.Rows = append(data.Rows, row)
	}
	for _, client := range clients {
		host := client.URL
		if parsed, err := url.Parse(client.URL); err == nil && parsed.Host != "" {
			host = parsed.Host
		}
		data.Clients = append(data.Clients, downloadClientRow{
			Client:      client,
			KindLabel:   downloadClientKindLabel(client.Kind),
			Host:        host,
			HasSecret:   client.PasswordEnc != "" || client.APIKeyEnc != "",
			ProtocolKey: "client.protocol." + client.Protocol,
		})
	}
	s.render(w, r, "applications", data, http.StatusOK)
}

type applicationFormPage struct {
	base
	IsNew                   bool
	Application             store.Application
	Kind                    string
	Name                    string
	URL                     string
	SyncLevel               string
	TagFilter               string
	EnableRSS               bool
	EnableAutomaticSearch   bool
	EnableInteractiveSearch bool
	MinSeeders              int
	Categories              string
	HasKey                  bool
	Kinds                   []applicationOption
	SyncLevels              []applicationOption
	Problems                []string
}

func (s *Server) newApplicationForm(r *http.Request, title string, current *store.Application, isNew bool) applicationFormPage {
	data := applicationFormPage{
		base:                    s.newBase(r, title, "applications"),
		IsNew:                   isNew,
		Kind:                    "sonarr",
		SyncLevel:               "full",
		EnableRSS:               true,
		EnableAutomaticSearch:   true,
		EnableInteractiveSearch: true,
		Kinds:                   applicationKindOptions,
		SyncLevels:              applicationSyncLevelOptions,
	}
	if current != nil {
		data.Application = *current
		data.Kind = current.Kind
		data.Name = current.Name
		data.URL = current.URL
		data.SyncLevel = current.SyncLevel
		data.TagFilter = current.TagFilter
		data.EnableRSS = current.EnableRSS
		data.EnableAutomaticSearch = current.EnableAutomaticSearch
		data.EnableInteractiveSearch = current.EnableInteractiveSearch
		data.MinSeeders = current.MinSeeders
		data.Categories = current.Categories
		if data.SyncLevel == "" {
			data.SyncLevel = "full"
		}
		data.HasKey = len(current.APIKeyEnc) > 0
	}
	return data
}

func (s *Server) applicationNew(w http.ResponseWriter, r *http.Request) {
	data := s.newApplicationForm(r, s.translate(r, "application.new"), nil, true)
	s.render(w, r, "application_form", data, http.StatusOK)
}

// applicationInputFromForm parses the non-secret fields. The API key is
// handled by the caller so it never ends up in a page or a log line.
func (s *Server) applicationInputFromForm(r *http.Request, create bool) (store.Application, []string) {
	app := store.Application{}
	var problems []string

	app.Kind = strings.ToLower(strings.TrimSpace(r.FormValue("kind")))
	if !validApplicationKind(app.Kind) {
		problems = append(problems, s.translate(r, "application.error.invalidKind"))
	}

	app.Name = strings.TrimSpace(r.FormValue("name"))
	if app.Name == "" {
		// Name is an indexarr label, not something the user must know. The
		// app type is the sensible default; the advanced block can override it.
		app.Name = applicationKindLabel(app.Kind)
		if strings.TrimSpace(app.Name) == "" {
			problems = append(problems, s.translate(r, "application.error.nameRequired"))
		}
	}

	rawURL := strings.TrimSpace(r.FormValue("url"))
	normalized, err := normalizeApplicationURL(rawURL)
	if err != nil {
		problems = append(problems, s.translate(r, "application.error.urlInvalid"))
	} else {
		app.URL = normalized
	}

	level := strings.ToLower(strings.TrimSpace(r.FormValue("sync_level")))
	if level == "" && create {
		level = "full"
	}
	if !validApplicationSyncLevel(level) {
		problems = append(problems, s.translate(r, "application.error.levelInvalid"))
	}
	app.SyncLevel = level
	app.TagFilter = normalizeTagFilter(r.FormValue("tag_filter"))

	// The form marks the profile block with a hidden field. A legacy client
	// that does not send that block gets the safe defaults; an explicit
	// unchecked box is still respected because the real form sends the marker.
	if r.Form.Has("profile_fields") {
		app.EnableRSS = r.FormValue("enable_rss") != ""
		app.EnableAutomaticSearch = r.FormValue("enable_automatic_search") != ""
		app.EnableInteractiveSearch = r.FormValue("enable_interactive_search") != ""
	} else {
		app.EnableRSS = true
		app.EnableAutomaticSearch = true
		app.EnableInteractiveSearch = true
	}

	rawSeeders := strings.TrimSpace(r.FormValue("min_seeders"))
	if rawSeeders != "" {
		seeders, err := strconv.Atoi(rawSeeders)
		if err != nil || seeders < 0 {
			problems = append(problems, s.translate(r, "application.error.minSeedersInvalid"))
		} else {
			app.MinSeeders = seeders
		}
	}
	categories, err := normalizeApplicationCategories(r.FormValue("categories"))
	if err != nil {
		problems = append(problems, s.translate(r, "application.error.categoriesInvalid"))
	} else {
		app.Categories = categories
	}
	return app, problems
}

// normalizeApplicationCategories turns the free-form category list into the
// canonical comma-separated form stored in applications.categories: standard
// Torznab ids, trimmed, deduplicated and sorted. Empty means "all".
func normalizeApplicationCategories(raw string) (string, error) {
	seen := make(map[int]bool)
	ids := make([]int, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil || id < 0 {
			return "", errors.New("category ids must be non-negative integers")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Ints(ids)
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ","), nil
}

// normalizeTagFilter turns the free-form tag filter into a stable
// comma-separated list: trimmed labels, no duplicates (case-insensitively),
// bounded in length so a paste cannot bloat applications.tag_filter.
func normalizeTagFilter(raw string) string {
	limit := 200
	if runes := []rune(raw); len(runes) > limit*4 {
		raw = string(runes[:limit*4])
	}
	seen := make(map[string]bool)
	out := make([]string, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		label := strings.TrimSpace(part)
		if label == "" {
			continue
		}
		key := strings.ToLower(label)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, label)
		if len(out) == 20 {
			break
		}
	}
	filter := strings.Join(out, ", ")
	if runes := []rune(filter); len(runes) > limit {
		filter = strings.TrimSpace(string(runes[:limit]))
	}
	return filter
}

// normalizeApplicationURL validates an application root URL and drops the
// query string, the fragment and a trailing slash, matching syncjob.
func normalizeApplicationURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("url must be an absolute http or https URL")
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", errors.New("url must be an absolute http or https URL")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (s *Server) applicationCreate(w http.ResponseWriter, r *http.Request) {
	app, problems := s.applicationInputFromForm(r, true)
	apiKey := strings.TrimSpace(r.FormValue("api_key"))
	if apiKey == "" {
		problems = append(problems, s.translate(r, "application.error.apiKeyRequired"))
	}

	data := s.newApplicationForm(r, s.translate(r, "application.new"), nil, true)
	data.Kind = app.Kind
	data.Name = app.Name
	data.URL = strings.TrimSpace(r.FormValue("url"))
	data.SyncLevel = app.SyncLevel
	data.TagFilter = app.TagFilter
	data.EnableRSS = app.EnableRSS
	data.EnableAutomaticSearch = app.EnableAutomaticSearch
	data.EnableInteractiveSearch = app.EnableInteractiveSearch
	data.MinSeeders = app.MinSeeders
	data.Categories = app.Categories
	if data.SyncLevel == "" {
		data.SyncLevel = "full"
	}
	if len(problems) > 0 {
		data.Problems = problems
		data.Error = s.translate(r, "application.error.checkFields")
		s.render(w, r, "application_form", data, http.StatusBadRequest)
		return
	}
	if s.vault == nil {
		data.Problems = []string{s.translate(r, "application.error.vaultRequired")}
		data.Error = s.translate(r, "application.error.vaultTitle")
		s.render(w, r, "application_form", data, http.StatusServiceUnavailable)
		return
	}
	sealed, err := s.vault.SealString(apiKey)
	if err != nil {
		s.log.Error("seal application api key", "component", "indexarr", "subsystem", "web", "error", err)
		s.renderError(w, r, http.StatusInternalServerError, s.translate(r, "application.error.encryptFailed"))
		return
	}
	app.APIKeyEnc = []byte(sealed)
	if _, err := s.store.CreateApplication(r.Context(), app); err != nil {
		s.log.Error("create application", "component", "indexarr", "subsystem", "web", "error", err)
		s.renderError(w, r, http.StatusInternalServerError, s.translate(r, "application.error.saveFailed"))
		return
	}
	redirect(w, r, "/applications", s.translate(r, "application.created"), "")
}

func (s *Server) applicationEdit(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "application.error.invalidID"))
		return
	}
	current, err := s.store.GetApplication(r.Context(), id)
	if err != nil {
		s.renderError(w, r, statusForApplicationError(err), s.applicationErrorMessage(r, err))
		return
	}
	data := s.newApplicationForm(r, s.translate(r, "application.edit", current.Name), &current, false)
	s.render(w, r, "application_form", data, http.StatusOK)
}

func (s *Server) applicationUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/applications", "", s.translate(r, "application.error.invalidID"))
		return
	}
	current, err := s.store.GetApplication(r.Context(), id)
	if err != nil {
		redirect(w, r, "/applications", "", s.applicationErrorMessage(r, err))
		return
	}

	app, problems := s.applicationInputFromForm(r, false)
	app.ID = current.ID
	app.LastSyncAt = current.LastSyncAt
	app.LastError = current.LastError

	apiKey := strings.TrimSpace(r.FormValue("api_key"))
	clearKey := r.FormValue("clear_api_key") != ""
	switch {
	case clearKey:
		app.APIKeyEnc = nil
	case apiKey != "":
		if s.vault == nil {
			problems = append(problems, s.translate(r, "application.error.vaultRequired"))
		} else if sealed, err := s.vault.SealString(apiKey); err != nil {
			s.log.Error("seal application api key", "component", "indexarr", "subsystem", "web", "error", err)
			problems = append(problems, s.translate(r, "application.error.encryptFailed"))
		} else {
			app.APIKeyEnc = []byte(sealed)
		}
	default:
		app.APIKeyEnc = current.APIKeyEnc
	}

	if len(problems) > 0 {
		data := s.newApplicationForm(r, s.translate(r, "application.edit", current.Name), &current, false)
		data.Kind = app.Kind
		data.Name = app.Name
		data.URL = strings.TrimSpace(r.FormValue("url"))
		data.SyncLevel = app.SyncLevel
		data.TagFilter = app.TagFilter
		data.EnableRSS = app.EnableRSS
		data.EnableAutomaticSearch = app.EnableAutomaticSearch
		data.EnableInteractiveSearch = app.EnableInteractiveSearch
		data.MinSeeders = app.MinSeeders
		data.Categories = app.Categories
		data.Problems = problems
		data.Error = s.translate(r, "application.error.checkFields")
		s.render(w, r, "application_form", data, http.StatusBadRequest)
		return
	}
	if err := s.store.UpdateApplication(r.Context(), app); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			redirect(w, r, "/applications", "", s.translate(r, "application.error.notFound"))
			return
		}
		s.log.Error("update application", "component", "indexarr", "subsystem", "web", "id", id, "error", err)
		s.renderError(w, r, http.StatusInternalServerError, s.translate(r, "application.error.saveFailed"))
		return
	}
	redirect(w, r, "/applications", s.translate(r, "application.saved"), "")
}

func (s *Server) applicationDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/applications", "", s.translate(r, "application.error.invalidID"))
		return
	}
	if err := s.store.DeleteApplication(r.Context(), id); err != nil {
		s.log.Error("delete application", "component", "indexarr", "subsystem", "web", "id", id, "error", err)
		redirect(w, r, "/applications", "", s.translate(r, "application.error.deleteFailed"))
		return
	}
	redirect(w, r, "/applications", s.translate(r, "application.deleted"), "")
}

func (s *Server) applicationSync(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/applications", "", s.translate(r, "application.error.invalidID"))
		return
	}
	app, err := s.store.GetApplication(r.Context(), id)
	if err != nil {
		redirect(w, r, "/applications", "", s.applicationErrorMessage(r, err))
		return
	}
	if s.sync == nil {
		redirect(w, r, "/applications", "", s.translate(r, "application.error.syncUnavailable"))
		return
	}

	result, syncErr := s.sync.SyncApp(r.Context(), id)
	if syncErr != nil {
		s.log.Warn("manual application sync failed",
			"component", "indexarr", "subsystem", "web", "application", app.Name, "error", syncErr)
		redirect(w, r, "/applications", "", s.translate(r, "application.syncFailed", app.Name, syncErr.Error()))
		return
	}
	message := s.translate(r, "application.syncSummary",
		app.Name, result.Added, result.Updated, result.Deleted, result.Skipped)
	if strings.EqualFold(app.SyncLevel, "disabled") {
		message = s.translate(r, "application.syncDisabled", app.Name)
	}
	redirect(w, r, "/applications", message, "")
}

func (s *Server) applicationsSyncAll(w http.ResponseWriter, r *http.Request) {
	if s.sync == nil {
		redirect(w, r, "/applications", "", s.translate(r, "application.error.syncUnavailable"))
		return
	}
	results := s.sync.SyncAll(r.Context())
	var added, updated, deleted, skipped, failures int
	for _, result := range results {
		added += result.Added
		updated += result.Updated
		deleted += result.Deleted
		skipped += result.Skipped
		if result.Err != nil {
			failures++
		}
	}
	message := s.translate(r, "application.syncAllSummary",
		len(results), added, updated, deleted, skipped)
	problem := ""
	if failures > 0 {
		problem = s.translate(r, "application.syncAllProblems", failures)
	}
	redirect(w, r, "/applications", message, problem)
}

// applicationSnapshot stores a manual checkpoint of the app indexer list.
func (s *Server) applicationSnapshot(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/applications", "", s.translate(r, "application.error.invalidID"))
		return
	}
	app, err := s.store.GetApplication(r.Context(), id)
	if err != nil {
		redirect(w, r, "/applications", "", s.applicationErrorMessage(r, err))
		return
	}
	if s.sync == nil {
		redirect(w, r, "/applications", "", s.translate(r, "application.error.syncUnavailable"))
		return
	}
	if _, err := s.sync.SnapshotApp(r.Context(), id); err != nil {
		if errors.Is(err, appsync.ErrUnsupported) {
			redirect(w, r, "/applications", "",
				s.translate(r, "application.snapshotUnsupported", app.Name))
			return
		}
		s.log.Warn("manual application snapshot failed",
			"component", "indexarr", "subsystem", "web", "application", app.Name, "error", err)
		redirect(w, r, "/applications", "",
			s.translate(r, "application.snapshotFailed", app.Name, err.Error()))
		return
	}
	redirect(w, r, "/applications",
		s.translate(r, "application.snapshotCreated", app.Name), "")
}

// applicationRestore brings the indexers managed by indexarr back to the state
// stored in one snapshot. Indexers created by the operator are never touched.
func (s *Server) applicationRestore(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/applications", "", s.translate(r, "application.error.invalidID"))
		return
	}
	snapshotID, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("snapshot")), 10, 64)
	if err != nil || snapshotID <= 0 {
		redirect(w, r, "/applications", "", s.translate(r, "application.error.invalidSnapshot"))
		return
	}
	app, err := s.store.GetApplication(r.Context(), id)
	if err != nil {
		redirect(w, r, "/applications", "", s.applicationErrorMessage(r, err))
		return
	}
	if s.sync == nil {
		redirect(w, r, "/applications", "", s.translate(r, "application.error.syncUnavailable"))
		return
	}
	result, restoreErr := s.sync.Restore(r.Context(), id, snapshotID)
	if restoreErr != nil {
		s.log.Warn("application restore failed",
			"component", "indexarr", "subsystem", "web", "application", app.Name,
			"snapshot", snapshotID, "error", restoreErr)
		redirect(w, r, "/applications", "",
			s.translate(r, "application.restoreFailed", app.Name, restoreErr.Error()))
		return
	}
	redirect(w, r, "/applications", s.translate(r, "application.restoreSummary",
		app.Name, result.Added, result.Updated, result.Deleted, result.Skipped), "")
}

func statusForApplicationError(err error) int {
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func (s *Server) applicationErrorMessage(r *http.Request, err error) string {
	if errors.Is(err, sql.ErrNoRows) {
		return s.translate(r, "application.error.notFound")
	}
	return err.Error()
}
