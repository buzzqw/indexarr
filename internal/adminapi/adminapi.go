// Package adminapi implements the versioned, machine-facing JSON API of
// indexarr 2.
//
// The API lives under /api/v2 and is authenticated with the existing API keys
// (the primary key from settings plus the revocable keys in api_keys). It uses
// no cookies and no CSRF tokens, so it can be called by scripts and other
// services. Every response is JSON; internal failures are logged through slog
// and never include request bodies or secret values.
package adminapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/store"
	"github.com/buzzqw/indexarr/internal/vault"
)

// maxBodyBytes is the hard limit applied to every JSON request body.
const maxBodyBytes = 1 << 20

// Config configures the admin API server.
type Config struct {
	Store   *store.Store
	Vault   *vault.Vault
	Manager *indexer.Manager // optional: enables test/search/run
	Logger  *slog.Logger
	Version string // optional, reported by /api/v2/health
}

// Server serves the versioned admin API.
type Server struct {
	store   *store.Store
	vault   *vault.Vault
	manager *indexer.Manager
	logger  *slog.Logger
	version string
}

// New builds an admin API server. A store is required; every other field is
// optional.
func New(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, errors.New("adminapi: store is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		store:   cfg.Store,
		vault:   cfg.Vault,
		manager: cfg.Manager,
		logger:  logger,
		version: strings.TrimSpace(cfg.Version),
	}, nil
}

// Handler returns the HTTP handler with every /api/v2 route and the API key
// middleware. The web server mounts it at the root of its own mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v2/health", s.handleHealth)

	mux.HandleFunc("GET /api/v2/definitions", s.handleDefinitionList)
	mux.HandleFunc("POST /api/v2/definitions", s.handleDefinitionCreate)
	mux.HandleFunc("POST /api/v2/definitions/validate", s.handleDefinitionValidate)
	mux.HandleFunc("GET /api/v2/definitions/{id}", s.handleDefinitionGet)
	mux.HandleFunc("PUT /api/v2/definitions/{id}", s.handleDefinitionUpdate)
	mux.HandleFunc("DELETE /api/v2/definitions/{id}", s.handleDefinitionDelete)

	mux.HandleFunc("GET /api/v2/catalog", s.handleCatalogList)
	mux.HandleFunc("POST /api/v2/catalog/sync", s.handleCatalogSync)

	mux.HandleFunc("GET /api/v2/indexers", s.handleIndexerList)
	mux.HandleFunc("POST /api/v2/indexers", s.handleIndexerCreate)
	mux.HandleFunc("GET /api/v2/indexers/{id}", s.handleIndexerGet)
	mux.HandleFunc("PUT /api/v2/indexers/{id}", s.handleIndexerUpdate)
	mux.HandleFunc("DELETE /api/v2/indexers/{id}", s.handleIndexerDelete)
	mux.HandleFunc("POST /api/v2/indexers/{id}/test", s.handleIndexerTest)
	mux.HandleFunc("POST /api/v2/indexers/{id}/search", s.handleIndexerSearch)
	mux.HandleFunc("POST /api/v2/indexers/{id}/run", s.handleIndexerRun)

	// JSON for unknown routes, so clients never receive an HTML body.
	notFound := func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	}
	mux.HandleFunc("/api/v2/", notFound)
	mux.HandleFunc("/api/v2", notFound)

	return s.authenticate(mux)
}

// authenticate accepts apikey (query), X-Api-Key or Authorization: Bearer.
// The primary key is compared in constant time; the additional keys are looked
// up in the store and touched on success.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		settings, err := s.store.LoadSettings(ctx)
		if err != nil {
			s.logger.Error("adminapi: load settings", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if settings.APIKey == "" {
			writeError(w, http.StatusServiceUnavailable, "api key not configured")
			return
		}

		key := requestAPIKey(r)
		if key == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if subtle.ConstantTimeCompare([]byte(key), []byte(settings.APIKey)) == 1 {
			next.ServeHTTP(w, r)
			return
		}

		secondary, ok, err := s.store.LookupAPIKey(ctx, key)
		if err != nil {
			s.logger.Error("adminapi: lookup api key", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if err := s.store.TouchAPIKey(ctx, secondary.ID); err != nil {
			s.logger.Warn("adminapi: touch api key", "error", err)
		}
		next.ServeHTTP(w, r)
	})
}

func requestAPIKey(r *http.Request) string {
	if key := strings.TrimSpace(r.URL.Query().Get("apikey")); key != "" {
		return key
	}
	if key := strings.TrimSpace(r.Header.Get("X-Api-Key")); key != "" {
		return key
	}
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(authorization) > len("Bearer ") &&
		strings.EqualFold(authorization[:len("Bearer ")], "Bearer ") {
		return strings.TrimSpace(authorization[len("Bearer "):])
	}
	return ""
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{Status: "ok", Version: s.version})
}

type healthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version,omitempty"`
}

// decodeRequest applies the body limit and strict JSON decoding. It writes a
// JSON 400 and reports false when the request cannot be decoded.
func decodeRequest(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusBadRequest, "request body too large")
			return false
		}
		if errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "request body is empty")
			return false
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			writeError(w, http.StatusBadRequest, "request body must contain a single JSON value")
		} else {
			writeError(w, http.StatusBadRequest, err.Error())
		}
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// indexerID parses the {id} path parameter.
func indexerID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}
