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
	"github.com/buzzqw/indexarr/internal/legacyimport"
)

// importTimeout bounds a Prowlarr HTTP import and a Jackett directory scan.
const importTimeout = 20 * time.Second

type prowlarrImportRequest struct {
	URL    string `json:"url"`
	APIKey string `json:"api_key"`
}

type jackettImportRequest struct {
	Path string `json:"path"`
}

type importIndexer struct {
	Name           string   `json:"name"`
	Implementation string   `json:"implementation"`
	Protocol       string   `json:"protocol"`
	DefinitionHint string   `json:"definition_hint"`
	BaseURL        string   `json:"base_url"`
	Categories     []int    `json:"categories"`
	Tags           []string `json:"tags"`
	Priority       int      `json:"priority"`
	Enabled        bool     `json:"enabled"`
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
	Warnings     []string            `json:"warnings"`
}

type legacyPreviewCounts struct {
	Native  int `json:"native"`
	Skipped int `json:"skipped"`
}

type legacyPreviewResponse struct {
	Candidates []legacyimport.Candidate `json:"candidates"`
	Counts     legacyPreviewCounts      `json:"counts"`
}

type legacyApplyRequest struct {
	SourceIDs []int64 `json:"source_ids"`
	DryRun    bool    `json:"dry_run"`
}

type legacyRevertRequest struct {
	SourceIDs []int64 `json:"source_ids"`
}

type legacyActionResponse struct {
	DryRun  bool               `json:"dry_run"`
	Results []legacyResultItem `json:"results"`
}

type legacyResultItem struct {
	SourceID     int64  `json:"source_id"`
	Name         string `json:"name"`
	DefinitionID string `json:"definition_id,omitempty"`
	IndexerID    int64  `json:"indexer_id,omitempty"`
	Applied      bool   `json:"applied"`
	Skipped      bool   `json:"skipped"`
	Reason       string `json:"reason,omitempty"`
	Error        string `json:"error,omitempty"`
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
	writeJSON(w, http.StatusOK, importPlanResponse(plan, apiKey))
}

func (s *Server) handleImportJackett(w http.ResponseWriter, r *http.Request) {
	var req jackettImportRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	rawPath := strings.TrimSpace(req.Path)
	if rawPath == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	path := filepath.Clean(rawPath)
	if !filepath.IsAbs(path) {
		writeError(w, http.StatusBadRequest, "path must be absolute")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), importTimeout)
	defer cancel()
	plan, err := importers.FromJackettConfig(ctx, path)
	if err != nil {
		s.logger.Warn("adminapi: jackett import failed", "error", err)
		writeError(w, http.StatusBadGateway, "jackett import failed")
		return
	}
	writeJSON(w, http.StatusOK, importPlanResponse(plan))
}

func (s *Server) handleLegacyPreview(w http.ResponseWriter, r *http.Request) {
	candidates, err := legacyimport.Preview(r.Context(), s.store)
	if err != nil {
		s.logger.Error("adminapi: legacy preview", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	native, skipped := legacyimport.Counts(candidates)
	writeJSON(w, http.StatusOK, legacyPreviewResponse{
		Candidates: candidates,
		Counts:     legacyPreviewCounts{Native: native, Skipped: skipped},
	})
}

func (s *Server) handleLegacyApply(w http.ResponseWriter, r *http.Request) {
	var req legacyApplyRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	if err := validateLegacySourceIDs(req.SourceIDs, false); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Without a vault, refuse the whole request before writing anything when
	// any selected source needs sealed credentials. A 503 must not follow a
	// partial apply of the other sources.
	if s.vault == nil {
		preflight, err := legacyimport.Apply(r.Context(), s.store, nil, legacyimport.ApplyOptions{
			SourceIDs: req.SourceIDs,
			DryRun:    true,
		})
		if err != nil {
			s.logger.Error("adminapi: legacy apply preflight", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		for _, result := range preflight {
			if errors.Is(result.Err, legacyimport.ErrVaultRequired) {
				writeError(w, http.StatusServiceUnavailable, "vault not configured")
				return
			}
		}
	}

	results, err := legacyimport.Apply(r.Context(), s.store, s.vault, legacyimport.ApplyOptions{
		SourceIDs: req.SourceIDs,
		DryRun:    req.DryRun,
	})
	if err != nil {
		s.logger.Error("adminapi: legacy apply", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, legacyActionResponse{
		DryRun:  req.DryRun,
		Results: legacyResultItems(results),
	})
}

func (s *Server) handleLegacyRevert(w http.ResponseWriter, r *http.Request) {
	var req legacyRevertRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	if err := validateLegacySourceIDs(req.SourceIDs, true); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	results, err := legacyimport.Revert(r.Context(), s.store, req.SourceIDs)
	if err != nil {
		s.logger.Error("adminapi: legacy revert", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, legacyActionResponse{
		Results: legacyResultItems(results),
	})
}

// validateLegacySourceIDs rejects non-positive ids. Apply accepts an empty
// list (every source); revert requires at least one id.
func validateLegacySourceIDs(ids []int64, required bool) error {
	if required && len(ids) == 0 {
		return errors.New("source_ids is required")
	}
	for _, id := range ids {
		if id <= 0 {
			return errors.New("source_ids must contain positive ids")
		}
	}
	return nil
}

// legacyResultItems converts results to the wire shape. Errors are already
// redacted by legacyimport; the raw error value never leaves the process.
func legacyResultItems(results []legacyimport.ApplyResult) []legacyResultItem {
	out := make([]legacyResultItem, 0, len(results))
	for _, result := range results {
		item := legacyResultItem{
			SourceID:     result.SourceID,
			Name:         result.Name,
			DefinitionID: result.DefinitionID,
			IndexerID:    result.IndexerID,
			Applied:      result.Applied,
			Skipped:      result.Skipped,
			Reason:       result.Reason,
		}
		if result.Err != nil {
			item.Error = result.Err.Error()
		}
		out = append(out, item)
	}
	return out
}

// importPlanResponse converts an importers.Plan into a response that never
// carries an API key, username, password or credential-bearing URL. Credentials
// are reported only as booleans, and warnings are redacted once more before
// leaving the process.
func importPlanResponse(plan importers.Plan, extraSecrets ...string) importResponse {
	out := importResponse{
		Source:       plan.Source,
		Indexers:     make([]importIndexer, 0, len(plan.Indexers)),
		Applications: make([]importApplication, 0, len(plan.Applications)),
		Proxies:      make([]importProxy, 0, len(plan.Proxies)),
		Warnings:     make([]string, 0, len(plan.Warnings)),
	}

	secrets := append([]string(nil), extraSecrets...)
	for _, in := range plan.Indexers {
		secrets = append(secrets, in.APIKey, in.Username, in.Password)
		out.Indexers = append(out.Indexers, importIndexer{
			Name:           in.Name,
			Implementation: in.Implementation,
			Protocol:       string(in.Protocol),
			DefinitionHint: in.DefinitionHint,
			BaseURL:        sanitizeImportURL(in.BaseURL),
			Categories:     nonNilInts(in.Categories),
			Tags:           nonNilStrings(in.Tags),
			Priority:       in.Priority,
			Enabled:        in.Enabled,
			HasKey:         strings.TrimSpace(in.APIKey) != "",
			HasBasicAuth: strings.TrimSpace(in.Username) != "" ||
				strings.TrimSpace(in.Password) != "",
		})
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
