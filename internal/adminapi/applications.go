package adminapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/appsync"
	"github.com/buzzqw/indexarr/internal/store"
)

// applicationRequest is the create/update payload. api_key is accepted on
// update only when non-empty; an empty value keeps the stored key. Pointer
// profile fields distinguish an omitted value ("keep the current one") from an
// explicit zero.
type applicationRequest struct {
	Kind                    string        `json:"kind"`
	Name                    string        `json:"name"`
	URL                     string        `json:"url"`
	APIKey                  string        `json:"api_key"`
	SyncLevel               string        `json:"sync_level"`
	EnableRSS               *bool         `json:"enable_rss"`
	EnableAutomaticSearch   *bool         `json:"enable_automatic_search"`
	EnableInteractiveSearch *bool         `json:"enable_interactive_search"`
	MinSeeders              *int          `json:"min_seeders"`
	Categories              *categoryList `json:"categories"`
}

// categoryList accepts both a JSON array of ids and the comma-separated form
// used by the web form, so an API client can send either.
type categoryList []int

func (c *categoryList) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*c = nil
		return nil
	}
	if strings.HasPrefix(raw, `"`) {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		ids, err := parseCategoryList(text)
		if err != nil {
			return err
		}
		*c = ids
		return nil
	}
	var ids []int
	if err := json.Unmarshal(data, &ids); err != nil {
		return err
	}
	*c = ids
	return nil
}

// applicationResponse is the public view of an application. The API key is
// represented only by has_api_key: neither the sealed blob nor the plaintext
// is ever returned.
type applicationResponse struct {
	ID                      int64     `json:"id"`
	Kind                    string    `json:"kind"`
	Name                    string    `json:"name"`
	URL                     string    `json:"url"`
	SyncLevel               string    `json:"sync_level"`
	EnableRSS               bool      `json:"enable_rss"`
	EnableAutomaticSearch   bool      `json:"enable_automatic_search"`
	EnableInteractiveSearch bool      `json:"enable_interactive_search"`
	MinSeeders              int       `json:"min_seeders"`
	Categories              []int     `json:"categories"`
	LastSyncAt              time.Time `json:"last_sync_at"`
	LastError               string    `json:"last_error"`
	HasAPIKey               bool      `json:"has_api_key"`
}

type applicationSyncResponse struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Deleted int `json:"deleted"`
	Skipped int `json:"skipped"`
}

type applicationSyncAllResult struct {
	ApplicationID int64  `json:"application_id"`
	Name          string `json:"name"`
	Added         int    `json:"added"`
	Updated       int    `json:"updated"`
	Deleted       int    `json:"deleted"`
	Skipped       int    `json:"skipped"`
	Error         string `json:"error,omitempty"`
}

func applicationFromStore(app store.Application) applicationResponse {
	return applicationResponse{
		ID:                      app.ID,
		Kind:                    app.Kind,
		Name:                    app.Name,
		URL:                     app.URL,
		SyncLevel:               app.SyncLevel,
		EnableRSS:               app.EnableRSS,
		EnableAutomaticSearch:   app.EnableAutomaticSearch,
		EnableInteractiveSearch: app.EnableInteractiveSearch,
		MinSeeders:              app.MinSeeders,
		Categories:              storedCategories(app.Categories),
		LastSyncAt:              app.LastSyncAt,
		LastError:               app.LastError,
		HasAPIKey:               len(app.APIKeyEnc) > 0,
	}
}

// parseCategoryList validates and normalizes the standard Torznab ids sent by
// an API client. It accepts the comma-separated form too, mirroring the web
// form. Negative and non-numeric ids are rejected.
func parseCategoryList(raw string) ([]int, error) {
	seen := make(map[int]bool)
	out := make([]int, 0, 8)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil || id < 0 {
			return nil, errors.New("category ids must be non-negative integers")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Ints(out)
	return out, nil
}

// normalizeCategoryIDs validates the ids in a request and returns a stable,
// duplicate-free list.
func normalizeCategoryIDs(ids []int) ([]int, error) {
	seen := make(map[int]bool, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id < 0 {
			return nil, errors.New("category ids must be non-negative integers")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Ints(out)
	return out, nil
}

// storedCategories parses applications.categories. A malformed value is
// ignored rather than failing the whole API response; the web and API writers
// validate it before saving.
func storedCategories(raw string) []int {
	ids, err := parseCategoryList(raw)
	if err != nil {
		return []int{}
	}
	return ids
}

func categoryIDsCSV(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}

func (s *Server) handleApplicationList(w http.ResponseWriter, r *http.Request) {
	apps, err := s.store.ListApplications(r.Context())
	if err != nil {
		s.logger.Error("adminapi: list applications", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]applicationResponse, 0, len(apps))
	for _, app := range apps {
		out = append(out, applicationFromStore(app))
	}
	writeJSON(w, http.StatusOK, map[string]any{"applications": out})
}

func (s *Server) handleApplicationGet(w http.ResponseWriter, r *http.Request) {
	id, ok := applicationID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid application id")
		return
	}
	app, err := s.store.GetApplication(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		s.logger.Error("adminapi: get application", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, applicationFromStore(app))
}

func (s *Server) handleApplicationCreate(w http.ResponseWriter, r *http.Request) {
	var req applicationRequest
	if !decodeRequest(w, r, &req) {
		return
	}

	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if !appsync.Kind(kind).Valid() {
		writeError(w, http.StatusBadRequest, "invalid application kind")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	appURL, err := normalizeApplicationURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	level, ok := normalizeSyncLevel(req.SyncLevel, true)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid sync_level")
		return
	}
	enableRSS, enableAutomaticSearch, enableInteractiveSearch := true, true, true
	if req.EnableRSS != nil {
		enableRSS = *req.EnableRSS
	}
	if req.EnableAutomaticSearch != nil {
		enableAutomaticSearch = *req.EnableAutomaticSearch
	}
	if req.EnableInteractiveSearch != nil {
		enableInteractiveSearch = *req.EnableInteractiveSearch
	}
	minSeeders := 0
	if req.MinSeeders != nil {
		minSeeders = *req.MinSeeders
	}
	if minSeeders < 0 {
		writeError(w, http.StatusBadRequest, "min_seeders must be zero or positive")
		return
	}
	var categories []int
	if req.Categories != nil {
		var err error
		categories, err = normalizeCategoryIDs(*req.Categories)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" {
		writeError(w, http.StatusBadRequest, "api_key is required")
		return
	}
	if s.vault == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}
	sealed, err := s.vault.SealString(apiKey)
	if err != nil {
		s.logger.Error("adminapi: seal application api key", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	ctx := r.Context()
	id, err := s.store.CreateApplication(ctx, store.Application{
		Kind:                    kind,
		Name:                    name,
		URL:                     appURL,
		APIKeyEnc:               []byte(sealed),
		SyncLevel:               level,
		EnableRSS:               enableRSS,
		EnableAutomaticSearch:   enableAutomaticSearch,
		EnableInteractiveSearch: enableInteractiveSearch,
		MinSeeders:              minSeeders,
		Categories:              categoryIDsCSV(categories),
	})
	if err != nil {
		s.logger.Error("adminapi: create application", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	app, err := s.store.GetApplication(ctx, id)
	if err != nil {
		s.logger.Error("adminapi: get created application", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, applicationFromStore(app))
}

func (s *Server) handleApplicationUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := applicationID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid application id")
		return
	}
	var req applicationRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	ctx := r.Context()

	app, err := s.store.GetApplication(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		s.logger.Error("adminapi: get application for update", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if raw := strings.TrimSpace(req.Kind); raw != "" {
		kind := strings.ToLower(raw)
		if !appsync.Kind(kind).Valid() {
			writeError(w, http.StatusBadRequest, "invalid application kind")
			return
		}
		app.Kind = kind
	}
	if name := strings.TrimSpace(req.Name); name != "" {
		app.Name = name
	}
	if raw := strings.TrimSpace(req.URL); raw != "" {
		appURL, err := normalizeApplicationURL(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		app.URL = appURL
	}
	if raw := strings.TrimSpace(req.SyncLevel); raw != "" {
		level, ok := normalizeSyncLevel(raw, false)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid sync_level")
			return
		}
		app.SyncLevel = level
	}
	if req.EnableRSS != nil {
		app.EnableRSS = *req.EnableRSS
	}
	if req.EnableAutomaticSearch != nil {
		app.EnableAutomaticSearch = *req.EnableAutomaticSearch
	}
	if req.EnableInteractiveSearch != nil {
		app.EnableInteractiveSearch = *req.EnableInteractiveSearch
	}
	if req.MinSeeders != nil {
		if *req.MinSeeders < 0 {
			writeError(w, http.StatusBadRequest, "min_seeders must be zero or positive")
			return
		}
		app.MinSeeders = *req.MinSeeders
	}
	if req.Categories != nil {
		categories, err := normalizeCategoryIDs(*req.Categories)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		app.Categories = categoryIDsCSV(categories)
	}
	// An empty api_key on update means "keep the current key". The key can
	// never be read back through the API.
	if apiKey := strings.TrimSpace(req.APIKey); apiKey != "" {
		if s.vault == nil {
			writeError(w, http.StatusServiceUnavailable, "vault not configured")
			return
		}
		sealed, err := s.vault.SealString(apiKey)
		if err != nil {
			s.logger.Error("adminapi: seal application api key", "id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		app.APIKeyEnc = []byte(sealed)
	}

	if err := s.store.UpdateApplication(ctx, app); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		s.logger.Error("adminapi: update application", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	updated, err := s.store.GetApplication(ctx, id)
	if err != nil {
		s.logger.Error("adminapi: get updated application", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, applicationFromStore(updated))
}

func (s *Server) handleApplicationDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := applicationID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid application id")
		return
	}
	ctx := r.Context()
	if _, err := s.store.GetApplication(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		s.logger.Error("adminapi: get application for delete", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := s.store.DeleteApplication(ctx, id); err != nil {
		s.logger.Error("adminapi: delete application", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeNoContent(w)
}

func (s *Server) handleApplicationSync(w http.ResponseWriter, r *http.Request) {
	id, ok := applicationID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid application id")
		return
	}
	if s.syncService == nil {
		writeError(w, http.StatusServiceUnavailable, "sync service not configured")
		return
	}
	if _, err := s.store.GetApplication(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "application not found")
			return
		}
		s.logger.Error("adminapi: get application for sync", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	result, err := s.syncService.SyncApp(r.Context(), id)
	if err != nil {
		s.logger.Error("adminapi: sync application", "id", id, "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":   err.Error(),
			"added":   result.Added,
			"updated": result.Updated,
			"deleted": result.Deleted,
			"skipped": result.Skipped,
		})
		return
	}
	writeJSON(w, http.StatusOK, applicationSyncResponse{
		Added:   result.Added,
		Updated: result.Updated,
		Deleted: result.Deleted,
		Skipped: result.Skipped,
	})
}

func (s *Server) handleApplicationSyncAll(w http.ResponseWriter, r *http.Request) {
	if s.syncService == nil {
		writeError(w, http.StatusServiceUnavailable, "sync service not configured")
		return
	}
	results := s.syncService.SyncAll(r.Context())
	out := make([]applicationSyncAllResult, 0, len(results))
	for _, result := range results {
		item := applicationSyncAllResult{
			ApplicationID: result.ApplicationID,
			Name:          result.Name,
			Added:         result.Added,
			Updated:       result.Updated,
			Deleted:       result.Deleted,
			Skipped:       result.Skipped,
		}
		if result.Err != nil {
			item.Error = result.Err.Error()
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// applicationID parses the {id} path parameter.
func applicationID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// normalizeApplicationURL validates an application root URL and removes a
// trailing slash, query string and fragment, matching appsync.
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

// normalizeSyncLevel validates a store sync level. An empty value means "full"
// on create and "leave unchanged" on update.
func normalizeSyncLevel(raw string, create bool) (string, bool) {
	level := strings.ToLower(strings.TrimSpace(raw))
	if level == "" {
		if create {
			return "full", true
		}
		return "", true
	}
	switch level {
	case "disabled", "add", "add-remove", "full":
		return level, true
	default:
		return "", false
	}
}
