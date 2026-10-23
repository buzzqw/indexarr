package adminapi

import (
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/appsync"
	"github.com/buzzqw/indexarr/internal/store"
)

// applicationRequest is the create/update payload. api_key is accepted on
// update only when non-empty; an empty value keeps the stored key.
type applicationRequest struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	APIKey    string `json:"api_key"`
	SyncLevel string `json:"sync_level"`
}

// applicationResponse is the public view of an application. The API key is
// represented only by has_api_key: neither the sealed blob nor the plaintext
// is ever returned.
type applicationResponse struct {
	ID         int64     `json:"id"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	URL        string    `json:"url"`
	SyncLevel  string    `json:"sync_level"`
	LastSyncAt time.Time `json:"last_sync_at"`
	LastError  string    `json:"last_error"`
	HasAPIKey  bool      `json:"has_api_key"`
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
		ID:         app.ID,
		Kind:       app.Kind,
		Name:       app.Name,
		URL:        app.URL,
		SyncLevel:  app.SyncLevel,
		LastSyncAt: app.LastSyncAt,
		LastError:  app.LastError,
		HasAPIKey:  len(app.APIKeyEnc) > 0,
	}
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
		Kind:      kind,
		Name:      name,
		URL:       appURL,
		APIKeyEnc: []byte(sealed),
		SyncLevel: level,
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
