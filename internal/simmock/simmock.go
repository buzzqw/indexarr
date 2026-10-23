// Package simmock is a deterministic, self-contained mock of the services
// indexarr talks to: Torznab/Newznab indexers, an HTML tracker, Prowlarr,
// Sonarr and the supported download clients.
//
// It never performs external network calls and all responses are derived from
// in-memory fixtures, so the end-to-end simulation in sim/ is reproducible.
package simmock

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config configures the mock server. Empty keys fall back to the demo values.
type Config struct {
	APIKey      string
	SonarrKey   string
	ProwlarrKey string
	Logger      *log.Logger
}

// AddRecord is one download handed to a fake download client.
type AddRecord struct {
	Kind     string `json:"kind"`
	Mode     string `json:"mode,omitempty"`
	Name     string `json:"name,omitempty"`
	Title    string `json:"title,omitempty"`
	Category string `json:"category,omitempty"`
	URL      string `json:"url,omitempty"`
	Magnet   string `json:"magnet,omitempty"`
	Bytes    int    `json:"bytes,omitempty"`
	Paused   bool   `json:"paused,omitempty"`
}

// State is the observable state exposed by /debug/state.
type State struct {
	TorznabRequests  int              `json:"torznab_requests"`
	TorznabSearches  int              `json:"torznab_searches"`
	TorznabDownloads int              `json:"torznab_downloads"`
	TrackerLogins    int              `json:"tracker_logins"`
	TrackerDownloads []string         `json:"tracker_downloads"`
	ProwlarrRequests int              `json:"prowlarr_requests"`
	SonarrIndexers   []map[string]any `json:"sonarr_indexers"`
	Sabnzbd          []AddRecord      `json:"sabnzbd"`
	NZBGet           []AddRecord      `json:"nzbget"`
	Deluge           []AddRecord      `json:"deluge"`
	RTorrent         []AddRecord      `json:"rtorrent"`
}

// Server implements the fake services.
type Server struct {
	apiKey      string
	sonarrKey   string
	prowlarrKey string
	logger      *log.Logger

	mu           sync.Mutex
	state        State
	releases     []release
	byID         map[string]int
	sonarr       map[int]map[string]any
	sonarrNextID int
	sabCount     int
	nzbgetCount  int

	handler http.Handler
}

// New builds the mock server with deterministic fixtures.
func New(cfg Config) *Server {
	if strings.TrimSpace(cfg.APIKey) == "" {
		cfg.APIKey = "demo-key"
	}
	if strings.TrimSpace(cfg.SonarrKey) == "" {
		cfg.SonarrKey = "demo-sonarr"
	}
	if strings.TrimSpace(cfg.ProwlarrKey) == "" {
		cfg.ProwlarrKey = "demo-prowlarr"
	}
	if cfg.Logger == nil {
		cfg.Logger = log.New(os.Stderr, "simmock ", log.LstdFlags)
	}

	s := &Server{
		apiKey:      strings.TrimSpace(cfg.APIKey),
		sonarrKey:   strings.TrimSpace(cfg.SonarrKey),
		prowlarrKey: strings.TrimSpace(cfg.ProwlarrKey),
		logger:      cfg.Logger,
		releases:    buildFixtureReleases(),
		byID:        map[string]int{},
		sonarr:      map[int]map[string]any{},
	}
	for i, rel := range s.releases {
		s.byID[rel.ID] = i
		s.byID[rel.GUID] = i
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /debug/state", s.handleDebugState)

	mux.HandleFunc("GET /torznab/api", s.handleTorznab)
	mux.HandleFunc("GET /newznab/api", s.handleTorznab)
	mux.HandleFunc("GET /torznab/download/{file}", s.handleTorznabDownload)
	mux.HandleFunc("GET /newznab/download/{file}", s.handleTorznabDownload)

	mux.HandleFunc("GET /tracker/login", s.handleTrackerLoginPage)
	mux.HandleFunc("POST /tracker/login", s.handleTrackerLogin)
	mux.HandleFunc("GET /tracker/search", s.handleTrackerSearch)
	mux.HandleFunc("GET /tracker/download/{file}", s.handleTrackerDownload)

	mux.HandleFunc("GET /prowlarr/api/v1/indexer", s.handleProwlarrIndexers)
	mux.HandleFunc("GET /prowlarr/api/v1/applications", s.handleProwlarrApplications)

	mux.HandleFunc("GET /sonarr/api/v3/system/status", s.sonarrStatus)
	mux.HandleFunc("GET /sonarr/api/v3/indexer", s.sonarrList)
	mux.HandleFunc("POST /sonarr/api/v3/indexer", s.sonarrCreate)
	mux.HandleFunc("GET /sonarr/api/v3/indexer/schema", s.sonarrSchema)
	mux.HandleFunc("GET /sonarr/api/v3/indexer/{id}", s.sonarrGet)
	mux.HandleFunc("PUT /sonarr/api/v3/indexer/{id}", s.sonarrUpdate)
	mux.HandleFunc("DELETE /sonarr/api/v3/indexer/{id}", s.sonarrDelete)

	mux.HandleFunc("GET /sabnzbd/api", s.handleSABnzbd)
	mux.HandleFunc("POST /sabnzbd/api", s.handleSABnzbd)
	mux.HandleFunc("POST /nzbget/jsonrpc", s.handleNZBGet)
	mux.HandleFunc("POST /deluge/json", s.handleDeluge)
	mux.HandleFunc("POST /rtorrent/RPC2", s.handleRTorrent)

	s.handler = s.logRequests(mux)
	return s
}

// Handler returns the HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDebugState(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.state
	state.TrackerDownloads = append([]string(nil), s.state.TrackerDownloads...)
	state.Sabnzbd = append([]AddRecord(nil), s.state.Sabnzbd...)
	state.NZBGet = append([]AddRecord(nil), s.state.NZBGet...)
	state.Deluge = append([]AddRecord(nil), s.state.Deluge...)
	state.RTorrent = append([]AddRecord(nil), s.state.RTorrent...)
	state.SonarrIndexers = make([]map[string]any, 0, len(s.sonarr))
	ids := make([]int, 0, len(s.sonarr))
	for id := range s.sonarr {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		state.SonarrIndexers = append(state.SonarrIndexers, cloneMap(s.sonarr[id]))
	}
	writeJSON(w, http.StatusOK, state)
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

// logRequests writes one compact line per request to the configured logger.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		s.logger.Printf("%s %s %d %s", r.Method, r.URL.RequestURI(), recorder.status,
			time.Since(started).Round(time.Millisecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	return r.ResponseWriter.Write(body)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message, "message": message})
}

func readAll(r io.Reader) []byte {
	body, _ := io.ReadAll(io.LimitReader(r, 4<<20))
	return body
}

// requireTorznabKey validates the apikey query parameter or X-Api-Key header.
func (s *Server) requireTorznabKey(w http.ResponseWriter, r *http.Request) bool {
	key := strings.TrimSpace(r.URL.Query().Get("apikey"))
	if key == "" {
		key = strings.TrimSpace(r.Header.Get("X-Api-Key"))
	}
	if key != s.apiKey {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+
			`<error code="100" description="invalid API key"/>`+"\n")
		return false
	}
	return true
}

func (s *Server) requireHeaderKey(w http.ResponseWriter, r *http.Request, expected string) bool {
	if strings.TrimSpace(r.Header.Get("X-Api-Key")) != expected {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}
