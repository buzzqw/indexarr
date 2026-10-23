package web

import (
	"net/http"
	"strings"
)

// activityPage unifies the run history and the in-memory log in one tabbed
// page. The two data sets keep their existing loaders.
type activityPage struct {
	base
	Tab  string
	Runs runsPage
	Logs logsPage
}

func (s *Server) activityPage(w http.ResponseWriter, r *http.Request) {
	tab := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tab")))
	if tab != "logs" {
		tab = "runs"
	}
	runs, err := s.runsData(r)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	logs := s.logsData(r)
	data := activityPage{
		base: s.newBase(r, "Activity", "activity"),
		Tab:  tab,
		Runs: runs,
		Logs: logs,
	}
	if tab == "logs" {
		data.AutoRefresh = logs.AutoRefresh
	}
	s.render(w, r, "activity", data, http.StatusOK)
}
