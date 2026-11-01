package adminapi

import (
	"net/http"
	"strings"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/notify"
)

type catalogEntry struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Author    string `json:"author"`
	Hash      string `json:"hash"`
	Installed bool   `json:"installed"`
}

func (s *Server) handleCatalogList(w http.ResponseWriter, r *http.Request) {
	entries, err := catalog.Builtins()
	if err != nil {
		s.logger.Error("adminapi: load catalog", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defs, err := s.store.ListDefinitions(r.Context())
	if err != nil {
		s.logger.Error("adminapi: list definitions for catalog", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	installed := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		installed[def.ID] = struct{}{}
	}

	out := make([]catalogEntry, 0, len(entries))
	for _, entry := range entries {
		_, ok := installed[entry.ID]
		out = append(out, catalogEntry{
			ID:        entry.ID,
			State:     entry.State,
			Author:    entry.Author,
			Hash:      entry.Hash,
			Installed: ok,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalog": out})
}

// catalogImportRequest is the body of a remote catalog read. The address is
// the directory that holds index.json and definitions/, for example
// https://raw.githubusercontent.com/buzzqw/indexarr/main/internal/catalog
type catalogImportRequest struct {
	URL string `json:"url"`
}

// handleCatalogImport reads a catalog from an address and stores its
// definitions, so a site fix reaches an installation without a new release.
// The operator's own definitions (origin "local") are never touched, and the
// embedded catalog never overwrites what this endpoint wrote (origin
// "remote").
func (s *Server) handleCatalogImport(w http.ResponseWriter, r *http.Request) {
	var request catalogImportRequest
	if !decodeRequest(w, r, &request) {
		return
	}
	base := strings.TrimSpace(request.URL)
	if base == "" {
		writeError(w, http.StatusBadRequest, "url is required")
		return
	}
	result, err := catalog.Import(r.Context(), s.store, base)
	if err != nil {
		s.logger.Warn("adminapi: import catalog", "error", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if result.Written > 0 && s.notifier != nil {
		if settings, settingsErr := s.store.LoadSettings(r.Context()); settingsErr == nil {
			s.notifier.DispatchAsync(r.Context(), settings, notify.Event{
				Kind:   notify.KindCatalogUpdated,
				Source: "remote catalog",
				Count:  result.Written,
				Path:   "/settings?section=advanced",
			})
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCatalogSync(w http.ResponseWriter, r *http.Request) {
	written, err := catalog.Sync(r.Context(), s.store)
	if err != nil {
		s.logger.Error("adminapi: sync catalog", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if written > 0 && s.notifier != nil {
		settings, err := s.store.LoadSettings(r.Context())
		if err == nil {
			s.notifier.DispatchAsync(r.Context(), settings, notify.Event{
				Kind:   notify.KindCatalogUpdated,
				Source: "admin API",
				Count:  written,
				Path:   "/settings?section=advanced",
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"written": written})
}
