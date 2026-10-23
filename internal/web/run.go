package web

import (
	"fmt"
	"net/http"

	"github.com/buzzqw/indexarr/internal/store"
)

type runPage struct {
	base
	Run store.RunHistory
}

func (s *Server) runPage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "invalid run id")
		return
	}
	run, err := s.store.GetRunHistory(r.Context(), id)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "run not found")
		return
	}
	s.render(w, r, "run", runPage{
		base: s.newBase(r, fmt.Sprintf("Run %d", run.ID), "activity"),
		Run:  run,
	}, http.StatusOK)
}
