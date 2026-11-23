package web

import (
	"context"
	"net/http"
	"net/url"
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
	s.renderLiveSearch(w, r, parseLiveSearchForm(r))
}

// renderLiveSearch runs the on-demand fan-out and renders the unified search
// page with the live source selected. releasesLive and the source=live branch
// of releasesSearch both end here.
func (s *Server) renderLiveSearch(w http.ResponseWriter, r *http.Request, form liveSearchForm) {
	indexers, _ := s.store.ListIndexers(r.Context())
	saved, _ := s.store.ListSavedSearches(r.Context())
	data := releasesPage{
		base:          s.newBaseT(r, "releases.title", "releases"),
		Indexers:      indexers,
		SavedSearches: saved,
		SearchSource:  "live",
		Query:         form.Query,
		Category:      form.Category,
		IndexerIDs:    append([]int64(nil), form.IndexerIDs...),
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
	limit := liveLimit(form.Limit)
	data.LiveLimit = limit
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
		Limit:      limit,
		IndexerIDs: form.IndexerIDs,
		ForceLive:  true,
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
		Limit:     limit,
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
	data.LiveSince = started.Unix()
	data.Pager = livePager(r, livePageValues(form, limit, started.Unix()), total, limit, 0)
	s.render(w, r, "releases", data, http.StatusOK)
}

// releasesLivePage serves the later pages of a live search without running the
// fan-out again: the rows were stored by the first request and carry a
// last_seen_at at or after live_since.
func (s *Server) releasesLivePage(w http.ResponseWriter, r *http.Request) {
	form := parseLiveSearchForm(r)
	limit := liveLimit(form.Limit)
	since := int64Param(r.URL.Query().Get("live_since"), 0)
	page := intParam(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	indexers, _ := s.store.ListIndexers(r.Context())
	saved, _ := s.store.ListSavedSearches(r.Context())
	data := releasesPage{
		base:          s.newBaseT(r, "releases.title", "releases"),
		Indexers:      indexers,
		SavedSearches: saved,
		SearchSource:  "live",
		Query:         form.Query,
		Category:      form.Category,
		IndexerIDs:    append([]int64(nil), form.IndexerIDs...),
		LiveForm:      form,
		LiveIndexers:  liveIndexerOptions(indexers, form.IndexerIDs, false),
		LivePerformed: since > 0,
		LiveSince:     since,
		LiveLimit:     limit,
	}
	if since <= 0 {
		data.LiveError = s.translate(r, "releases.livePageExpired")
		s.render(w, r, "releases", data, http.StatusOK)
		return
	}
	offset := (page - 1) * limit
	filter := store.Filter{
		Sort:      "recent",
		Limit:     limit,
		Offset:    offset,
		SeenSince: time.Unix(since, 0),
	}
	if len(form.IndexerIDs) > 0 {
		filter.IndexerIDs = append([]int64(nil), form.IndexerIDs...)
	}
	releases, total, err := s.store.SearchReleases(r.Context(), filter)
	if err != nil {
		data.LiveError = err.Error()
		s.render(w, r, "releases", data, http.StatusOK)
		return
	}
	before := len(releases)
	releases = store.FuseReleases(releases)
	if merged := int64(before - len(releases)); merged > 0 && total > merged {
		total -= merged
	}
	data.Releases = releases
	data.Pager = newPager(r, "/releases/live", total, limit, offset)
	s.render(w, r, "releases", data, http.StatusOK)
}

// liveLimit keeps the page size inside a bound: a larger answer is still
// bounded by the server, and a missing or invalid value keeps the old 50.
func liveLimit(raw int) int {
	if raw < releasesPerPage {
		return releasesPerPage
	}
	if raw > 500 {
		return 500
	}
	return raw
}

// livePageValues carries the form and the timestamp of the first fan-out into
// every later page, so paging never asks the sites again.
func livePageValues(form liveSearchForm, limit int, since int64) url.Values {
	values := url.Values{}
	values.Set("source", "live")
	values.Set("live_since", strconv.FormatInt(since, 10))
	values.Set("live_limit", strconv.Itoa(limit))
	for key, value := range map[string]string{
		"mode": form.Mode, "q": form.Query, "live_season": strconv.Itoa(form.Season),
		"episode": strconv.Itoa(form.Episode), "year": strconv.Itoa(form.Year),
		"imdbid": form.IMDbID, "tmdbid": form.TMDbID, "tvdbid": form.TVDbID,
		"artist": form.Artist, "album": form.Album, "author": form.Author,
		"title": form.Title, "cat": form.Category,
	} {
		if value != "" && value != "0" {
			values.Set(key, value)
		}
	}
	for _, id := range form.IndexerIDs {
		values.Add("indexers", strconv.FormatInt(id, 10))
	}
	return values
}

func livePager(r *http.Request, values url.Values, total int64, limit, offset int) pager {
	paged := r.Clone(r.Context())
	target := *r.URL
	target.RawQuery = values.Encode()
	paged.URL = &target
	return newPager(paged, "/releases/live", total, limit, offset)
}

func parseLiveSearchForm(r *http.Request) liveSearchForm {
	mode := choiceValue(r.FormValue("mode"), "search", "tvsearch", "movie", "music", "book")
	if mode == "" {
		mode = "search"
	}
	// The live form has its own season field: the catalogue filter uses
	// "season" on the same form, so the two must not share a name. A request
	// without "live_season" (an old bookmark) still reads "season".
	_ = r.ParseForm()
	seasonRaw := r.FormValue("season")
	if r.Form.Has("live_season") {
		seasonRaw = r.FormValue("live_season")
	}
	form := liveSearchForm{
		Mode:     mode,
		Query:    strings.TrimSpace(r.FormValue("q")),
		Season:   intParam(seasonRaw, 0),
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
		Limit:    intParam(r.FormValue("live_limit"), releasesPerPage),
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
// its old route as an alias, grab sends every selected release and the
// curation actions set flags or add blocklist rules, all reporting how many
// succeeded instead of stopping at the first failure.
func (s *Server) releasesBulk(w http.ResponseWriter, r *http.Request) {
	switch strings.ToLower(strings.TrimSpace(r.FormValue("op"))) {
	case "", "delete":
		s.releasesDelete(w, r)
	case "grab":
		s.releasesGrab(w, r)
	case "favorite", "unfavorite", "hide", "unhide", "block":
		s.releasesCurate(w, r)
	default:
		redirect(w, r, bulkReturnURL(r), "", s.translate(r, "releases.bulkInvalidOp"))
	}
}

func (s *Server) releasesGrab(w http.ResponseWriter, r *http.Request) {
	ids := releaseIDsFromForm(r)
	if len(ids) == 0 {
		redirect(w, r, bulkReturnURL(r), "", s.translate(r, "releases.noneSelected"))
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
		indexerID := int64(0)
		var source store.Indexer
		if found, ok, lookupErr := s.store.IndexerForRelease(r.Context(), release.ID); lookupErr == nil && ok {
			indexerID = found.ID
			source = found
		}
		cfg, _, err := s.resolveDownloadClientFor(r.Context(), release.Protocol, indexerID)
		if err != nil {
			failed++
			s.log.Warn("bulk grab: no download client", "component", "indexarr",
				"subsystem", "web", "release_id", id, "protocol", release.Protocol, "error", err)
			continue
		}
		if source.ID != 0 {
			cfg.SeedRatio = source.SeedRatio
			cfg.SeedTimeMinutes = source.SeedTimeMinutes
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
		redirect(w, r, bulkReturnURL(r), "", message)
		return
	}
	redirect(w, r, bulkReturnURL(r), message, "")
}
