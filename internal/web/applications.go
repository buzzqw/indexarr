package web

import (
	"database/sql"
	"errors"
	"fmt"
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
	{"disabled", "Disabilitato"},
	{"add-remove", "Solo aggiunta/rimozione"},
	{"full", "Sincronizzazione completa"},
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
	Unavailable string
}

func (s *Server) applicationsPage(w http.ResponseWriter, r *http.Request) {
	apps, err := s.store.ListApplications(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	data := applicationsPage{base: s.newBase(r, "Applicazioni", "applications")}
	if s.sync == nil {
		data.Unavailable = "Il servizio di sincronizzazione non è configurato: le applicazioni restano modificabili, ma il pulsante Sync segnala l'errore."
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
	data := s.newApplicationForm(r, "Nuova applicazione", nil, true)
	s.render(w, r, "application_form", data, http.StatusOK)
}

// applicationInputFromForm parses the non-secret fields. The API key is
// handled by the caller so it never ends up in a page or a log line.
func applicationInputFromForm(r *http.Request, create bool) (store.Application, []string) {
	app := store.Application{}
	var problems []string

	app.Kind = strings.ToLower(strings.TrimSpace(r.FormValue("kind")))
	if !validApplicationKind(app.Kind) {
		problems = append(problems, "Scegli un tipo di applicazione valido.")
	}

	app.Name = strings.TrimSpace(r.FormValue("name"))
	if app.Name == "" {
		problems = append(problems, "Il nome è obbligatorio.")
	}

	rawURL := strings.TrimSpace(r.FormValue("url"))
	normalized, err := normalizeApplicationURL(rawURL)
	if err != nil {
		problems = append(problems, "L'URL deve essere un indirizzo http:// o https:// assoluto.")
	} else {
		app.URL = normalized
	}

	level := strings.ToLower(strings.TrimSpace(r.FormValue("sync_level")))
	if level == "" && create {
		level = "full"
	}
	if !validApplicationSyncLevel(level) {
		problems = append(problems, "Scegli un livello di sincronizzazione valido.")
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
	app, problems := applicationInputFromForm(r, true)
	apiKey := strings.TrimSpace(r.FormValue("api_key"))
	if apiKey == "" {
		problems = append(problems, "La chiave API è obbligatoria.")
	}

	data := s.newApplicationForm(r, "Nuova applicazione", nil, true)
	data.Kind = app.Kind
	data.Name = app.Name
	data.URL = strings.TrimSpace(r.FormValue("url"))
	data.SyncLevel = app.SyncLevel
	if data.SyncLevel == "" {
		data.SyncLevel = "full"
	}
	if len(problems) > 0 {
		data.Problems = problems
		data.Error = "Controlla i campi dell'applicazione"
		s.render(w, r, "application_form", data, http.StatusBadRequest)
		return
	}
	if s.vault == nil {
		data.Problems = []string{"Il vault non è configurato: impossibile cifrare la chiave API."}
		data.Error = "Vault non configurato"
		s.render(w, r, "application_form", data, http.StatusServiceUnavailable)
		return
	}
	sealed, err := s.vault.SealString(apiKey)
	if err != nil {
		s.log.Error("seal application api key", "component", "indexarr", "subsystem", "web", "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "Impossibile cifrare la chiave API.")
		return
	}
	app.APIKeyEnc = []byte(sealed)
	if _, err := s.store.CreateApplication(r.Context(), app); err != nil {
		s.log.Error("create application", "component", "indexarr", "subsystem", "web", "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "Impossibile salvare l'applicazione.")
		return
	}
	redirect(w, r, "/applications", "Applicazione creata", "")
}

func (s *Server) applicationEdit(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "ID applicazione non valido")
		return
	}
	current, err := s.store.GetApplication(r.Context(), id)
	if err != nil {
		s.renderError(w, r, statusForApplicationError(err), applicationErrorMessage(err))
		return
	}
	data := s.newApplicationForm(r, "Modifica "+current.Name, &current, false)
	s.render(w, r, "application_form", data, http.StatusOK)
}

func (s *Server) applicationUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/applications", "", "ID applicazione non valido")
		return
	}
	current, err := s.store.GetApplication(r.Context(), id)
	if err != nil {
		redirect(w, r, "/applications", "", applicationErrorMessage(err))
		return
	}

	app, problems := applicationInputFromForm(r, false)
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
			problems = append(problems, "Il vault non è configurato: impossibile cifrare la chiave API.")
		} else if sealed, err := s.vault.SealString(apiKey); err != nil {
			s.log.Error("seal application api key", "component", "indexarr", "subsystem", "web", "error", err)
			problems = append(problems, "Impossibile cifrare la chiave API.")
		} else {
			app.APIKeyEnc = []byte(sealed)
		}
	default:
		app.APIKeyEnc = current.APIKeyEnc
	}

	if len(problems) > 0 {
		data := s.newApplicationForm(r, "Modifica "+current.Name, &current, false)
		data.Kind = app.Kind
		data.Name = app.Name
		data.URL = strings.TrimSpace(r.FormValue("url"))
		data.SyncLevel = app.SyncLevel
		data.Problems = problems
		data.Error = "Controlla i campi dell'applicazione"
		s.render(w, r, "application_form", data, http.StatusBadRequest)
		return
	}
	if err := s.store.UpdateApplication(r.Context(), app); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			redirect(w, r, "/applications", "", "Applicazione non trovata")
			return
		}
		s.log.Error("update application", "component", "indexarr", "subsystem", "web", "id", id, "error", err)
		s.renderError(w, r, http.StatusInternalServerError, "Impossibile salvare l'applicazione.")
		return
	}
	redirect(w, r, "/applications", "Applicazione salvata", "")
}

func (s *Server) applicationDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/applications", "", "ID applicazione non valido")
		return
	}
	if err := s.store.DeleteApplication(r.Context(), id); err != nil {
		s.log.Error("delete application", "component", "indexarr", "subsystem", "web", "id", id, "error", err)
		redirect(w, r, "/applications", "", "Impossibile eliminare l'applicazione.")
		return
	}
	redirect(w, r, "/applications", "Applicazione eliminata", "")
}

func (s *Server) applicationSync(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/applications", "", "ID applicazione non valido")
		return
	}
	app, err := s.store.GetApplication(r.Context(), id)
	if err != nil {
		redirect(w, r, "/applications", "", applicationErrorMessage(err))
		return
	}
	if s.sync == nil {
		redirect(w, r, "/applications", "", "Il servizio di sincronizzazione non è configurato.")
		return
	}

	result, syncErr := s.sync.SyncApp(r.Context(), id)
	if syncErr != nil {
		s.log.Warn("manual application sync failed",
			"component", "indexarr", "subsystem", "web", "application", app.Name, "error", syncErr)
		redirect(w, r, "/applications", "", fmt.Sprintf("Sync di %s non riuscito: %s", app.Name, syncErr.Error()))
		return
	}
	message := fmt.Sprintf("Sync di %s: %d aggiunti, %d aggiornati, %d eliminati, %d saltati",
		app.Name, result.Added, result.Updated, result.Deleted, result.Skipped)
	if strings.EqualFold(app.SyncLevel, "disabled") {
		message = fmt.Sprintf("Sync di %s: applicazione disabilitata, nessuna modifica (0 aggiunti, 0 aggiornati, 0 eliminati, 0 saltati)", app.Name)
	}
	redirect(w, r, "/applications", message, "")
}

func (s *Server) applicationsSyncAll(w http.ResponseWriter, r *http.Request) {
	if s.sync == nil {
		redirect(w, r, "/applications", "", "Il servizio di sincronizzazione non è configurato.")
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
	message := fmt.Sprintf("Sincronizzazione completata: %d applicazioni, %d aggiunti, %d aggiornati, %d eliminati, %d saltati",
		len(results), added, updated, deleted, skipped)
	problem := ""
	if failures > 0 {
		problem = fmt.Sprintf("%d applicazione/i con errori: controlla la colonna «Ultimo errore».", failures)
	}
	redirect(w, r, "/applications", message, problem)
}

func statusForApplicationError(err error) int {
	if errors.Is(err, sql.ErrNoRows) {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func applicationErrorMessage(err error) string {
	if errors.Is(err, sql.ErrNoRows) {
		return "Applicazione non trovata"
	}
	return err.Error()
}
