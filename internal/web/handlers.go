package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/store"
)

const releasesPerPage = 50

// dashboardStatus is the single error summary of the overview. It merges the
// current state of every indexer and legacy source with the failed runs of the
// last 24 hours, so the page cannot say "all clear" while the run counter
// reports errors (or the other way round).
type dashboardStatus struct {
	Current   int   // indexers or sources with a non-empty last_error
	RunErrors int64 // failed runs in the last 24 hours
	Runs      int64 // runs in the last 24 hours
}

func (s dashboardStatus) OK() bool {
	return s.Current == 0 && s.RunErrors == 0
}

type dashboardPage struct {
	base
	Stats          store.Stats
	Sources        []store.Source
	Indexers       []store.Indexer
	Applications   []store.Application
	Definitions    []store.Definition
	Running        map[int64]bool
	Runs           []store.Run
	Recent         []store.Release
	Status         dashboardStatus
	NativeErrors   int
	LegacyErrors   int
	NativeCount    int
	LegacyCount    int
	ApplicationNum int
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stats, err := s.store.LoadStats(ctx)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	sources, err := s.store.ListSources(ctx)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	indexers, err := s.store.ListIndexers(ctx)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	applications, err := s.store.ListApplications(ctx)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	definitions, err := s.store.ListDefinitions(ctx)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	runs, _ := s.store.ListRuns(ctx, 0, false, 6)
	recent, _ := s.store.ListRecentReleases(ctx, 10)
	running := map[int64]bool{}
	legacyErrors := 0
	for _, source := range sources {
		running[source.ID] = s.engine != nil && s.engine.IsRunning(source.ID)
		if source.LastError != "" {
			legacyErrors++
		}
	}
	nativeIndexers := make([]store.Indexer, 0, len(indexers))
	nativeErrors := 0
	for _, in := range indexers {
		if in.LegacySourceID > 0 {
			continue
		}
		nativeIndexers = append(nativeIndexers, in)
		if state, err := s.store.GetIndexerState(ctx, in.ID); err == nil && state.LastError != "" {
			nativeErrors++
		}
	}
	localDefinitions := make([]store.Definition, 0, len(definitions))
	for _, def := range definitions {
		if strings.HasPrefix(def.ID, "legacy-") {
			continue
		}
		localDefinitions = append(localDefinitions, def)
	}
	s.render(w, r, "dashboard", dashboardPage{
		base:         s.newBaseT(r, "dashboard.title", "dashboard"),
		Stats:        stats,
		Sources:      sources,
		Indexers:     nativeIndexers,
		Applications: applications,
		Definitions:  localDefinitions,
		Running:      running,
		Runs:         runs,
		Recent:       recent,
		Status: dashboardStatus{
			Current:   nativeErrors + legacyErrors,
			RunErrors: stats.Errors24h,
			Runs:      stats.Runs24h,
		},
		NativeErrors:   nativeErrors,
		LegacyErrors:   legacyErrors,
		NativeCount:    len(nativeIndexers),
		LegacyCount:    len(sources),
		ApplicationNum: len(applications),
	}, http.StatusOK)
}

type releasesPage struct {
	base
	Releases      []store.Release
	Sources       []store.Source
	Pager         pager
	Query         string
	Source        int64
	Category      string
	Sort          string
	Regex         bool
	InvalidRegex  string
	FavoritesOnly bool
	ShowHidden    bool
	MinSeeders    int64
	MinSizeMB     int
	MaxSizeMB     int
	Quality       string
	SourceQuality string
	Language      string
	SavedSearches []store.SavedSearch
	CurrentQuery  string
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
	sourceID := int64Param(query.Get("source"), 0)
	category := strings.TrimSpace(query.Get("cat"))
	sortMode := query.Get("sort")
	favoritesOnly := query.Get("favorites") == "1"
	showHidden := query.Get("hidden") == "1"
	minSeeders := int64Param(query.Get("min_seeders"), 0)
	minSizeMB := intParam(query.Get("min_size_mb"), 0)
	maxSizeMB := intParam(query.Get("max_size_mb"), 0)
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

	sources, _ := s.store.ListSources(r.Context())
	saved, _ := s.store.ListSavedSearches(r.Context())
	current := query
	current.Del("page")
	data := releasesPage{
		base:          s.newBase(r, "Release", "releases"),
		Sources:       sources,
		Query:         searchQuery,
		Source:        sourceID,
		Category:      category,
		Sort:          sortMode,
		Regex:         regexMode,
		FavoritesOnly: favoritesOnly,
		ShowHidden:    showHidden,
		MinSeeders:    minSeeders,
		MinSizeMB:     minSizeMB,
		MaxSizeMB:     maxSizeMB,
		Quality:       quality,
		SourceQuality: sourceQuality,
		Language:      language,
		SavedSearches: saved,
		CurrentQuery:  current.Encode(),
	}
	if parseErr != nil {
		data.InvalidRegex = parseErr.Error()
		data.Pager = newPager(r, "/releases", 0, releasesPerPage, 0)
		return data, nil
	}

	filter := store.Filter{
		SourceID:      sourceID,
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
	data.Releases = releases
	data.Pager = newPager(r, "/releases", total, releasesPerPage, filter.Offset)
	return data, nil
}

type releasePage struct {
	base
	Release        store.Release
	TorrentURL     string
	DownloadClient string
}

func (s *Server) releasePage(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "invalid release id")
		return
	}
	release, err := s.store.GetRelease(r.Context(), id)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "release not found")
		return
	}
	clientName := ""
	if settings, err := s.store.LoadSettings(r.Context()); err == nil {
		switch settings.DownloadClient {
		case "qbittorrent":
			clientName = "qBittorrent"
		case "transmission":
			clientName = "Transmission"
		}
	}
	s.render(w, r, "release", releasePage{
		base:           s.newBase(r, "Release", "releases"),
		Release:        release,
		TorrentURL:     fmt.Sprintf("/releases/%d/torrent", release.ID),
		DownloadClient: clientName,
	}, http.StatusOK)
}

func (s *Server) releaseTorrent(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, "invalid release id")
		return
	}
	release, err := s.store.GetRelease(r.Context(), id)
	if err != nil {
		s.renderError(w, r, http.StatusNotFound, "release not found")
		return
	}
	if len(release.Torrent) == 0 {
		s.renderError(w, r, http.StatusNotFound, "no .torrent stored for this release; use the magnet link")
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
		redirect(w, r, "/releases", "", "invalid release id")
		return
	}
	deleted, err := s.store.DeleteRelease(r.Context(), id)
	if err != nil {
		redirect(w, r, "/releases", "", err.Error())
		return
	}
	if !deleted {
		redirect(w, r, "/releases", "", "release not found")
		return
	}
	redirect(w, r, "/releases", "Release deleted", "")
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
		redirect(w, r, "/releases", "", "No releases selected")
		return
	}
	removed, err := s.store.DeleteReleases(r.Context(), ids)
	if err != nil {
		redirect(w, r, "/releases", "", err.Error())
		return
	}
	redirect(w, r, "/releases", fmt.Sprintf("%d release(s) deleted", removed), "")
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
