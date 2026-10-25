package adminapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/importers"
)

// importTimeout bounds a Prowlarr HTTP import and a Jackett directory scan.
const importTimeout = 20 * time.Second

type prowlarrImportRequest struct {
	URL    string `json:"url"`
	APIKey string `json:"api_key"`
}

type jackettImportRequest struct {
	Path            string `json:"path"`
	URL             string `json:"url"`
	APIKey          string `json:"api_key"`
	IncludeInactive bool   `json:"include_inactive"`
}

type importIndexer struct {
	Name           string   `json:"name"`
	Implementation string   `json:"implementation"`
	Protocol       string   `json:"protocol"`
	DefinitionHint string   `json:"definition_hint"`
	DefinitionID   string   `json:"definition_id,omitempty"`
	NativeStatus   string   `json:"native_status"`
	NativeReason   string   `json:"native_reason,omitempty"`
	NativeDetail   string   `json:"native_detail,omitempty"`
	Family         string   `json:"family,omitempty"`
	BaseURL        string   `json:"base_url"`
	Categories     []int    `json:"categories"`
	Tags           []string `json:"tags"`
	Priority       int      `json:"priority"`
	Enabled        bool     `json:"enabled"`
	CatalogOnly    bool     `json:"catalog_only"`
	DisabledReason string   `json:"disabled_reason,omitempty"`
	HasKey         bool     `json:"has_key"`
	HasBasicAuth   bool     `json:"has_basic_auth"`
}

type importApplication struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	SyncLevel string `json:"sync_level"`
	HasKey    bool   `json:"has_key"`
}

type importProxy struct {
	Kind    string `json:"kind"`
	URL     string `json:"url"`
	HasAuth bool   `json:"has_auth"`
}

type importResponse struct {
	Source       string              `json:"source"`
	Indexers     []importIndexer     `json:"indexers"`
	Applications []importApplication `json:"applications"`
	Proxies      []importProxy       `json:"proxies"`
	Pending      int                 `json:"pending"`
	Warnings     []string            `json:"warnings"`
}

func (s *Server) handleImportProwlarr(w http.ResponseWriter, r *http.Request) {
	var req prowlarrImportRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	baseURL, err := validateImportURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	apiKey := strings.TrimSpace(req.APIKey)

	ctx, cancel := context.WithTimeout(r.Context(), importTimeout)
	defer cancel()
	plan, err := importers.FromProwlarr(ctx, baseURL, apiKey, nil)
	if err != nil {
		s.logger.Warn("adminapi: prowlarr import failed", "error", err)
		writeError(w, http.StatusBadGateway, "prowlarr import failed")
		return
	}
	targets, err := importers.Targets(plan)
	if err != nil {
		s.logger.Error("adminapi: map prowlarr import targets", "error", err)
		writeError(w, http.StatusInternalServerError, "prowlarr import mapping failed")
		return
	}
	writeJSON(w, http.StatusOK, importPlanResponse(plan, targets, apiKey))
}

func (s *Server) handleImportJackett(w http.ResponseWriter, r *http.Request) {
	var req jackettImportRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	rawPath := strings.TrimSpace(req.Path)
	baseURL := strings.TrimSpace(req.URL)
	apiKey := strings.TrimSpace(req.APIKey)
	if rawPath == "" && baseURL == "" {
		writeError(w, http.StatusBadRequest, "path or url is required")
		return
	}
	if baseURL != "" && apiKey == "" {
		writeError(w, http.StatusBadRequest, "api_key is required with url")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), importTimeout)
	defer cancel()

	var plan importers.Plan
	var err error
	if baseURL != "" {
		plan, err = importers.FromJackettAPIWithOptions(ctx, baseURL, apiKey,
			&http.Client{Timeout: importTimeout},
			importers.JackettOptions{IncludeInactive: req.IncludeInactive})
	} else {
		path := filepath.Clean(rawPath)
		if !filepath.IsAbs(path) {
			writeError(w, http.StatusBadRequest, "path must be absolute")
			return
		}
		plan, err = importers.FromJackettConfig(ctx, path)
		if err == nil && req.IncludeInactive {
			if strings.TrimSpace(plan.ManagerAPIKey) == "" {
				writeError(w, http.StatusBadRequest, "api_key is required for the full catalog")
				return
			}
			plan, err = importers.FromJackettAPIWithOptions(ctx, plan.ManagerURL,
				plan.ManagerAPIKey, &http.Client{Timeout: importTimeout},
				importers.JackettOptions{IncludeInactive: true})
		}
	}
	if err != nil {
		s.logger.Warn("adminapi: jackett import failed", "error", err)
		writeError(w, http.StatusBadGateway, "jackett import failed")
		return
	}
	targets, err := importers.Targets(plan)
	if err != nil {
		s.logger.Error("adminapi: map prowlarr import targets", "error", err)
		writeError(w, http.StatusInternalServerError, "prowlarr import mapping failed")
		return
	}
	writeJSON(w, http.StatusOK, importPlanResponse(plan, targets, apiKey))
}

// importPlanResponse converts an importers.Plan into a response that never
// carries an API key, username, password or credential-bearing URL. Credentials
// are reported only as booleans, and warnings are redacted once more before
// leaving the process.
func importPlanResponse(plan importers.Plan, targets []importers.NativeTarget, extraSecrets ...string) importResponse {
	out := importResponse{
		Source:       plan.Source,
		Indexers:     make([]importIndexer, 0, len(plan.Indexers)),
		Applications: make([]importApplication, 0, len(plan.Applications)),
		Proxies:      make([]importProxy, 0, len(plan.Proxies)),
		Warnings:     make([]string, 0, len(plan.Warnings)),
	}

	secrets := append([]string(nil), extraSecrets...)
	for i, in := range plan.Indexers {
		secrets = append(secrets, in.APIKey, in.Username, in.Password)
		row := importIndexer{
			Name:           in.Name,
			Implementation: in.Implementation,
			Protocol:       string(in.Protocol),
			DefinitionHint: in.DefinitionHint,
			BaseURL:        sanitizeImportURL(in.BaseURL),
			Categories:     nonNilInts(in.Categories),
			Tags:           nonNilStrings(in.Tags),
			Priority:       in.Priority,
			Enabled:        in.Enabled,
			CatalogOnly:    in.CatalogOnly,
			DisabledReason: in.DisabledReason,
			HasKey:         strings.TrimSpace(in.APIKey) != "",
			HasBasicAuth: strings.TrimSpace(in.Username) != "" ||
				strings.TrimSpace(in.Password) != "",
		}
		if i < len(targets) {
			target := targets[i]
			row.DefinitionID = target.DefinitionID
			row.NativeStatus = target.Status
			if row.NativeStatus == "" {
				row.NativeStatus = importers.TargetNative
			}
			row.NativeReason = target.Reason
			row.NativeDetail = target.ReasonDetail
			row.Family = target.Family
			if row.NativeStatus == importers.TargetPending {
				// No native route yet: nothing will be created for this row.
				out.Pending++
			}
		}
		out.Indexers = append(out.Indexers, row)
	}
	for _, app := range plan.Applications {
		secrets = append(secrets, app.APIKey)
		out.Applications = append(out.Applications, importApplication{
			Kind:      app.Kind,
			Name:      app.Name,
			URL:       sanitizeImportURL(app.URL),
			SyncLevel: app.SyncLevel,
			HasKey:    strings.TrimSpace(app.APIKey) != "",
		})
	}
	for _, proxy := range plan.Proxies {
		secrets = append(secrets, proxy.Username, proxy.Password)
		out.Proxies = append(out.Proxies, importProxy{
			Kind: proxy.Kind,
			URL:  sanitizeImportURL(proxy.URL),
			HasAuth: strings.TrimSpace(proxy.Username) != "" ||
				strings.TrimSpace(proxy.Password) != "",
		})
	}
	out.Warnings = redactImportWarnings(plan.Warnings, secrets...)
	return out
}

// validateImportURL accepts only an absolute http or https URL and returns a
// normalized form. The raw value is never echoed in errors: it may embed
// basic-auth credentials.
func validateImportURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("url is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("url must be a valid URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("url must use http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("url must include a host")
	}
	parsed.Scheme = scheme
	return parsed.String(), nil
}

// sanitizeImportURL removes userinfo, fragments and credential-bearing query
// parameters from a URL before it is shown by the API.
func sanitizeImportURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.Fragment = ""
	query := parsed.Query()
	changed := false
	for key := range query {
		if isImportSecretParam(key) {
			query.Del(key)
			changed = true
		}
	}
	if changed {
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

func isImportSecretParam(name string) bool {
	normalized := strings.ToLower(strings.TrimSpace(name))
	normalized = strings.NewReplacer("_", "", "-", "", ".", "").Replace(normalized)
	switch normalized {
	case "apikey", "apitoken", "passkey", "password", "passwd", "token",
		"secret", "rsskey", "authkey", "cookie", "credentials":
		return true
	}
	return strings.Contains(normalized, "apikey") ||
		strings.Contains(normalized, "passkey") ||
		strings.Contains(normalized, "token") ||
		strings.Contains(normalized, "secret") ||
		strings.Contains(normalized, "password")
}

// redactImportWarnings removes every known secret and its URL-escaped form
// from importer warnings. Longer values are replaced first.
func redactImportWarnings(warnings []string, secrets ...string) []string {
	unique := make(map[string]struct{}, len(secrets)*2)
	values := make([]string, 0, len(secrets)*2)
	for _, secret := range secrets {
		for _, candidate := range []string{secret, url.QueryEscape(secret)} {
			if candidate == "" {
				continue
			}
			if _, exists := unique[candidate]; exists {
				continue
			}
			unique[candidate] = struct{}{}
			values = append(values, candidate)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })

	out := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		for _, secret := range values {
			warning = strings.ReplaceAll(warning, secret, "[redacted]")
		}
		out = append(out, warning)
	}
	return out
}

func nonNilInts(values []int) []int {
	if values == nil {
		return []int{}
	}
	return values
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
