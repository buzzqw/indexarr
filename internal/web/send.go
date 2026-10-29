package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/downloadclient"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/notify"
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
	// The indexer's share limits travel with the release: a private tracker
	// asks for a ratio, and the client that receives the magnet must know it.
	// A missing provenance leaves the client's defaults untouched.
	if found, ok, lookupErr := s.store.IndexerForRelease(r.Context(), release.ID); lookupErr == nil && ok {
		cfg.SeedRatio = found.SeedRatio
		cfg.SeedTimeMinutes = found.SeedTimeMinutes
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if err := downloadclient.Add(ctx, cfg, release); err != nil {
		s.log.Warn("send to download client failed", "component", "indexarr", "subsystem", "downloadclient", "release_id", id, "client", cfg.Kind, "error", err)
		redirect(w, r, "/releases/"+fmt.Sprint(id), "", s.translate(r, "release.sendFailed", err.Error()))
		return
	}
	s.log.Info("release sent to download client", "release_id", id, "client", cfg.Kind, "title", release.Title)
	if strings.TrimSpace(release.Ed2k) != "" {
		// An eD2k send is a real delivery indexarr made: record it like the
		// Torznab finishGrab, so the Download tab can show it. Sends of
		// torrents and magnets keep their historical behaviour.
		s.recordGrab(r.Context(), release, indexer.GrabViaEd2k)
	}
	s.notifyGrabSuccess(r.Context(), release)
	redirect(w, r, "/releases/"+fmt.Sprint(id), s.translate(r, "release.sentTo", clientName), "")
}

// releaseDownload hands an eD2k release to the browser through indexarr, so the
// delivery is recorded like an app's grab. Torrents and magnets keep their
// direct links: the feed carries those links itself, and only the eD2k path
// needs this round trip.
func (s *Server) releaseDownload(w http.ResponseWriter, r *http.Request) {
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
	link := strings.TrimSpace(release.Ed2k)
	if !strings.HasPrefix(strings.ToLower(link), "ed2k://") {
		redirect(w, r, "/releases/"+fmt.Sprint(id), "", s.translate(r, "release.noEd2k"))
		return
	}
	s.recordGrab(r.Context(), release, indexer.GrabViaEd2k)
	s.notifyGrabSuccess(r.Context(), release)
	http.Redirect(w, r, link, http.StatusFound)
}

// notifyGrabSuccess reports a delivery the web interface made, using the same
// event and switches as a Torznab grab. The source is the indexer that supplied
// the release when the provenance is known.
func (s *Server) notifyGrabSuccess(ctx context.Context, release store.Release) {
	if s == nil || s.notifier == nil {
		return
	}
	ctx = context.WithoutCancel(ctx)
	settings, err := s.store.LoadSettings(ctx)
	if err != nil {
		return
	}
	source := ""
	if found, ok, lookupErr := s.store.IndexerForRelease(ctx, release.ID); lookupErr == nil && ok {
		source = found.Name
	}
	s.notifier.DispatchAsync(ctx, settings, notify.Event{
		Kind:       notify.KindGrabSuccess,
		Source:     source,
		Title:      release.Title,
		App:        "web",
		Size:       release.Size,
		Categories: splitCategories(release.Category),
		Path:       "/releases",
	})
}

func splitCategories(category string) []string {
	var out []string
	for _, item := range strings.Split(category, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// recordGrab writes a web delivery the way the Torznab server does: the
// release becomes downloaded and the source gets a grab row with the delivery
// kind. A missing provenance or a failed write is logged, never fatal: the
// link has already been handed to the user.
func (s *Server) recordGrab(ctx context.Context, release store.Release, via string) {
	ctx = context.WithoutCancel(ctx)
	if err := s.store.MarkReleaseDownloaded(ctx, release.ID); err != nil {
		s.log.Warn("mark release downloaded", "component", "indexarr", "subsystem", "web",
			"release_id", release.ID, "error", err)
	}
	if s.native == nil {
		return
	}
	found, ok, err := s.store.IndexerForRelease(ctx, release.ID)
	if err != nil {
		s.log.Warn("resolve release indexer", "component", "indexarr", "subsystem", "web",
			"release_id", release.ID)
		return
	}
	if !ok {
		return
	}
	if err := s.native.RecordGrab(indexer.WithGrabVia(ctx, via), found.ID, release.ID, release.Title); err != nil {
		s.log.Warn("record grab", "component", "indexarr", "subsystem", "web",
			"indexer_id", found.ID, "release_id", release.ID)
	}
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
