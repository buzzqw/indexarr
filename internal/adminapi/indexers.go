package adminapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/store"
)

type indexerRequest struct {
	DefinitionID              string         `json:"definition_id"`
	Name                      string         `json:"name"`
	Slug                      string         `json:"slug"`
	Protocol                  string         `json:"protocol"`
	Priority                  *int           `json:"priority"`
	Tags                      []string       `json:"tags"`
	Enabled                   *bool          `json:"enabled"`
	QueryLimit                *int           `json:"query_limit"`
	GrabLimit                 *int           `json:"grab_limit"`
	ProxyID                   *int64         `json:"proxy_id"`
	MinSeeders                *int           `json:"min_seeders"`
	SeedRatio                 *float64       `json:"seed_ratio"`
	SeedTimeMinutes           *int           `json:"seed_time_minutes"`
	SeasonPackSeedTimeMinutes *int           `json:"season_pack_seed_time_minutes"`
	IntervalSeconds           *int           `json:"interval_seconds"`
	RequestTimeoutSeconds     *int           `json:"request_timeout_seconds"`
	Settings                  map[string]any `json:"settings"`
}

type indexerResponse struct {
	ID                        int64     `json:"id"`
	DefinitionID              string    `json:"definition_id"`
	Protocol                  string    `json:"protocol"`
	Slug                      string    `json:"slug"`
	Name                      string    `json:"name"`
	Enabled                   bool      `json:"enabled"`
	Priority                  int       `json:"priority"`
	Tags                      []string  `json:"tags"`
	QueryLimit                int       `json:"query_limit"`
	GrabLimit                 int       `json:"grab_limit"`
	ProxyID                   *int64    `json:"proxy_id,omitempty"`
	MinSeeders                int       `json:"min_seeders"`
	SeedRatio                 float64   `json:"seed_ratio"`
	SeedTimeMinutes           int       `json:"seed_time_minutes"`
	SeasonPackSeedTimeMinutes int       `json:"season_pack_seed_time_minutes"`
	IntervalSeconds           int       `json:"interval_seconds"`
	HasSecrets                bool      `json:"has_secrets"`
	CreatedAt                 time.Time `json:"created_at"`
	UpdatedAt                 time.Time `json:"updated_at"`
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
	// Ed2k and Ed2kHash carry an eD2k file link; they are empty for torrent
	// and Usenet results.
	Ed2k     string `json:"ed2k"`
	Ed2kHash string `json:"ed2k_hash"`
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
	created, err := s.indexers.Create(r.Context(), req.input())
	if err != nil {
		s.writeIndexerError(w, "create", err)
		return
	}
	s.writeIndexer(w, r, http.StatusCreated, created)
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
	updated, err := s.indexers.Update(r.Context(), id, req.input())
	if err != nil {
		s.writeIndexerError(w, "update", err)
		return
	}
	s.writeIndexer(w, r, http.StatusOK, updated)
}

// input turns the JSON body into the form the indexer service validates, the
// same one the web interface submits. A proxy_id of zero or less removes the
// proxy; settings are sent as text, as the interface sends them.
func (req indexerRequest) input() indexersvc.Input {
	in := indexersvc.Input{
		DefinitionID:              req.DefinitionID,
		Name:                      req.Name,
		Slug:                      req.Slug,
		Protocol:                  req.Protocol,
		Priority:                  req.Priority,
		Tags:                      req.Tags,
		Enabled:                   req.Enabled,
		IntervalSeconds:           req.IntervalSeconds,
		RequestTimeoutSeconds:     req.RequestTimeoutSeconds,
		QueryLimit:                req.QueryLimit,
		GrabLimit:                 req.GrabLimit,
		MinSeeders:                req.MinSeeders,
		SeedRatio:                 req.SeedRatio,
		SeedTimeMinutes:           req.SeedTimeMinutes,
		SeasonPackSeedTimeMinutes: req.SeasonPackSeedTimeMinutes,
	}
	if req.ProxyID != nil {
		if *req.ProxyID > 0 {
			id := *req.ProxyID
			in.ProxyID = &id
		} else {
			in.ClearProxy = true
		}
	}
	if req.Settings != nil {
		in.Settings = make(map[string]string, len(req.Settings))
		for key, value := range req.Settings {
			in.Settings[key] = settingText(value)
		}
	}
	return in
}

// writeIndexerError maps an error of the indexer service to its status: the
// caller's mistakes are 4xx with the service's message, everything else is
// logged and answered as an internal error.
func (s *Server) writeIndexerError(w http.ResponseWriter, operation string, err error) {
	switch {
	case errors.Is(err, indexersvc.ErrIndexerNotFound):
		writeError(w, http.StatusNotFound, "indexer not found")
	case errors.Is(err, indexersvc.ErrDefinitionNotFound):
		writeError(w, http.StatusBadRequest, "definition not found")
	case errors.Is(err, indexersvc.ErrSlugTaken):
		writeError(w, http.StatusConflict, "indexer slug already exists")
	case errors.Is(err, indexersvc.ErrVaultRequired):
		writeError(w, http.StatusServiceUnavailable, "vault not configured")
	case errors.Is(err, indexersvc.ErrInvalidInput), errors.Is(err, indexersvc.ErrManagerEndpoint):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.logger.Error("adminapi: "+operation+" indexer", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func (s *Server) writeIndexer(w http.ResponseWriter, r *http.Request, status int, in store.Indexer) {
	resp, err := s.indexerResponse(r, in)
	if err != nil {
		s.logger.Error("adminapi: indexer response", "id", in.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, status, resp)
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
			Ed2k:       item.Ed2k,
			Ed2kHash:   item.Ed2kHash,
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
		ID:                        in.ID,
		DefinitionID:              in.DefinitionID,
		Protocol:                  in.Protocol,
		Slug:                      in.Slug,
		Name:                      in.Name,
		Enabled:                   in.Enabled,
		Priority:                  in.Priority,
		Tags:                      splitTags(in.Tags),
		QueryLimit:                in.QueryLimit,
		GrabLimit:                 in.GrabLimit,
		ProxyID:                   in.ProxyID,
		MinSeeders:                in.MinSeeders,
		SeedRatio:                 in.SeedRatio,
		SeedTimeMinutes:           in.SeedTimeMinutes,
		SeasonPackSeedTimeMinutes: in.SeasonPackSeedTimeMinutes,
		IntervalSeconds:           in.IntervalSeconds,
		HasSecrets:                len(secrets) > 0,
		CreatedAt:                 in.CreatedAt,
		UpdatedAt:                 in.UpdatedAt,
	}, nil
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
