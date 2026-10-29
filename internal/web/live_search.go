package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/downloadclient"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/store"
)

// releasesLive runs the manual fan-out behind the Releases page. It uses the
// same indexer.Manager path as an app request, then reads only the rows whose
// last_seen_at moved after the search started.
func (s *Server) releasesLive(w http.ResponseWriter, r *http.Request) {
	form := parseLiveSearchForm(r)
	indexers, _ := s.store.ListIndexers(r.Context())
	saved, _ := s.store.ListSavedSearches(r.Context())
	data := releasesPage{
		base:          s.newBaseT(r, "releases.title", "releases"),
		Indexers:      indexers,
		SavedSearches: saved,
		LiveForm:      form,
		LiveIndexers:  liveIndexerOptions(indexers, form.IndexerIDs, false),
	}
	if !form.hasTerms() {
		data.LiveError = s.translate(r, "releases.liveValidation")
		s.render(w, r, "releases", data, http.StatusOK)
		return
	}
	if len(form.IndexerIDs) == 0 {
		data.LiveError = s.translate(r, "releases.liveNoIndexers")
		s.render(w, r, "releases", data, http.StatusOK)
		return
	}
	if s.native == nil {
		data.LiveError = s.translate(r, "releases.liveUnavailable")
		s.render(w, r, "releases", data, http.StatusOK)
		return
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		data.LiveError = err.Error()
		s.render(w, r, "releases", data, http.StatusOK)
		return
	}
	timeout := time.Duration(settings.TorznabLiveTimeout) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}

	started := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	result, searchErr := s.native.SearchLive(ctx, indexer.LiveQuery{
		Mode:       form.Mode,
		Query:      form.Query,
		Season:     form.Season,
		Episode:    form.Episode,
		Year:       form.Year,
		IMDbID:     form.IMDbID,
		TMDbID:     form.TMDbID,
		TVDbID:     form.TVDbID,
		Artist:     form.Artist,
		Album:      form.Album,
		Author:     form.Author,
		Title:      form.Title,
		Categories: form.categoryIDs(),
		Limit:      releasesPerPage,
		IndexerIDs: form.IndexerIDs,
	})
	cancel()
	data.LivePerformed = true
	data.LiveOutcomes = result.Indexers
	data.LiveStored = result.Stored
	if searchErr != nil {
		data.LiveError = s.translate(r, "releases.liveFailed", searchErr.Error())
	}

	filter := store.Filter{
		Sort:      "recent",
		Limit:     releasesPerPage,
		SeenSince: started,
	}
	if len(form.IndexerIDs) > 0 {
		filter.IndexerIDs = append([]int64(nil), form.IndexerIDs...)
	}
	releases, total, err := s.store.SearchReleases(r.Context(), filter)
	if err != nil {
		if data.LiveError == "" {
			data.LiveError = err.Error()
		}
		s.render(w, r, "releases", data, http.StatusOK)
		return
	}
	before := len(releases)
	releases = store.FuseReleases(releases)
	if merged := int64(before - len(releases)); merged > 0 && total > merged {
		total -= merged
	}
	data.Releases = releases
	data.Pager = newPager(r, "/releases", total, releasesPerPage, 0)
	s.render(w, r, "releases", data, http.StatusOK)
}

func parseLiveSearchForm(r *http.Request) liveSearchForm {
	mode := choiceValue(r.FormValue("mode"), "search", "tvsearch", "movie", "music", "book")
	if mode == "" {
		mode = "search"
	}
	form := liveSearchForm{
		Mode:     mode,
		Query:    strings.TrimSpace(r.FormValue("q")),
		Season:   intParam(r.FormValue("season"), 0),
		Episode:  intParam(r.FormValue("episode"), 0),
		Year:     intParam(r.FormValue("year"), 0),
		IMDbID:   strings.TrimSpace(r.FormValue("imdbid")),
		TMDbID:   strings.TrimSpace(r.FormValue("tmdbid")),
		TVDbID:   strings.TrimSpace(r.FormValue("tvdbid")),
		Artist:   strings.TrimSpace(r.FormValue("artist")),
		Album:    strings.TrimSpace(r.FormValue("album")),
		Author:   strings.TrimSpace(r.FormValue("author")),
		Title:    strings.TrimSpace(r.FormValue("title")),
		Category: strings.TrimSpace(r.FormValue("cat")),
	}
	if form.Season < 0 {
		form.Season = 0
	}
	if form.Episode < 0 {
		form.Episode = 0
	}
	if form.Year < 0 {
		form.Year = 0
	}
	for _, raw := range r.Form["indexers"] {
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err == nil && value > 0 {
			form.IndexerIDs = append(form.IndexerIDs, value)
		}
	}
	return form
}

func (f liveSearchForm) hasTerms() bool {
	if strings.TrimSpace(f.Query) != "" {
		return true
	}
	for _, value := range []string{f.IMDbID, f.TMDbID, f.TVDbID, f.Artist, f.Album, f.Author, f.Title} {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func (f liveSearchForm) categoryIDs() []int {
	if f.Category == "" {
		return nil
	}
	value, err := strconv.Atoi(f.Category)
	if err != nil || value <= 0 {
		return nil
	}
	return []int{value}
}

// liveIndexerOptions builds the multi-select. On the normal page every enabled
// site is selected; after a POST the user's own selection is preserved.
func liveIndexerOptions(indexers []store.Indexer, selected []int64, defaultAll bool) []liveIndexerOption {
	chosen := make(map[int64]struct{}, len(selected))
	for _, id := range selected {
		if id > 0 {
			chosen[id] = struct{}{}
		}
	}
	out := make([]liveIndexerOption, 0, len(indexers))
	for _, indexer := range indexers {
		if !indexer.Enabled {
			continue
		}
		_, picked := chosen[indexer.ID]
		if defaultAll && len(selected) == 0 {
			picked = true
		}
		out = append(out, liveIndexerOption{ID: indexer.ID, Name: indexer.Name, Selected: picked})
	}
	return out
}

// releasesBulk is the single POST target of the results table. Delete keeps
// its old route as an alias, while grab sends every selected release and
// reports how many succeeded instead of stopping at the first failure.
func (s *Server) releasesBulk(w http.ResponseWriter, r *http.Request) {
	switch strings.ToLower(strings.TrimSpace(r.FormValue("op"))) {
	case "", "delete":
		s.releasesDelete(w, r)
	case "grab":
		s.releasesGrab(w, r)
	default:
		redirect(w, r, "/releases", "", s.translate(r, "releases.bulkInvalidOp"))
	}
}

func (s *Server) releasesGrab(w http.ResponseWriter, r *http.Request) {
	ids := releaseIDsFromForm(r)
	if len(ids) == 0 {
		redirect(w, r, "/releases", "", s.translate(r, "releases.noneSelected"))
		return
	}
	sent, failed := 0, 0
	for _, id := range ids {
		release, err := s.store.GetRelease(r.Context(), id)
		if err != nil {
			failed++
			s.log.Warn("bulk grab: release not found", "component", "indexarr",
				"subsystem", "web", "release_id", id, "error", err)
			continue
		}
		release = s.openEd2kTopic(r.Context(), release)
		cfg, _, err := s.resolveDownloadClient(r.Context(), release.Protocol)
		if err != nil {
			failed++
			s.log.Warn("bulk grab: no download client", "component", "indexarr",
				"subsystem", "web", "release_id", id, "protocol", release.Protocol, "error", err)
			continue
		}
		if found, ok, lookupErr := s.store.IndexerForRelease(r.Context(), release.ID); lookupErr == nil && ok {
			cfg.SeedRatio = found.SeedRatio
			cfg.SeedTimeMinutes = found.SeedTimeMinutes
		}
		ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
		addErr := downloadclient.Add(ctx, cfg, release)
		cancel()
		if addErr != nil {
			failed++
			s.log.Warn("bulk grab: download client refused the release", "component", "indexarr",
				"subsystem", "downloadclient", "release_id", id, "client", cfg.Kind, "error", addErr)
			continue
		}
		sent++
		s.log.Info("release sent to download client", "release_id", id, "client", cfg.Kind, "title", release.Title)
		if strings.TrimSpace(release.Ed2k) != "" {
			s.recordGrab(r.Context(), release, indexer.GrabViaEd2k)
		}
	}
	message := s.translate(r, "releases.bulkGrabResult", sent, failed)
	if failed > 0 {
		redirect(w, r, "/releases", "", message)
		return
	}
	redirect(w, r, "/releases", message, "")
}
