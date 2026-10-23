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
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		redirect(w, r, "/releases/"+fmt.Sprint(id), "", err.Error())
		return
	}
	release, err := s.store.GetRelease(r.Context(), id)
	if err != nil {
		redirect(w, r, "/releases", "", "release not found")
		return
	}
	cfg := downloadclient.Config{
		Kind:     settings.DownloadClient,
		URL:      settings.DownloadClientURL,
		Username: settings.DownloadClientUser,
		Password: settings.DownloadClientPassword,
		Category: settings.DownloadClientCategory,
		SavePath: settings.DownloadClientSavePath,
		Paused:   settings.DownloadClientPaused,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := downloadclient.Add(ctx, cfg, release); err != nil {
		s.log.Warn("send to download client failed", "component", "indexarr", "subsystem", "downloadclient", "release_id", id, "client", cfg.Kind, "error", err)
		redirect(w, r, "/releases/"+fmt.Sprint(id), "", "Download client: "+err.Error())
		return
	}
	s.log.Info("release sent to download client", "release_id", id, "client", cfg.Kind, "title", release.Title)
	redirect(w, r, "/releases/"+fmt.Sprint(id), "Sent to "+settings.DownloadClient, "")
}
