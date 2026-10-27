package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
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
	if strings.EqualFold(strings.TrimSpace(release.Protocol), "ed2k") && strings.TrimSpace(release.Ed2k) == "" && s.downloadResolver != nil {
		// A live search stores the eD2k release behind a sealed details
		// reference. Open it now, so the client receives the real link.
		if body, _, _, resolveErr := s.downloadResolver(r.Context(), release.ID); resolveErr == nil {
			if link := strings.TrimSpace(string(body)); strings.HasPrefix(strings.ToLower(link), "ed2k://") {
				release.Ed2k = link
			}
		}
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
