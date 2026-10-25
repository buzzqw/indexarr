package adminapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
)

// Prowlarr-compatible read-only endpoints for dashboards (Homepage,
// Organizr, …). They expose the same information a dashboard reads from
// Prowlarr: system status, health and the indexer list. They never accept
// writes and never proxy a search to a manager.

type prowlarrIndexer struct {
	ID             int64              `json:"id"`
	Name           string             `json:"name"`
	Implementation string             `json:"implementation"`
	ConfigContract string             `json:"configContract"`
	InfoLink       string             `json:"infoLink,omitempty"`
	Priority       int                `json:"priority"`
	Enable         bool               `json:"enable"`
	Protocol       string             `json:"protocol"`
	Tags           []int              `json:"tags"`
	Fields         []prowlarrField    `json:"fields"`
	Capabilities   prowlarrCapability `json:"capabilities"`
	SupportsRSS    bool               `json:"supportsRss"`
	SupportsSearch bool               `json:"supportsSearch"`
}

type prowlarrField struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

type prowlarrCapability struct {
	Categories        []prowlarrCategory `json:"categories"`
	SupportsRawSearch bool               `json:"supportsRawSearch"`
	SearchParams      []string           `json:"searchParams"`
}

type prowlarrCategory struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func (s *Server) handleProwlarrSystemStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"appName":        "indexarr",
		"version":        s.version,
		"branch":         "main",
		"authentication": "apikey",
		"startTime":      time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleProwlarrHealth(w http.ResponseWriter, r *http.Request) {
	// Prowlarr returns an array of health issues. indexarr has no issue to
	// report here; the detailed health lives in /api/v2/health.
	writeJSON(w, http.StatusOK, []any{})
}

func (s *Server) handleProwlarrIndexerList(w http.ResponseWriter, r *http.Request) {
	indexers, err := s.store.ListIndexers(r.Context())
	if err != nil {
		s.logger.Error("adminapi: prowlarr indexer list", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]prowlarrIndexer, 0, len(indexers))
	for _, indexer := range indexers {
		out = append(out, s.prowlarrIndexer(r, indexer))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleProwlarrIndexerGet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid indexer id")
		return
	}
	indexer, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "indexer not found")
		return
	}
	writeJSON(w, http.StatusOK, s.prowlarrIndexer(r, indexer))
}

func (s *Server) prowlarrIndexer(r *http.Request, indexer store.Indexer) prowlarrIndexer {
	implementation, contract := "Torznab", "TorznabSettings"
	if strings.EqualFold(strings.TrimSpace(indexer.Protocol), "usenet") {
		implementation, contract = "Newznab", "NewznabSettings"
	}
	base := publicBase(r) + "/torznab/" + indexer.Slug
	categories := make([]prowlarrCategory, 0)
	if def, err := s.store.GetDefinition(r.Context(), indexer.DefinitionID); err == nil {
		if parsed, err := definition.Parse([]byte(def.Body)); err == nil {
			seen := map[int]bool{}
			for _, category := range parsed.Categories {
				if category.Standard <= 0 || seen[category.Standard] {
					continue
				}
				seen[category.Standard] = true
				categories = append(categories, prowlarrCategory{ID: category.Standard, Name: category.Desc})
			}
		}
	}
	fields := []prowlarrField{
		{Name: "baseUrl", Value: base},
		{Name: "apiPath", Value: "/api"},
		{Name: "categories", Value: joinIntsCSV(indexerCategories(indexer))},
	}
	return prowlarrIndexer{
		ID: indexer.ID, Name: indexer.Name, Implementation: implementation,
		ConfigContract: contract, Priority: indexer.Priority, Enable: indexer.Enabled,
		Protocol: indexer.Protocol, Tags: []int{}, Fields: fields,
		Capabilities: prowlarrCapability{
			Categories: categories, SupportsRawSearch: true, SearchParams: []string{"q"},
		},
		SupportsRSS: true, SupportsSearch: true,
	}
}

func publicBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = strings.ToLower(strings.Split(forwarded, ",")[0])
	}
	host := strings.TrimSpace(r.Host)
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Host")); forwarded != "" {
		host = strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return scheme + "://" + host
}

func joinIntsCSV(values []int) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

func indexerCategories(indexer store.Indexer) []int {
	var config map[string]any
	if err := json.Unmarshal([]byte(indexer.ConfigJSON), &config); err != nil {
		return nil
	}
	raw, _ := config["categories"].(string)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []int
	for _, part := range strings.Split(raw, ",") {
		if value, err := strconv.Atoi(strings.TrimSpace(part)); err == nil && value > 0 {
			out = append(out, value)
		}
	}
	return out
}
