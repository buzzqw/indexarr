package adminapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/store"
)

type definitionRequest struct {
	Body string `json:"body"`
}

type definitionMeta struct {
	ID            string    `json:"id"`
	SchemaVersion int       `json:"schema_version"`
	Family        string    `json:"family"`
	State         string    `json:"state"`
	Origin        string    `json:"origin"`
	Author        string    `json:"author"`
	Hash          string    `json:"hash"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type definitionDetail struct {
	definitionMeta
	Body string `json:"body"`
}

func metaFromStore(def store.Definition) definitionMeta {
	return definitionMeta{
		ID:            def.ID,
		SchemaVersion: def.SchemaVersion,
		Family:        def.Family,
		State:         def.State,
		Origin:        def.Origin,
		Author:        def.Author,
		Hash:          def.Hash,
		UpdatedAt:     def.UpdatedAt,
	}
}

func (s *Server) handleDefinitionList(w http.ResponseWriter, r *http.Request) {
	defs, err := s.store.ListDefinitions(r.Context())
	if err != nil {
		s.logger.Error("adminapi: list definitions", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]definitionMeta, 0, len(defs))
	for _, def := range defs {
		out = append(out, metaFromStore(def))
	}
	writeJSON(w, http.StatusOK, map[string]any{"definitions": out})
}

func (s *Server) handleDefinitionGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	def, err := s.store.GetDefinition(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "definition not found")
			return
		}
		s.logger.Error("adminapi: get definition", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, definitionDetail{
		definitionMeta: metaFromStore(def),
		Body:           def.Body,
	})
}

func (s *Server) handleDefinitionCreate(w http.ResponseWriter, r *http.Request) {
	var req definitionRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	def, err := definition.Parse([]byte(req.Body))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":  "invalid definition",
			"errors": validationProblems(err),
		})
		return
	}

	ctx := r.Context()
	_, err = s.store.GetDefinition(ctx, def.Meta.ID)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.logger.Error("adminapi: check definition", "id", def.Meta.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if exists && !overwriteRequested(r) {
		writeError(w, http.StatusConflict, "definition already exists")
		return
	}

	stored := store.Definition{
		ID:            def.Meta.ID,
		SchemaVersion: def.Schema,
		Family:        def.Connector,
		State:         "bozza",
		Origin:        "local",
		Author:        "api",
		Body:          req.Body,
		Hash:          definition.Fingerprint([]byte(req.Body)),
		UpdatedAt:     time.Now().UTC(),
	}
	if err := s.store.UpsertDefinition(ctx, stored); err != nil {
		s.logger.Error("adminapi: create definition", "id", stored.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, metaFromStore(stored))
}

func (s *Server) handleDefinitionUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req definitionRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	def, err := definition.Parse([]byte(req.Body))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":  "invalid definition",
			"errors": validationProblems(err),
		})
		return
	}
	if def.Meta.ID != id {
		writeError(w, http.StatusBadRequest, "definition id in body does not match path")
		return
	}

	ctx := r.Context()
	existing, err := s.store.GetDefinition(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "definition not found")
			return
		}
		s.logger.Error("adminapi: get definition for update", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	existing.SchemaVersion = def.Schema
	existing.Family = def.Connector
	existing.Body = req.Body
	existing.Hash = definition.Fingerprint([]byte(req.Body))
	existing.UpdatedAt = time.Now().UTC()
	// An edited built-in definition becomes local so catalog sync does not
	// overwrite the user's changes.
	existing.Origin = "local"
	if existing.State == "" {
		existing.State = "bozza"
	}
	if existing.Author == "" {
		existing.Author = "api"
	}
	if err := s.store.UpsertDefinition(ctx, existing); err != nil {
		s.logger.Error("adminapi: update definition", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, metaFromStore(existing))
}

func (s *Server) handleDefinitionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	if _, err := s.store.GetDefinition(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "definition not found")
			return
		}
		s.logger.Error("adminapi: get definition for delete", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		s.logger.Error("adminapi: list indexers for definition delete", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	for _, in := range indexers {
		if in.DefinitionID == id {
			writeError(w, http.StatusConflict, "definition is referenced by indexers")
			return
		}
	}

	// The store has no DeleteDefinition helper. The pre-check above guarantees
	// the row is not referenced; the FK is kept as a second line of defence.
	if _, err := s.store.DB().ExecContext(ctx, `DELETE FROM definitions WHERE id = ?`, id); err != nil {
		if isConstraintError(err) {
			writeError(w, http.StatusConflict, "definition is referenced by indexers")
			return
		}
		s.logger.Error("adminapi: delete definition", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeNoContent(w)
}

func (s *Server) handleDefinitionValidate(w http.ResponseWriter, r *http.Request) {
	var req definitionRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	def, err := definition.Parse([]byte(req.Body))
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"valid":  false,
			"errors": validationProblems(err),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"valid": true,
		"id":    def.Meta.ID,
		"name":  def.Meta.Name,
	})
}

func overwriteRequested(r *http.Request) bool {
	value := strings.TrimSpace(r.URL.Query().Get("overwrite"))
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

func validationProblems(err error) []string {
	var invalid *definition.ValidationError
	if errors.As(err, &invalid) {
		problems := make([]string, 0, len(invalid.Problems))
		problems = append(problems, invalid.Problems...)
		return problems
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "invalid definition"
	}
	return []string{message}
}

// isConstraintError reports whether err is a SQLite constraint violation,
// used to turn a racing definition delete into a clear 409.
func isConstraintError(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "constraint failed") || strings.Contains(text, "foreign key")
}

// decodeConfig parses a config_json object without losing numeric precision.
func decodeConfig(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}
