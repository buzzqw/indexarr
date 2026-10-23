package web

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"

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
}

type applicationsPage struct {
	base
	Rows        []applicationRow
	Unavailable bool
}

func (s *Server) applicationsPage(w http.ResponseWriter, r *http.Request) {
	apps, err := s.store.ListApplications(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	data := applicationsPage{base: s.newBaseT(r, "applications.title", "applications")}
	if s.sync == nil {
		data.Unavailable = true
	}
	for _, app := range apps {
		data.Rows = append(data.Rows, applicationRow{
			Application:    app,
			KindLabel:      applicationKindLabel(app.Kind),
			SyncLevelLabel: applicationSyncLevelLabel(app.SyncLevel),
			HasKey:         len(app.APIKeyEnc) > 0,
		})
	}
	s.render(w, r, "applications", data, http.StatusOK)
}

type applicationFormPage struct {
	base
	IsNew       bool
	Application store.Application
	Kind        string
	Name        string
	URL         string
	SyncLevel   string
	HasKey      bool
	Kinds       []applicationOption
	SyncLevels  []applicationOption
	Problems    []string
}

func (s *Server) newApplicationForm(r *http.Request, title string, current *store.Application, isNew bool) applicationFormPage {
	data := applicationFormPage{
		base:       s.newBase(r, title, "applications"),
		IsNew:      isNew,
		Kind:       "sonarr",
		SyncLevel:  "full",
		Kinds:      applicationKindOptions,
		SyncLevels: applicationSyncLevelOptions,
	}
	if current != nil {
		data.Application = *current
		data.Kind = current.Kind
		data.Name = current.Name
		data.URL = current.URL
		data.SyncLevel = current.SyncLevel
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
		problems = append(problems, s.translate(r, "application.error.nameRequired"))
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
	return app, problems
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
