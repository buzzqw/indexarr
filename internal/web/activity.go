package web

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/logtext"
	"github.com/buzzqw/indexarr/internal/store"
)

const activityPageSize = 50

// activityFeedView is the "new releases" stream: every release in the cache,
// newest first, with its provenance and a server-side page.
type activityFeedView struct {
	Releases  []feedReleaseView
	Indexers  []store.Indexer
	IndexerID int64
	Pager     pager
	Empty     uiEmpty
}

// feedReleaseView is one feed row with the indexers that saw it pre-joined, so
// the template never has to call a function to render provenance.
type feedReleaseView struct {
	Release      store.Release
	IndexerCount int
	Names        string
}

type appSearchView struct {
	App         string
	Mode        string
	Query       string
	Params      string
	Indexers    string
	ResultRows  []appSearchResult
	Live        []appSearchLiveView
	Total       int64
	Duplicates  int64
	Duration    string
	Error       string
	CreatedAt   time.Time
	HasDuration bool
	// Summary counts of the live fan-out, shown on the collapsed row. Late,
	// Queued and Skipped are not failures: only Failed is.
	InTime  int
	Late    int
	Queued  int
	Skipped int
	Failed  int
	// What is the translated search kind (TV, film, free text).
	What string
}

// appSearchLiveView is one indexer of the live fan-out: effective mode,
// fallback, results, time and whether it exceeded the request timeout.
type appSearchLiveView struct {
	Name     string
	Mode     string
	Query    string
	Results  int64
	Duration string
	Fallback bool
	TimedOut bool
	Skipped  bool
	Error    string
	// State is ok, late, queued, skipped or error; Reason explains a skipped,
	// queued or failed source in plain words.
	State  string
	Reason string
}

type appSearchResult struct {
	Name  string
	Count int64
}

// activityPage is the Statistics section: the requests received from the
// apps, their aggregated counters, the downloads, the source runs, the
// new-release feed and the per-host network counters, one tab each. The
// process log lives on its own page (/logs).
type activityPage struct {
	base
	Tab         string
	Runs        runsPage
	Feed        *activityFeedView
	Requests    *logsRequestsView
	Stats       *logsStatsView
	Downloads   *logsDownloadsView
	HostMetrics []engine.HostMetric
	// TotalRequests and ErrorRequests feed the Requests tab chips, GrabsToday
	// the Downloads one.
	TotalRequests int64
	ErrorRequests int64
	GrabsToday    int64
}

func (s *Server) activityPage(w http.ResponseWriter, r *http.Request) {
	tab := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tab")))
	switch tab {
	case "logs":
		// The process log moved to the Log section: keep the old links and
		// their filters working.
		values := r.URL.Query()
		values.Del("tab")
		target := "/logs"
		if encoded := values.Encode(); encoded != "" {
			target += "?" + encoded
		}
		http.Redirect(w, r, target, http.StatusFound)
		return
	case "searches":
		// Searches was the same table as Requests, without the counters.
		redirectTab(w, r, "/activity", "requests")
		return
	case "requests", "stats", "downloads", "runs", "feed", "network":
	default:
		tab = "requests"
	}
	data := activityPage{
		base: s.newBaseT(r, "activity.title", "activity"),
		Tab:  tab,
	}
	data.TotalRequests, _ = s.store.CountAppSearches(r.Context())
	data.ErrorRequests, _ = s.store.CountAppSearchesFiltered(r.Context(), true)
	data.GrabsToday, _ = s.store.CountGrabs(r.Context(), time.Now().Add(-24*time.Hour))
	data.HostMetrics = s.polite.HostMetrics()
	switch tab {
	case "requests":
		view, err := s.logsRequests(r)
		if err != nil {
			s.renderError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		data.Requests = view
		data.LiveRefresh = 10
	case "stats":
		view, err := s.logsStats(r)
		if err != nil {
			s.renderError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		data.Stats = view
	case "downloads":
		view, err := s.logsDownloads(r)
		if err != nil {
			s.renderError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		data.Downloads = view
	case "feed":
		feed, err := s.activityFeed(r)
		if err != nil {
			s.renderError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		data.Feed = feed
		// Refresh only while the tab is visible; app.js implements the check.
		data.LiveRefresh = 10
	case "network":
	default:
		runs, err := s.runsData(r)
		if err != nil {
			s.renderError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		data.Runs = runs
	}
	s.render(w, r, "activity", data, http.StatusOK)
}

func (s *Server) activityFeed(r *http.Request) (*activityFeedView, error) {
	indexerID := int64Param(r.URL.Query().Get("indexer"), 0)
	page := intParam(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * activityPageSize
	releases, total, err := s.store.ListFeedReleases(r.Context(), indexerID, activityPageSize, offset)
	if err != nil {
		return nil, err
	}
	indexers, err := s.store.ListIndexers(r.Context())
	if err != nil {
		return nil, err
	}
	view := &activityFeedView{
		Indexers:  indexers,
		IndexerID: indexerID,
		Pager:     newPager(r, "/activity", total, activityPageSize, offset),
		Empty: uiEmpty{
			Title: s.translate(r, "activity.feed.emptyTitle"),
			Text:  s.translate(r, "activity.feed.emptyText"),
		},
	}
	for _, release := range releases {
		view.Releases = append(view.Releases, feedReleaseView{
			Release:      release.Release,
			IndexerCount: release.IndexerCount,
			Names:        strings.Join(release.IndexerNames, ", "),
		})
	}
	return view, nil
}

// appSearchView converts one stored request into the row rendered by the
// Requests table of the Statistics section.
func (s *Server) appSearchView(lang i18n.Language, search store.AppSearch) appSearchView {
	row := appSearchView{
		What:       searchModeLabel(lang, search.Mode),
		App:        search.App,
		Mode:       search.Mode,
		Query:      search.Query,
		Params:     search.Params,
		Indexers:   strings.Join(search.IndexerNames, ", "),
		Total:      search.TotalResults,
		Duplicates: search.DuplicatesMerged,
		Error:      search.Error,
		CreatedAt:  search.CreatedAt,
	}
	for _, entry := range search.Live {
		view := appSearchLiveView{
			Name:     entry.Name,
			Mode:     entry.Mode,
			Query:    entry.Query,
			Results:  entry.Results,
			Fallback: entry.Fallback,
			TimedOut: entry.TimedOut,
			Skipped:  entry.Skipped,
			Error:    entry.Error,
		}
		view.State, view.Reason = s.liveState(lang, entry)
		switch view.State {
		case "late":
			row.Late++
		case "queued":
			row.Queued++
		case "skipped":
			row.Skipped++
		case "error":
			row.Failed++
		default:
			row.InTime++
		}
		if entry.Duration > 0 {
			if entry.Duration < 1000 {
				view.Duration = strconv.FormatInt(entry.Duration, 10) + " ms"
			} else {
				view.Duration = strconv.FormatFloat(float64(entry.Duration)/1000, 'f', 1, 64) + " s"
			}
		}
		row.Live = append(row.Live, view)
	}
	if search.DurationMS > 0 {
		row.HasDuration = true
		if search.DurationMS < 1000 {
			row.Duration = strconv.FormatInt(search.DurationMS, 10) + " ms"
		} else {
			row.Duration = strconv.FormatFloat(float64(search.DurationMS)/1000, 'f', 1, 64) + " s"
		}
	}
	names := make([]string, 0, len(search.Results))
	for name := range search.Results {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	for _, name := range names {
		row.ResultRows = append(row.ResultRows, appSearchResult{
			Name: name, Count: search.Results[name],
		})
	}
	return row
}

// liveState classifies one source of a live fan-out. A source that answered
// after the app got its response kept working and stored its rows for the
// next request: that is "late", not a failure.
func (s *Server) liveState(lang i18n.Language, entry store.AppSearchLive) (string, string) {
	errText := strings.ToLower(strings.TrimSpace(entry.Error))
	switch {
	case entry.Skipped:
		switch {
		case strings.Contains(errText, "feed-only"):
			return "skipped", i18n.T(lang, "logs.live.reason.feedOnly")
		case strings.Contains(errText, "circuit"):
			return "skipped", i18n.T(lang, "logs.live.reason.paused")
		case strings.Contains(errText, "queue"):
			return "skipped", i18n.T(lang, "logs.live.reason.queue")
		case strings.Contains(errText, "no request is waiting"):
			return "skipped", i18n.T(lang, "logs.live.reason.abandoned")
		}
		return "skipped", entry.Error
	case entry.TimedOut && strings.Contains(errText, "waiting for a slot"):
		return "queued", i18n.T(lang, "logs.live.reason.queued")
	case entry.TimedOut:
		return "late", i18n.T(lang, "logs.live.reason.late")
	case errText != "":
		if code := logtext.Problem(entry.Error); code != "" {
			return "error", i18n.T(lang, "logs.problem."+code)
		}
		return "error", entry.Error
	}
	return "ok", ""
}

// searchModeLabel names the Torznab search kind in the user's language.
func searchModeLabel(lang i18n.Language, mode string) string {
	key := "logs.mode." + strings.ToLower(strings.TrimSpace(mode))
	if text := i18n.T(lang, key); text != key {
		return text
	}
	return mode
}
