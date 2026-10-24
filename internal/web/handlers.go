package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/store"
)

const releasesPerPage = 50

type dashboardPage struct {
	base
	Status       HomeStatus
	Activity     store.HomeActivity
	NextStep     *homeNextStep
	Clients      []store.DownloadClient
	SuggestedURL string
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	settings, err := s.store.LoadSettings(ctx)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	// First run: the dashboard is replaced by the four-step wizard until the
	// operator adds an indexer, finishes or explicitly skips it.
	if s.shouldShowSetup(r, settings, len(indexers)) {
		data, err := s.setupData(r, 1)
		if err != nil {
			s.renderError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		s.render(w, r, "setup", data, http.StatusOK)
		return
	}
	status := s.HomeStatus(ctx)
	activity, err := s.store.LoadHomeActivity(ctx)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	clients, err := s.store.ListDownloadClients(ctx)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.render(w, r, "dashboard", dashboardPage{
		base:         s.newBaseT(r, "dashboard.title", "dashboard"),
		Status:       status,
		Activity:     activity,
		NextStep:     s.nextStep(ctx, status, settings, len(clients)),
		Clients:      clients,
		SuggestedURL: suggestedPublicURL(r),
	}, http.StatusOK)
}

type releasesPage struct {
	base
	Releases       []store.Release
	Indexers       []store.Indexer
	Pager          pager
	Query          string
	Indexer        int64
	Category       string
	Sort           string
	Regex          bool
	InvalidRegex   string
	FavoritesOnly  bool
	ShowHidden     bool
	ShowDuplicates bool
	MinSeeders     int64
	MinSizeMB      int
	MaxSizeMB      int
	// MinSeedersText, MinSizeText and MaxSizeText are the raw query values. An
	// unset numeric filter is rendered as an empty field, never as 0.
	MinSeedersText      string
	MinSizeText         string
	MaxSizeText         string
	Quality             string
	SourceQuality       string
	Language            string
	AdvancedFilterCount int
	SavedSearches       []store.SavedSearch
	CurrentQuery        string
}

func (s *Server) releasesPage(w http.ResponseWriter, r *http.Request) {
	s.renderReleases(w, r, false)
}

func (s *Server) releasesResults(w http.ResponseWriter, r *http.Request) {
	s.renderReleases(w, r, true)
}

func (s *Server) renderReleases(w http.ResponseWriter, r *http.Request, partial bool) {
	data, err := s.releasesData(r)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	if partial {
		s.renderPartial(w, r, "releases_results", data)
		return
	}
	s.render(w, r, "releases", data, http.StatusOK)
}

func (s *Server) releasesData(r *http.Request) (releasesPage, error) {
	query := r.URL.Query()
	searchQuery := strings.TrimSpace(query.Get("q"))
	regexMode := query.Get("regex") == "1"
	page := intParam(query.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	indexerID := int64Param(query.Get("indexer"), 0)
	if indexerID == 0 {
		// Keep old ?source= links working.
		indexerID = int64Param(query.Get("source"), 0)
	}
	category := strings.TrimSpace(query.Get("cat"))
	sortMode := query.Get("sort")
	favoritesOnly := query.Get("favorites") == "1"
	showHidden := query.Get("hidden") == "1"
	showDuplicates := query.Get("duplicates") == "1"
	minSeedersRaw := strings.TrimSpace(query.Get("min_seeders"))
	minSizeRaw := strings.TrimSpace(query.Get("min_size_mb"))
	maxSizeRaw := strings.TrimSpace(query.Get("max_size_mb"))
	minSeeders := int64Param(minSeedersRaw, 0)
	minSizeMB := intParam(minSizeRaw, 0)
	maxSizeMB := intParam(maxSizeRaw, 0)
	if minSizeMB < 0 {
		minSizeMB = 0
	}
	if maxSizeMB < 0 {
		maxSizeMB = 0
	}
	quality := choiceValue(query.Get("quality"), "2160p", "1080p", "720p", "480p")
	sourceQuality := choiceValue(query.Get("source_quality"), "webdl", "bluray", "hdtv")
	language := choiceValue(query.Get("language"), "ita", "eng")
	include, exclude, parseErr := parseSearchTerms(searchQuery, regexMode)

	minSeedersText := ""
	if minSeeders > 0 {
		minSeedersText = minSeedersRaw
	}
	minSizeText := ""
	if minSizeMB > 0 {
		minSizeText = minSizeRaw
	}
	maxSizeText := ""
	if maxSizeMB > 0 {
		maxSizeText = maxSizeRaw
	}
	advancedFilterCount := 0
	if quality != "" {
		advancedFilterCount++
	}
	if sourceQuality != "" {
		advancedFilterCount++
	}
	if language != "" {
		advancedFilterCount++
	}
	if minSeeders > 0 {
		advancedFilterCount++
	}
	if minSizeMB > 0 {
		advancedFilterCount++
	}
	if maxSizeMB > 0 {
		advancedFilterCount++
	}
	if favoritesOnly {
		advancedFilterCount++
	}
	if showHidden {
		advancedFilterCount++
	}
	if showDuplicates {
		advancedFilterCount++
	}

	indexers, _ := s.store.ListIndexers(r.Context())
	saved, _ := s.store.ListSavedSearches(r.Context())
	current := query
	current.Del("page")
	data := releasesPage{
		base:                s.newBaseT(r, "releases.title", "releases"),
		Indexers:            indexers,
		Query:               searchQuery,
		Indexer:             indexerID,
		Category:            category,
		Sort:                sortMode,
		Regex:               regexMode,
		FavoritesOnly:       favoritesOnly,
		ShowHidden:          showHidden,
		ShowDuplicates:      showDuplicates,
		MinSeeders:          minSeeders,
		MinSizeMB:           minSizeMB,
		MaxSizeMB:           maxSizeMB,
		MinSeedersText:      minSeedersText,
		MinSizeText:         minSizeText,
		MaxSizeText:         maxSizeText,
		Quality:             quality,
		SourceQuality:       sourceQuality,
		Language:            language,
		AdvancedFilterCount: advancedFilterCount,
		SavedSearches:       saved,
		CurrentQuery:        current.Encode(),
	}
	if parseErr != nil {
		data.InvalidRegex = parseErr.Error()
		data.Pager = newPager(r, "/releases", 0, releasesPerPage, 0)
		return data, nil
	}

	filter := store.Filter{
		IndexerID:     indexerID,
		Sort:          sortMode,
		Limit:         releasesPerPage,
		Offset:        (page - 1) * releasesPerPage,
		Include:       include,
		Exclude:       exclude,
		Regex:         regexMode,
		FavoritesOnly: favoritesOnly,
		IncludeHidden: showHidden || favoritesOnly,
		MinSeeders:    minSeeders,
		MinSize:       int64(minSizeMB) << 20,
		MaxSize:       int64(maxSizeMB) << 20,
		Quality:       quality,
		SourceQuality: sourceQuality,
		Language:      language,
	}
	if category != "" {
		filter.Categories = []string{category}
	}
	releases, total, err := s.store.SearchReleases(r.Context(), filter)
	if err != nil {
		return data, err
	}
	if !showDuplicates {
		before := len(releases)
		releases = store.FuseReleases(releases)
		if merged := int64(before - len(releases)); merged > 0 && total > merged {
			total -= merged
		}
	}
	data.Releases = releases
	data.Pager = newPager(r, "/releases", total, releasesPerPage, filter.Offset)
	return data, nil
}

type releasePage struct {
	base
	Release        store.Release
	Sources        []store.ReleaseSourceInfo
	TorrentURL     string
	DownloadClient string
}

func (s *Server) releasePage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "releases.error.invalidID"))
		return
	}
	release, err := s.store.GetRelease(r.Context(), id)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, s.translate(r, "releases.error.notFound"))
		return
	}
	clientName := ""
	if cfg, name, err := s.resolveDownloadClient(r.Context(), release.Protocol); err == nil {
		_ = cfg
		clientName = name
	}
	sources, _ := s.store.ReleaseSourcesFor(r.Context(), release.ID)
	s.render(w, r, "release", releasePage{
		base:           s.newBaseT(r, "releases.title", "releases"),
		Release:        release,
		Sources:        sources,
		TorrentURL:     fmt.Sprintf("/releases/%d/torrent", release.ID),
		DownloadClient: clientName,
	}, http.StatusOK)
}

func (s *Server) releaseTorrent(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "releases.error.invalidID"))
		return
	}
	release, err := s.store.GetRelease(r.Context(), id)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, s.translate(r, "releases.error.notFound"))
		return
	}
	if len(release.Torrent) == 0 {
		s.renderError(w, r, http.StatusNotFound, s.translate(r, "releases.error.noTorrent"))
		return
	}
	w.Header().Set("Content-Type", "application/x-bittorrent")
	w.Header().Set("Content-Disposition", `attachment; filename="`+downloadFilename(release.Title)+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(release.Torrent)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(release.Torrent)
}

func (s *Server) releaseDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/releases", "", s.translate(r, "releases.error.invalidID"))
		return
	}
	deleted, err := s.store.DeleteRelease(r.Context(), id)
	if err != nil {
		redirect(w, r, "/releases", "", err.Error())
		return
	}
	if !deleted {
		redirect(w, r, "/releases", "", s.translate(r, "releases.error.notFound"))
		return
	}
	redirect(w, r, "/releases", s.translate(r, "releases.deleted"), "")
}

func (s *Server) releasesDelete(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	for _, raw := range r.Form["ids"] {
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err == nil && value > 0 {
			ids = append(ids, value)
		}
	}
	if len(ids) == 0 {
		redirect(w, r, "/releases", "", s.translate(r, "releases.noneSelected"))
		return
	}
	removed, err := s.store.DeleteReleases(r.Context(), ids)
	if err != nil {
		redirect(w, r, "/releases", "", err.Error())
		return
	}
	redirect(w, r, "/releases", s.translate(r, "releases.deletedCount", removed), "")
}

func pathID(r *http.Request) (int64, error) {
	value, err := strconv.ParseInt(strings.TrimSpace(r.PathValue("id")), 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid id")
	}
	return value, nil
}

func intParam(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}

func int64Param(raw string, fallback int64) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}
