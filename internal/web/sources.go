package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/ingest"
	"github.com/buzzqw/indexarr/internal/store"
)

type sourcesPage struct {
	base
	Sources         []store.Source
	Running         map[int64]bool
	DefaultInterval int
	HostWarning     string
}

// sourcesRedirect keeps the old /sources URL working: the legacy sources now
// live in the Ponte legacy tab of the unified indexer page.
func (s *Server) sourcesRedirect(w http.ResponseWriter, r *http.Request) {
	redirectTab(w, r, "/indexers", "legacy")
}

// sourcesData loads the legacy source panel shared by the redirect target and
// the unified /indexers page.
func (s *Server) sourcesData(r *http.Request) (sourcesPage, error) {
	sources, err := s.store.ListSources(r.Context())
	if err != nil {
		return sourcesPage{}, err
	}
	settings, _ := s.store.LoadSettings(r.Context())
	running := map[int64]bool{}
	hosts := map[string]int{}
	for _, source := range sources {
		running[source.ID] = s.engine != nil && s.engine.IsRunning(source.ID)
		if parsed, err := url.Parse(source.URL); err == nil && parsed.Hostname() != "" {
			hosts[strings.ToLower(parsed.Hostname())]++
		}
	}
	hostWarning := ""
	var topHost string
	var topCount int
	for host, count := range hosts {
		if count > topCount {
			topHost, topCount = host, count
		}
	}
	if topCount >= 5 {
		hostWarning = fmt.Sprintf("%d sorgenti condividono l'host %s. I siti remoti limitano spesso le richieste (HTTP 429): disabilita, unisci o elimina gli elenchi che non servono, oppure aumenta l'intervallo.", topCount, topHost)
	}
	return sourcesPage{
		base:            s.newBase(r, "Ponte legacy", "sources"),
		Sources:         sources,
		Running:         running,
		DefaultInterval: settings.DefaultIntervalSeconds,
		HostWarning:     hostWarning,
	}, nil
}

type sourceFormPage struct {
	base
	Source          store.Source
	IsNew           bool
	DefaultInterval int
}

func (s *Server) sourceNew(w http.ResponseWriter, r *http.Request) {
	settings, _ := s.store.LoadSettings(r.Context())
	s.render(w, r, "source_form", sourceFormPage{
		base: s.newBase(r, "Nuova sorgente legacy", "sources"),
		Source: store.Source{
			Kind:    "torznab",
			Manager: "auto",
			Enabled: true,
		},
		IsNew:           true,
		DefaultInterval: settings.DefaultIntervalSeconds,
	}, http.StatusOK)
}

func (s *Server) sourceEdit(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "invalid source id")
		return
	}
	source, err := s.store.GetSource(r.Context(), id)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "source not found")
		return
	}
	settings, _ := s.store.LoadSettings(r.Context())
	s.render(w, r, "source_form", sourceFormPage{
		base:            s.newBase(r, "Modifica sorgente legacy", "sources"),
		Source:          source,
		DefaultInterval: settings.DefaultIntervalSeconds,
	}, http.StatusOK)
}

func (s *Server) sourceCreate(w http.ResponseWriter, r *http.Request) {
	source, err := parseSourceForm(r, nil)
	if err != nil {
		s.renderSourceFormError(w, r, source, true, err)
		return
	}
	id, err := s.store.CreateSource(r.Context(), source)
	if err != nil {
		s.renderSourceFormError(w, r, source, true, friendlySourceError(err))
		return
	}
	source.ID = id
	if s.sourceSavedAction(w, r, source, "Source created") {
		return
	}
	redirect(w, r, "/sources", "Source created", "")
}

// sourceSavedAction implements the optional "Save and test/run" buttons. It
// returns true when it already wrote the response.
func (s *Server) sourceSavedAction(w http.ResponseWriter, r *http.Request, source store.Source, savedMessage string) bool {
	if s.engine == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.FormValue("then"))) {
	case "test":
		message, err := s.engine.TestSource(r.Context(), source)
		if err != nil {
			redirect(w, r, "/sources", "", "Saved, but the test failed: "+err.Error())
			return true
		}
		redirect(w, r, "/sources", savedMessage+" · Test OK: "+message, "")
		return true
	case "run":
		if err := s.engine.Trigger(source.ID); err != nil {
			redirect(w, r, "/sources", "", savedMessage+", but the run was not queued: "+err.Error())
			return true
		}
		redirect(w, r, "/sources", savedMessage+" · run queued", "")
		return true
	default:
		return false
	}
}

func (s *Server) sourceUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/sources", "", "invalid source id")
		return
	}
	existing, err := s.store.GetSource(r.Context(), id)
	if err != nil {
		redirect(w, r, "/sources", "", "source not found")
		return
	}
	source, err := parseSourceForm(r, &existing)
	if err != nil {
		s.renderSourceFormError(w, r, source, false, err)
		return
	}
	if err := s.store.UpdateSource(r.Context(), source); err != nil {
		s.renderSourceFormError(w, r, source, false, friendlySourceError(err))
		return
	}
	if s.sourceSavedAction(w, r, source, "Source saved") {
		return
	}
	redirect(w, r, "/sources", "Source saved", "")
}

func (s *Server) renderSourceFormError(w http.ResponseWriter, r *http.Request, source store.Source, isNew bool, err error) {
	settings, _ := s.store.LoadSettings(r.Context())
	data := sourceFormPage{
		base:            s.newBase(r, "Sorgente legacy", "sources"),
		Source:          source,
		IsNew:           isNew,
		DefaultInterval: settings.DefaultIntervalSeconds,
	}
	data.Error = err.Error()
	s.render(w, r, "source_form", data, http.StatusBadRequest)
}

func (s *Server) sourceDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/sources", "", "invalid source id")
		return
	}
	if err := s.store.DeleteSource(r.Context(), id); err != nil {
		redirect(w, r, "/sources", "", err.Error())
		return
	}
	redirect(w, r, "/sources", "Source deleted; its releases stay in the cache", "")
}

func (s *Server) sourceRun(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/sources", "", "invalid source id")
		return
	}
	switch err := s.engine.Trigger(id); {
	case err == nil:
		redirect(w, r, "/sources", "Run queued", "")
	case errors.Is(err, ingest.ErrAlreadyRunning):
		redirect(w, r, "/sources", "", "This source is already running")
	default:
		redirect(w, r, "/sources", "", err.Error())
	}
}

func (s *Server) sourceTest(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/sources", "", "invalid source id")
		return
	}
	source, err := s.store.GetSource(r.Context(), id)
	if err != nil {
		redirect(w, r, "/sources", "", "source not found")
		return
	}
	message, err := s.engine.TestSource(r.Context(), source)
	if err != nil {
		redirect(w, r, "/sources", "", "Test failed: "+err.Error())
		return
	}
	redirect(w, r, "/sources", "Test OK: "+message, "")
}

func (s *Server) sourcesRunAll(w http.ResponseWriter, r *http.Request) {
	queued, err := s.engine.TriggerAll(r.Context())
	if err != nil {
		redirect(w, r, "/sources", "", err.Error())
		return
	}
	redirect(w, r, "/sources", fmt.Sprintf("Queued %d source(s)", queued), "")
}

func (s *Server) sourcesTestAll(w http.ResponseWriter, r *http.Request) {
	count, err := s.engine.TestAll(r.Context())
	if err != nil {
		redirect(w, r, "/sources", "", err.Error())
		return
	}
	if count == 0 {
		redirect(w, r, "/sources", "", "No enabled sources to test")
		return
	}
	redirect(w, r, "/sources",
		fmt.Sprintf("Testing %d source(s) in the background; results in Logs", count), "")
}

// parseSourceForm validates and normalises the source form. existing is nil
// for a new source; when editing, empty secret or URL fields mean "unchanged"
// because the pages never render their current value.
func parseSourceForm(r *http.Request, existing *store.Source) (store.Source, error) {
	source := store.Source{}
	if existing != nil {
		source = *existing
	}
	source.Name = strings.TrimSpace(r.FormValue("name"))
	source.Kind = strings.ToLower(strings.TrimSpace(r.FormValue("kind")))
	if source.Kind == "" {
		source.Kind = "torznab"
	}
	if source.Kind != "torznab" && source.Kind != "rss" {
		return source, errors.New("Kind must be torznab or rss")
	}
	source.Manager = strings.ToLower(strings.TrimSpace(r.FormValue("manager")))
	if source.Manager == "" {
		source.Manager = feed.ManagerAuto
	}
	switch source.Manager {
	case feed.ManagerAuto, feed.ManagerJackett, feed.ManagerProwlarr, feed.ManagerMirCrew, feed.ManagerTorznab:
	default:
		return source, errors.New("Manager must be auto, jackett, prowlarr, mircrew or torznab")
	}
	source.URL = strings.TrimSpace(r.FormValue("url"))
	if source.URL == "" && existing != nil {
		source.URL = existing.URL
	}
	if source.URL == "" {
		return source, errors.New("URL is required")
	}
	parsed, err := url.Parse(source.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return source, errors.New("URL must be an absolute http:// or https:// address")
	}
	source.APIKey = strings.TrimSpace(r.FormValue("api_key"))
	if source.APIKey == "" && existing != nil {
		source.APIKey = existing.APIKey
	}
	source.Username = strings.TrimSpace(r.FormValue("username"))
	source.Password = r.FormValue("password")
	if source.Password == "" && existing != nil {
		source.Password = existing.Password
	}
	source.Query = strings.TrimSpace(r.FormValue("query"))
	source.IntervalSeconds = intParam(r.FormValue("interval_seconds"), 0)
	if source.IntervalSeconds != 0 && (source.IntervalSeconds < 60 || source.IntervalSeconds > 30*24*3600) {
		return source, errors.New("Interval must be 0 (use default) or between 60 and 2592000 seconds")
	}
	categories, err := normalizeCategories(r.FormValue("categories"))
	if err != nil {
		return source, err
	}
	source.Categories = categories
	source.DefaultCategory = strings.TrimSpace(r.FormValue("default_category"))
	if source.DefaultCategory != "" {
		if _, err := strconv.Atoi(source.DefaultCategory); err != nil ||
			len(source.DefaultCategory) < 4 || len(source.DefaultCategory) > 6 {
			return source, errors.New("Default category must be a numeric Torznab id such as 5040, or empty")
		}
	}
	source.Enabled = r.FormValue("enabled") != ""
	if source.Kind == "rss" {
		source.Manager = feed.ManagerAuto
		source.Categories = ""
		source.Query = ""
	}
	if source.Name == "" {
		return source, errors.New("Name is required")
	}
	if len(source.Name) > 100 {
		return source, errors.New("Name is too long (max 100 characters)")
	}
	return source, nil
}

func normalizeCategories(raw string) (string, error) {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, err := strconv.Atoi(part); err != nil {
			return "", fmt.Errorf("category %q is not a number", part)
		}
		if !seen[part] {
			seen[part] = true
			out = append(out, part)
		}
	}
	return strings.Join(out, ","), nil
}

func friendlySourceError(err error) error {
	message := err.Error()
	if strings.Contains(message, "UNIQUE constraint failed") {
		return errors.New("A source with this name already exists")
	}
	return err
}
