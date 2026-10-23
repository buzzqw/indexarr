package simmock

import "net/http"

func (s *Server) handleProwlarrIndexers(w http.ResponseWriter, r *http.Request) {
	if !s.requireHeaderKey(w, r, s.prowlarrKey) {
		return
	}
	s.mu.Lock()
	s.state.ProwlarrRequests++
	s.mu.Unlock()

	payload := []map[string]any{
		{
			"id":             1,
			"name":           "Sim Tracker",
			"implementation": "Torznab",
			"configContract": "TorznabSettings",
			"protocol":       "torrent",
			"enableRss":      true,
			"priority":       25,
			"sortName":       "sim-tracker",
			"fields": []map[string]any{
				{"name": "baseUrl", "value": "http://127.0.0.1:19080/torznab/api"},
				{"name": "apiKey", "value": "demo-key"},
				{"name": "categories", "value": []int{2000, 5000}},
			},
		},
		{
			"id":             2,
			"name":           "Sim Usenet",
			"implementation": "Newznab",
			"configContract": "NewznabSettings",
			"protocol":       "usenet",
			"enableRss":      true,
			"priority":       30,
			"sortName":       "sim-usenet",
			"fields": []map[string]any{
				{"name": "baseUrl", "value": "http://127.0.0.1:19080/newznab/api"},
				{"name": "apiKey", "value": "demo-key"},
				{"name": "categories", "value": []int{3000, 7000}},
			},
		},
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleProwlarrApplications(w http.ResponseWriter, r *http.Request) {
	if !s.requireHeaderKey(w, r, s.prowlarrKey) {
		return
	}
	s.mu.Lock()
	s.state.ProwlarrRequests++
	s.mu.Unlock()

	payload := []map[string]any{
		{
			"id":             1,
			"name":           "Sim Sonarr",
			"implementation": "Sonarr",
			"syncLevel":      "full",
			"fields": []map[string]any{
				{"name": "prowlarrUrl", "value": "http://127.0.0.1:19080/prowlarr"},
				{"name": "baseUrl", "value": "http://127.0.0.1:19080/sonarr"},
				{"name": "apiKey", "value": "demo-sonarr"},
			},
		},
		{
			"id":             2,
			"name":           "Sim Radarr",
			"implementation": "Radarr",
			"syncLevel":      "addRemove",
			"fields": []map[string]any{
				{"name": "prowlarrUrl", "value": "http://127.0.0.1:19080/prowlarr"},
				{"name": "baseUrl", "value": "http://127.0.0.1:19080/sonarr"},
				{"name": "apiKey", "value": "demo-sonarr"},
			},
		},
	}
	writeJSON(w, http.StatusOK, payload)
}
