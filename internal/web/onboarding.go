package web

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/store"
)

// setupSite is one quick-add row of the first-run wizard. It reuses the
// catalog index and the existing /indexers/catalog/add handler.
type setupSite struct {
	ID       string
	Name     string
	Type     string
	Required int
	Protocol string
}

type setupPage struct {
	base
	Step         int
	Steps        int
	SuggestedURL string
	PublicURL    string
	HasPublicURL bool
	Sites        []setupSite
	SiteTotal    int
	AppCount     int
	ClientCount  int
}

const setupSteps = 4

// shouldShowSetup keeps the Home replaced by the wizard until the operator
// finishes or skips it. Adding an indexer always ends the wizard.
func (s *Server) shouldShowSetup(r *http.Request, settings store.Settings, indexers int) bool {
	if indexers > 0 {
		return false
	}
	return !settings.SetupDismissed
}

func (s *Server) setupData(r *http.Request, step int) (setupPage, error) {
	if step < 1 || step > setupSteps {
		step = 1
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		return setupPage{}, err
	}
	apps, err := s.store.ListApplications(r.Context())
	if err != nil {
		return setupPage{}, err
	}
	clients, err := s.store.ListDownloadClients(r.Context())
	if err != nil {
		return setupPage{}, err
	}
	data := setupPage{
		base:         s.newBaseT(r, "setup.title", "dashboard"),
		Step:         step,
		Steps:        setupSteps,
		SuggestedURL: suggestedPublicURL(r),
		PublicURL:    settings.PublicURL,
		HasPublicURL: strings.TrimSpace(settings.PublicURL) != "",
		AppCount:     len(apps),
		ClientCount:  len(clients),
	}
	if step == 3 {
		index, err := s.siteCatalog()
		if err == nil {
			data.Sites, data.SiteTotal = setupSites(index)
			for i := range data.Sites {
				data.Sites[i].Type = s.catalogTypeLabel(r, data.Sites[i].Type)
			}
		}
	}
	return data, nil
}

// setupSites picks public, verified sites that need no credentials: one click
// adds them through the same path as the catalog list.
func setupSites(index []catalog.IndexEntry) ([]setupSite, int) {
	var out []setupSite
	for _, entry := range index {
		if !catalogQuickAdd(entry) {
			continue
		}
		out = append(out, setupSite{
			ID: entry.ID, Name: entry.Name, Type: entry.Type,
			Required: len(entry.Required), Protocol: entry.Protocol,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Required != out[j].Required {
			return out[i].Required < out[j].Required
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	total := len(out)
	if len(out) > 12 {
		out = out[:12]
	}
	return out, total
}

func (s *Server) setupPage(w http.ResponseWriter, r *http.Request) {
	step := intParam(r.URL.Query().Get("step"), 1)
	data, err := s.setupData(r, step)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.render(w, r, "setup", data, http.StatusOK)
}

func (s *Server) setupAddress(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		redirect(w, r, "/setup", "", err.Error())
		return
	}
	raw := strings.TrimRight(strings.TrimSpace(r.FormValue("public_url")), "/")
	if raw != "" {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			redirect(w, r, "/setup", "", s.translate(r, "settings.error.publicURL"))
			return
		}
	}
	settings.PublicURL = raw
	if err := s.store.SaveSettings(r.Context(), settings); err != nil {
		redirect(w, r, "/setup", "", err.Error())
		return
	}
	redirect(w, r, "/setup?step=2", s.translate(r, "setup.addressSaved"), "")
}

func (s *Server) setupSkip(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		redirect(w, r, "/", "", err.Error())
		return
	}
	settings.SetupDismissed = true
	if err := s.store.SaveSettings(r.Context(), settings); err != nil {
		redirect(w, r, "/", "", err.Error())
		return
	}
	redirect(w, r, "/", s.translate(r, "setup.skipped"), "")
}

func (s *Server) setupFinish(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		redirect(w, r, "/", "", err.Error())
		return
	}
	settings.SetupDismissed = true
	if err := s.store.SaveSettings(r.Context(), settings); err != nil {
		redirect(w, r, "/", "", err.Error())
		return
	}
	redirect(w, r, "/", s.translate(r, "setup.finished"), "")
}

func (s *Server) setupReopen(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		redirect(w, r, "/settings?section=general", "", err.Error())
		return
	}
	settings.SetupDismissed = false
	if err := s.store.SaveSettings(r.Context(), settings); err != nil {
		redirect(w, r, "/settings?section=general", "", err.Error())
		return
	}
	redirect(w, r, "/setup?step=1", s.translate(r, "setup.reopened"), "")
}
