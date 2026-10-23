package web

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/buzzqw/indexarr/internal/downloadclient"
)

func (s *Server) releaseSend(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/releases", "", "invalid release id")
		return
	}
	release, err := s.store.GetRelease(r.Context(), id)
	if err != nil {
		redirect(w, r, "/releases", "", "release not found")
		return
	}
	cfg, clientName, err := s.resolveDownloadClient(r.Context(), release.Protocol)
	if err != nil {
		redirect(w, r, "/releases/"+fmt.Sprint(id), "", s.translate(r, "release.noClient"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := downloadclient.Add(ctx, cfg, release); err != nil {
		s.log.Warn("send to download client failed", "component", "indexarr", "subsystem", "downloadclient", "release_id", id, "client", cfg.Kind, "error", err)
		redirect(w, r, "/releases/"+fmt.Sprint(id), "", s.translate(r, "release.sendFailed", err.Error()))
		return
	}
	s.log.Info("release sent to download client", "release_id", id, "client", cfg.Kind, "title", release.Title)
	redirect(w, r, "/releases/"+fmt.Sprint(id), s.translate(r, "release.sentTo", clientName), "")
}
