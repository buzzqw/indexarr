package web

import (
	"fmt"
	"net/http"

	"github.com/buzzqw/indexarr/internal/store"
)

func (s *Server) sourceCancel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/sources", "", "invalid source id")
		return
	}
	if !s.engine.Cancel(id) {
		redirect(w, r, "/sources", "", "No run in progress for this source")
		return
	}
	s.log.Info("run canceled by user", "source_id", id)
	redirect(w, r, "/sources", "Run canceled", "")
}

type runPage struct {
	base
	Run store.Run
}

func (s *Server) runPage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "invalid run id")
		return
	}
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "run not found")
		return
	}
	s.render(w, r, "run", runPage{
		base: s.newBase(r, fmt.Sprintf("Run %d", run.ID), "activity"),
		Run:  run,
	}, http.StatusOK)
}
