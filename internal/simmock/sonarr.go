package simmock

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
)

func (s *Server) sonarrStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireHeaderKey(w, r, s.sonarrKey) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"appName":      "Sonarr",
		"instanceName": "Sonarr Sim",
		"version":      "4.0.0-sim",
		"buildTime":    "2026-10-01T00:00:00Z",
		"isDocker":     true,
		"osName":       "linux",
	})
}

func (s *Server) sonarrSchema(w http.ResponseWriter, r *http.Request) {
	if !s.requireHeaderKey(w, r, s.sonarrKey) {
		return
	}
	categories := []int{2000, 5000}
	payload := []map[string]any{
		{
			"implementation": "Torznab",
			"configContract": "TorznabSettings",
			"protocol":       "torrent",
			"name":           "Torznab",
			"fields": []map[string]any{
				{"name": "baseUrl", "value": ""},
				{"name": "apiKey", "value": ""},
				{"name": "categories", "value": categories},
				{"name": "additionalParameters", "value": ""},
			},
		},
		{
			"implementation": "Newznab",
			"configContract": "NewznabSettings",
			"protocol":       "usenet",
			"name":           "Newznab",
			"fields": []map[string]any{
				{"name": "baseUrl", "value": ""},
				{"name": "apiKey", "value": ""},
				{"name": "categories", "value": categories},
				{"name": "additionalParameters", "value": ""},
			},
		},
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) sonarrList(w http.ResponseWriter, r *http.Request) {
	if !s.requireHeaderKey(w, r, s.sonarrKey) {
		return
	}
	s.mu.Lock()
	ids := make([]int, 0, len(s.sonarr))
	for id := range s.sonarr {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	list := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		list = append(list, cloneMap(s.sonarr[id]))
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) sonarrCreate(w http.ResponseWriter, r *http.Request) {
	if !s.requireHeaderKey(w, r, s.sonarrKey) {
		return
	}
	payload, ok := decodeJSONObject(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	s.sonarrNextID++
	if s.sonarrNextID < 1 {
		s.sonarrNextID = 1
	}
	id := s.sonarrNextID
	payload["id"] = id
	s.sonarr[id] = payload
	created := cloneMap(payload)
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) sonarrGet(w http.ResponseWriter, r *http.Request) {
	if !s.requireHeaderKey(w, r, s.sonarrKey) {
		return
	}
	id, ok := sonarrID(r)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid indexer id")
		return
	}
	s.mu.Lock()
	stored, found := s.sonarr[id]
	payload := cloneMap(stored)
	s.mu.Unlock()
	if !found {
		writeJSONError(w, http.StatusNotFound, "indexer not found")
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) sonarrUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.requireHeaderKey(w, r, s.sonarrKey) {
		return
	}
	id, ok := sonarrID(r)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid indexer id")
		return
	}
	payload, ok := decodeJSONObject(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	if _, found := s.sonarr[id]; !found {
		s.mu.Unlock()
		writeJSONError(w, http.StatusNotFound, "indexer not found")
		return
	}
	payload["id"] = id
	s.sonarr[id] = payload
	updated := cloneMap(payload)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) sonarrDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireHeaderKey(w, r, s.sonarrKey) {
		return
	}
	id, ok := sonarrID(r)
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "invalid indexer id")
		return
	}
	s.mu.Lock()
	_, found := s.sonarr[id]
	if found {
		delete(s.sonarr, id)
	}
	s.mu.Unlock()
	if !found {
		writeJSONError(w, http.StatusNotFound, "indexer not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func sonarrID(r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func decodeJSONObject(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	var payload map[string]any
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return nil, false
	}
	if payload == nil {
		writeJSONError(w, http.StatusBadRequest, "empty JSON object")
		return nil, false
	}
	return payload, true
}
