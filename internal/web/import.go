package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/importers"
)

// importTimeout bounds one import request. The Prowlarr importer talks to a
// remote instance; the Jackett one reads local files.
const importTimeout = 20 * time.Second

type importIndexerRow struct {
	Name           string
	Implementation string
	Protocol       string
	DefinitionHint string
	BaseURL        string
	Categories     string
	Tags           string
	Enabled        bool
	HasKey         bool
	HasCredentials bool
}

type importApplicationRow struct {
	Kind      string
	Name      string
	URL       string
	SyncLevel string
	HasKey    bool
}

type importProxyRow struct {
	Kind        string
	URL         string
	HasUser     bool
	HasPassword bool
}

type importPlanView struct {
	Source       string
	Indexers     []importIndexerRow
	Applications []importApplicationRow
	Proxies      []importProxyRow
	Warnings     []string
}

type importPage struct {
	base
	ProwlarrURL string
	JackettPath string
	Plan        *importPlanView
}

func (s *Server) importPage(w http.ResponseWriter, r *http.Request) {
	data := importPage{base: s.newBase(r, "Import", "import")}
	data.ProwlarrURL = strings.TrimSpace(r.URL.Query().Get("base_url"))
	data.JackettPath = strings.TrimSpace(r.URL.Query().Get("path"))
	s.render(w, r, "import", data, http.StatusOK)
}

func (s *Server) importProwlarr(w http.ResponseWriter, r *http.Request) {
	baseURL := strings.TrimSpace(r.FormValue("base_url"))
	apiKey := strings.TrimSpace(r.FormValue("api_key"))
	data := importPage{
		base:        s.newBase(r, "Import", "import"),
		ProwlarrURL: baseURL,
	}
	if baseURL == "" || apiKey == "" {
		data.Error = "Inserisci l'URL di Prowlarr e la chiave API."
		s.render(w, r, "import", data, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), importTimeout)
	defer cancel()
	plan, err := importers.FromProwlarr(ctx, baseURL, apiKey, &http.Client{Timeout: importTimeout})
	if err != nil {
		s.log.Warn("prowlarr import failed", "component", "indexarr", "subsystem", "web", "error", err)
		data.Error = "Import da Prowlarr non riuscito: " + redactImportText(err.Error(), apiKey)
		s.render(w, r, "import", data, http.StatusBadRequest)
		return
	}
	data.Plan = importPlanFrom(plan)
	s.render(w, r, "import", data, http.StatusOK)
}

func (s *Server) importJackett(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.FormValue("path"))
	data := importPage{
		base:        s.newBase(r, "Import", "import"),
		JackettPath: path,
	}
	if path == "" {
		data.Error = "Indica la directory dati di Jackett o il file ServerConfig.json."
		s.render(w, r, "import", data, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), importTimeout)
	defer cancel()
	plan, err := importers.FromJackettConfig(ctx, path)
	if err != nil {
		s.log.Warn("jackett import failed", "component", "indexarr", "subsystem", "web", "error", err)
		data.Error = "Import da Jackett non riuscito: " + redactImportText(err.Error())
		s.render(w, r, "import", data, http.StatusBadRequest)
		return
	}
	data.Plan = importPlanFrom(plan)
	s.render(w, r, "import", data, http.StatusOK)
}

// importPlanFrom builds the page view. Secret fields (APIKey, Username,
// Password) are deliberately not copied: the template can only show whether
// a value exists.
func importPlanFrom(plan importers.Plan) *importPlanView {
	view := &importPlanView{
		Source:   plan.Source,
		Warnings: append([]string(nil), plan.Warnings...),
	}
	for _, indexer := range plan.Indexers {
		view.Indexers = append(view.Indexers, importIndexerRow{
			Name:           indexer.Name,
			Implementation: indexer.Implementation,
			Protocol:       string(indexer.Protocol),
			DefinitionHint: indexer.DefinitionHint,
			BaseURL:        indexer.BaseURL,
			Categories:     joinInts(indexer.Categories),
			Tags:           strings.Join(indexer.Tags, ", "),
			Enabled:        indexer.Enabled,
			HasKey:         indexer.APIKey != "",
			HasCredentials: indexer.Username != "" || indexer.Password != "",
		})
	}
	for _, app := range plan.Applications {
		view.Applications = append(view.Applications, importApplicationRow{
			Kind:      app.Kind,
			Name:      app.Name,
			URL:       app.URL,
			SyncLevel: app.SyncLevel,
			HasKey:    app.APIKey != "",
		})
	}
	for _, proxy := range plan.Proxies {
		view.Proxies = append(view.Proxies, importProxyRow{
			Kind:        proxy.Kind,
			URL:         proxy.URL,
			HasUser:     proxy.Username != "",
			HasPassword: proxy.Password != "",
		})
	}
	return view
}

func joinInts(values []int) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ", ")
}

// redactImportText removes any secret the caller still knows about from an
// error or warning. The importers already redact their own messages; this is a
// second line of defence around the submitted values.
func redactImportText(text string, secrets ...string) string {
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret == "" {
			continue
		}
		for _, form := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			if form != "" {
				text = strings.ReplaceAll(text, form, "[redacted]")
			}
		}
	}
	return text
}
