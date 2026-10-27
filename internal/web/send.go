package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/downloadclient"
	"github.com/buzzqw/indexarr/internal/store"
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
	release = s.openEd2kTopic(r.Context(), release)
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

// openEd2kTopic completes an eD2k release stored behind a sealed details
// reference (an archive row written before the topic was opened). The topic
// may carry an ed2k:// link, which goes to aMule, or a magnet, which turns the
// release into a torrent for a torrent client.
func (s *Server) openEd2kTopic(ctx context.Context, release store.Release) store.Release {
	if !strings.EqualFold(strings.TrimSpace(release.Protocol), "ed2k") ||
		strings.TrimSpace(release.Ed2k) != "" || s.downloadResolver == nil {
		return release
	}
	body, _, _, err := s.downloadResolver(ctx, release.ID)
	if err != nil {
		return release
	}
	link := strings.TrimSpace(string(body))
	switch lower := strings.ToLower(link); {
	case strings.HasPrefix(lower, "ed2k://"):
		release.Ed2k = link
	case strings.HasPrefix(lower, "magnet:"):
		release.Magnet = link
		release.Protocol = "torrent"
	}
	return release
}
