package adminapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
)

var secretFragments = []string{
	"api_key", "apikey", "token", "password", "passkey", "secret", "cookie",
}

type indexerRequest struct {
	DefinitionID    string         `json:"definition_id"`
	Name            string         `json:"name"`
	Slug            string         `json:"slug"`
	Protocol        string         `json:"protocol"`
	Priority        *int           `json:"priority"`
	Tags            []string       `json:"tags"`
	Enabled         *bool          `json:"enabled"`
	QueryLimit      *int           `json:"query_limit"`
	GrabLimit       *int           `json:"grab_limit"`
	IntervalSeconds *int           `json:"interval_seconds"`
	Settings        map[string]any `json:"settings"`
}

type indexerResponse struct {
	ID              int64     `json:"id"`
	DefinitionID    string    `json:"definition_id"`
	Protocol        string    `json:"protocol"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	Enabled         bool      `json:"enabled"`
	Priority        int       `json:"priority"`
	Tags            []string  `json:"tags"`
	QueryLimit      int       `json:"query_limit"`
	GrabLimit       int       `json:"grab_limit"`
	IntervalSeconds int       `json:"interval_seconds"`
	HasSecrets      bool      `json:"has_secrets"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type searchRequest struct {
	Mode       string `json:"mode"`
	Query      string `json:"query"`
	Season     int    `json:"season"`
	Episode    int    `json:"episode"`
	IMDbID     string `json:"imdbid"`
	TMDbID     string `json:"tmdbid"`
	TVDbID     string `json:"tvdbid"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
	Categories []int  `json:"categories"`
}

type searchItem struct {
	Title      string    `json:"title"`
	GUID       string    `json:"guid"`
	Size       int64     `json:"size"`
	Seeders    int64     `json:"seeders"`
	Leechers   int64     `json:"leechers"`
	Categories []int     `json:"categories"`
	Published  time.Time `json:"published"`
	InfoHash   string    `json:"infohash"`
	Magnet     string    `json:"magnet"`
}

type testPhase struct {
	Name       string `json:"name"`
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
	DurationMS int64  `json:"duration_ms"`
}

type testResponse struct {
	OK     bool        `json:"ok"`
	Phases []testPhase `json:"phases"`
}

type runResponse struct {
	Seen     int `json:"seen"`
	New      int `json:"new"`
	Updated  int `json:"updated"`
	Blocked  int `json:"blocked"`
	Skipped  int `json:"skipped"`
	Torrents int `json:"torrents"`
}

func (s *Server) handleIndexerList(w http.ResponseWriter, r *http.Request) {
	indexers, err := s.store.ListIndexers(r.Context())
	if err != nil {
		s.logger.Error("adminapi: list indexers", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]indexerResponse, 0, len(indexers))
	for _, in := range indexers {
		resp, err := s.indexerResponse(r, in)
		if err != nil {
			s.logger.Error("adminapi: indexer response", "id", in.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, map[string]any{"indexers": out})
}

func (s *Server) handleIndexerGet(w http.ResponseWriter, r *http.Request) {
	id, ok := indexerID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid indexer id")
		return
	}
	in, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "indexer not found")
			return
		}
		s.logger.Error("adminapi: get indexer", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp, err := s.indexerResponse(r, in)
	if err != nil {
		s.logger.Error("adminapi: indexer response", "id", in.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleIndexerCreate(w http.ResponseWriter, r *http.Request) {
	var req indexerRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	ctx := r.Context()

	definitionID := strings.TrimSpace(req.DefinitionID)
	name := strings.TrimSpace(req.Name)
	if definitionID == "" {
		writeError(w, http.StatusBadRequest, "definition_id is required")
		return
	}
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	def, err := s.store.GetDefinition(ctx, definitionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "definition not found")
			return
		}
		s.logger.Error("adminapi: get definition for indexer", "id", definitionID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	protocol := strings.ToLower(strings.TrimSpace(req.Protocol))
	parsed, err := definition.Parse([]byte(def.Body))
	if err != nil {
		s.logger.Error("adminapi: parse stored definition", "id", definitionID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if protocol == "" {
		protocol = strings.ToLower(strings.TrimSpace(parsed.Meta.Protocol))
	}
	if !validProtocol(protocol) {
		writeError(w, http.StatusBadRequest, "protocol must be torrent or usenet")
		return
	}

	provided := make(map[string]string, len(req.Settings))
	for key, value := range req.Settings {
		provided[key] = strings.TrimSpace(settingText(value))
	}
	for _, setting := range parsed.Settings {
		if setting.Required && provided[setting.Name] == "" {
			writeError(w, http.StatusBadRequest, "missing required setting: "+setting.Name)
			return
		}
	}

	slug := normalizeSlug(req.Slug)
	if slug == "" {
		slug = normalizeSlug(name)
	}
	if slug == "" {
		writeError(w, http.StatusBadRequest, "slug is required")
		return
	}
	if err := s.ensureSlugAvailable(ctx, slug, 0); err != nil {
		s.writeSlugError(w, err)
		return
	}

	public, secrets, _, err := splitSettings(req.Settings)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(secrets) > 0 && s.vault == nil {
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
		return
	}
	sealed, err := s.sealSecrets(secrets)
	if err != nil {
		s.logger.Error("adminapi: seal indexer secrets", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	configJSON, err := json.Marshal(public)
	if err != nil {
		s.logger.Error("adminapi: encode indexer config", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	priority := 25
	if req.Priority != nil {
		priority = *req.Priority
	}
	queryLimit, grabLimit := 0, 0
	if req.QueryLimit != nil {
		queryLimit = *req.QueryLimit
	}
	if req.GrabLimit != nil {
		grabLimit = *req.GrabLimit
	}
	interval, err := intervalFromRequest(req.IntervalSeconds, parsed.Meta.Interval.Duration())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	id, err := s.store.CreateIndexer(ctx, store.Indexer{
		DefinitionID:    definitionID,
		Protocol:        protocol,
		Slug:            slug,
		Name:            name,
		Enabled:         enabled,
		Priority:        priority,
		ConfigJSON:      string(configJSON),
		Tags:            joinTags(req.Tags),
		QueryLimit:      queryLimit,
		GrabLimit:       grabLimit,
		IntervalSeconds: interval,
	})
	if err != nil {
		if isUniqueError(err) {
			writeError(w, http.StatusConflict, "indexer slug already exists")
			return
		}
		s.logger.Error("adminapi: create indexer", "name", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	for key, value := range sealed {
		if err := s.store.PutSecret(ctx, store.Secret{
			IndexerID:  id,
			Key:        key,
			Nonce:      value.nonce,
			Ciphertext: value.ciphertext,
		}); err != nil {
			s.logger.Error("adminapi: store indexer secret", "indexer_id", id, "error", err)
			_ = s.store.DeleteIndexer(ctx, id)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}

	in, err := s.store.GetIndexer(ctx, id)
	if err != nil {
		s.logger.Error("adminapi: get created indexer", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp, err := s.indexerResponse(r, in)
	if err != nil {
		s.logger.Error("adminapi: indexer response", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleIndexerUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := indexerID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid indexer id")
		return
	}
	var req indexerRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	ctx := r.Context()

	in, err := s.store.GetIndexer(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "indexer not found")
			return
		}
		s.logger.Error("adminapi: get indexer for update", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if definitionID := strings.TrimSpace(req.DefinitionID); definitionID != "" && definitionID != in.DefinitionID {
		if _, err := s.store.GetDefinition(ctx, definitionID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusBadRequest, "definition not found")
				return
			}
			s.logger.Error("adminapi: get definition for indexer update", "id", definitionID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		in.DefinitionID = definitionID
	}
	if name := strings.TrimSpace(req.Name); name != "" {
		in.Name = name
	}
	if req.Slug != "" {
		slug := normalizeSlug(req.Slug)
		if slug == "" {
			writeError(w, http.StatusBadRequest, "slug is required")
			return
		}
		if err := s.ensureSlugAvailable(ctx, slug, in.ID); err != nil {
			s.writeSlugError(w, err)
			return
		}
		in.Slug = slug
	}
	if protocol := strings.ToLower(strings.TrimSpace(req.Protocol)); protocol != "" {
		if !validProtocol(protocol) {
			writeError(w, http.StatusBadRequest, "protocol must be torrent or usenet")
			return
		}
		in.Protocol = protocol
	}
	if req.Priority != nil {
		in.Priority = *req.Priority
	}
	if req.Enabled != nil {
		in.Enabled = *req.Enabled
	}
	if req.QueryLimit != nil {
		in.QueryLimit = *req.QueryLimit
	}
	if req.GrabLimit != nil {
		in.GrabLimit = *req.GrabLimit
	}
	if req.IntervalSeconds != nil {
		interval, err := intervalFromRequest(req.IntervalSeconds, 0)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		in.IntervalSeconds = interval
	}
	if req.Tags != nil {
		in.Tags = joinTags(req.Tags)
	}

	var sealed map[string]sealedSecret
	var deleteKeys []string
	if req.Settings != nil {
		config, err := decodeConfig(in.ConfigJSON)
		if err != nil {
			s.logger.Error("adminapi: decode indexer config", "id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		public, secrets, deletes, err := splitSettings(req.Settings)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		existing, err := s.store.ListSecrets(ctx, id)
		if err != nil {
			s.logger.Error("adminapi: list indexer secrets", "id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		existingSecrets := make(map[string]bool, len(existing))
		for _, secret := range existing {
			existingSecrets[secret.Key] = true
		}
		for key := range secrets {
			delete(config, key)
		}
		for key, value := range public {
			if existingSecrets[key] {
				deletes = append(deletes, key)
			}
			if value == nil || settingText(value) == "" {
				delete(config, key)
				continue
			}
			config[key] = value
		}
		configJSON, err := json.Marshal(config)
		if err != nil {
			s.logger.Error("adminapi: encode indexer config", "id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		in.ConfigJSON = string(configJSON)

		if len(secrets) > 0 && s.vault == nil {
			writeError(w, http.StatusServiceUnavailable, "vault not configured")
			return
		}
		sealed, err = s.sealSecrets(secrets)
		if err != nil {
			s.logger.Error("adminapi: seal indexer secrets", "id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		deleteKeys = deletes
	}

	if err := s.store.UpdateIndexer(ctx, in); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "indexer not found")
			return
		}
		if isUniqueError(err) {
			writeError(w, http.StatusConflict, "indexer slug already exists")
			return
		}
		s.logger.Error("adminapi: update indexer", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	for key, value := range sealed {
		if err := s.store.PutSecret(ctx, store.Secret{
			IndexerID:  id,
			Key:        key,
			Nonce:      value.nonce,
			Ciphertext: value.ciphertext,
		}); err != nil {
			s.logger.Error("adminapi: update indexer secret", "indexer_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	for _, key := range deleteKeys {
		if err := s.store.DeleteSecret(ctx, id, key); err != nil {
			s.logger.Error("adminapi: delete indexer secret", "indexer_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	if s.manager != nil {
		s.manager.Invalidate(id)
	}

	updated, err := s.store.GetIndexer(ctx, id)
	if err != nil {
		s.logger.Error("adminapi: get updated indexer", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp, err := s.indexerResponse(r, updated)
	if err != nil {
		s.logger.Error("adminapi: indexer response", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleIndexerDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := indexerID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid indexer id")
		return
	}
	ctx := r.Context()
	if _, err := s.store.GetIndexer(ctx, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "indexer not found")
			return
		}
		s.logger.Error("adminapi: get indexer for delete", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if s.manager != nil {
		s.manager.Invalidate(id)
	}
	if err := s.store.DeleteIndexer(ctx, id); err != nil {
		s.logger.Error("adminapi: delete indexer", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeNoContent(w)
}

func (s *Server) handleIndexerTest(w http.ResponseWriter, r *http.Request) {
	id, ok := indexerID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid indexer id")
		return
	}
	if s.manager == nil {
		writeError(w, http.StatusServiceUnavailable, "manager not configured")
		return
	}
	if !s.indexerExists(w, r, id) {
		return
	}
	result, err := s.manager.Test(r.Context(), id)
	if err != nil {
		s.logger.Error("adminapi: test indexer", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "test failed")
		return
	}
	phases := make([]testPhase, 0, len(result.Phases))
	for _, phase := range result.Phases {
		phases = append(phases, testPhase{
			Name:       phase.Name,
			OK:         phase.OK,
			Message:    phase.Message,
			DurationMS: phase.Duration.Milliseconds(),
		})
	}
	writeJSON(w, http.StatusOK, testResponse{OK: result.OK, Phases: phases})
}

func (s *Server) handleIndexerSearch(w http.ResponseWriter, r *http.Request) {
	id, ok := indexerID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid indexer id")
		return
	}
	if s.manager == nil {
		writeError(w, http.StatusServiceUnavailable, "manager not configured")
		return
	}
	if !s.indexerExists(w, r, id) {
		return
	}
	var req searchRequest
	if !decodeRequest(w, r, &req) {
		return
	}
	mode := engine.Mode(strings.ToLower(strings.TrimSpace(req.Mode)))
	if mode == "" {
		mode = engine.ModeSearch
	}
	if !mode.Valid() {
		writeError(w, http.StatusBadRequest, "invalid search mode")
		return
	}

	items, err := s.manager.Search(r.Context(), id, engine.SearchRequest{
		Mode:       mode,
		Query:      req.Query,
		Season:     req.Season,
		Episode:    req.Episode,
		IMDbID:     req.IMDbID,
		TMDbID:     req.TMDbID,
		TVDbID:     req.TVDbID,
		Categories: req.Categories,
		Limit:      req.Limit,
		Offset:     req.Offset,
	})
	if err != nil {
		s.logger.Error("adminapi: search indexer", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}

	out := make([]searchItem, 0, len(items))
	for _, item := range items {
		categories := item.Categories
		if categories == nil {
			categories = []int{}
		}
		out = append(out, searchItem{
			Title:      item.Title,
			GUID:       item.GUID,
			Size:       item.Size,
			Seeders:    item.Seeders,
			Leechers:   item.Leechers,
			Categories: categories,
			Published:  item.Published,
			InfoHash:   item.InfoHash,
			Magnet:     item.Magnet,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleIndexerRun(w http.ResponseWriter, r *http.Request) {
	id, ok := indexerID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid indexer id")
		return
	}
	if s.manager == nil {
		writeError(w, http.StatusServiceUnavailable, "manager not configured")
		return
	}
	if !s.indexerExists(w, r, id) {
		return
	}
	stats, err := s.manager.Run(r.Context(), id)
	if err != nil {
		s.logger.Error("adminapi: run indexer", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "run failed")
		return
	}
	writeJSON(w, http.StatusOK, runResponse{
		Seen:     stats.Seen,
		New:      stats.New,
		Updated:  stats.Updated,
		Blocked:  stats.Blocked,
		Skipped:  stats.Skipped,
		Torrents: stats.Torrents,
	})
}

func (s *Server) indexerExists(w http.ResponseWriter, r *http.Request, id int64) bool {
	if _, err := s.store.GetIndexer(r.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "indexer not found")
			return false
		}
		s.logger.Error("adminapi: get indexer", "id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return false
	}
	return true
}

func (s *Server) indexerResponse(r *http.Request, in store.Indexer) (indexerResponse, error) {
	secrets, err := s.store.ListSecrets(r.Context(), in.ID)
	if err != nil {
		return indexerResponse{}, err
	}
	return indexerResponse{
		ID:              in.ID,
		DefinitionID:    in.DefinitionID,
		Protocol:        in.Protocol,
		Slug:            in.Slug,
		Name:            in.Name,
		Enabled:         in.Enabled,
		Priority:        in.Priority,
		Tags:            splitTags(in.Tags),
		QueryLimit:      in.QueryLimit,
		GrabLimit:       in.GrabLimit,
		IntervalSeconds: in.IntervalSeconds,
		HasSecrets:      len(secrets) > 0,
		CreatedAt:       in.CreatedAt,
		UpdatedAt:       in.UpdatedAt,
	}, nil
}

type sealedSecret struct {
	nonce      []byte
	ciphertext []byte
}

func (s *Server) sealSecrets(values map[string]string) (map[string]sealedSecret, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if s.vault == nil {
		return nil, errors.New("vault not configured")
	}
	out := make(map[string]sealedSecret, len(values))
	for key, value := range values {
		nonce, ciphertext, err := s.vault.Seal(value)
		if err != nil {
			return nil, err
		}
		out[key] = sealedSecret{nonce: nonce, ciphertext: ciphertext}
	}
	return out, nil
}

func (s *Server) ensureSlugAvailable(ctx context.Context, slug string, self int64) error {
	in, err := s.store.GetIndexerBySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if in.ID != self {
		return errSlugTaken
	}
	return nil
}

var errSlugTaken = errors.New("indexer slug already exists")

func (s *Server) writeSlugError(w http.ResponseWriter, err error) {
	if errors.Is(err, errSlugTaken) {
		writeError(w, http.StatusConflict, "indexer slug already exists")
		return
	}
	s.logger.Error("adminapi: check indexer slug", "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func splitSettings(settings map[string]any) (public map[string]any, secrets map[string]string, deletes []string, err error) {
	public = make(map[string]any)
	secrets = make(map[string]string)
	for key, raw := range settings {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, nil, nil, errors.New("settings: key must not be empty")
		}
		if isSecretSetting(key, settingText(raw)) {
			value := settingText(raw)
			if value == "" {
				deletes = append(deletes, key)
				continue
			}
			secrets[key] = value
			continue
		}
		public[key] = raw
	}
	return public, secrets, deletes, nil
}

func isSecretKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	for _, fragment := range secretFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

func isURLSettingKey(key string) bool {
	lower := strings.ToLower(strings.TrimSpace(key))
	return lower == "url" || lower == "feed" ||
		strings.HasSuffix(lower, "_url") || strings.HasSuffix(lower, "_uri")
}

func isSecretSetting(key, value string) bool {
	return isSecretKey(key) || (isURLSettingKey(key) && feed.IsSensitiveURL(value))
}

// intervalFromRequest mirrors the indexersvc rule for the API: nil uses the
// definition suggestion, zero keeps the global default and a positive value
// must be at least one minute.
func intervalFromRequest(requested *int, suggested time.Duration) (int, error) {
	if requested == nil {
		if suggested >= time.Minute {
			return int(suggested / time.Second), nil
		}
		return 0, nil
	}
	seconds := *requested
	if seconds < 0 || (seconds > 0 && seconds < 60) {
		return 0, errors.New("interval_seconds must be 0 or at least 60")
	}
	return seconds, nil
}

func settingText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	}
}

func normalizeSlug(raw string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func splitTags(raw string) []string {
	out := []string{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out
	}
	if strings.HasPrefix(raw, "[") {
		var parsed []string
		if json.Unmarshal([]byte(raw), &parsed) == nil {
			for _, tag := range parsed {
				if tag = strings.TrimSpace(tag); tag != "" {
					out = append(out, tag)
				}
			}
			return out
		}
	}
	for _, tag := range strings.Split(raw, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return out
}

func joinTags(tags []string) string {
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			out = append(out, tag)
		}
	}
	return strings.Join(out, ",")
}

func validProtocol(protocol string) bool {
	return protocol == "torrent" || protocol == "usenet"
}

func isUniqueError(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unique constraint") || strings.Contains(text, "constraint failed: indexers.slug")
}
