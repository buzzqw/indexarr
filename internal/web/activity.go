package web

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
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

// activitySearchesView is the "searches" stream: requests received from
// Torznab/Newznab clients, newest first, with per-indexer result counts.
type activitySearchesView struct {
	Searches []appSearchView
	Pager    pager
	Empty    uiEmpty
}

type appSearchView struct {
	App         string
	Mode        string
	Query       string
	Indexers    string
	ResultRows  []appSearchResult
	Total       int64
	Duration    string
	Error       string
	CreatedAt   time.Time
	HasDuration bool
}

type appSearchResult struct {
	Name  string
	Count int64
}

// activityPage unifies the run history, the new-release feed, the searches
// received from apps and the in-memory log in one tabbed page.
type activityPage struct {
	base
	Tab         string
	Runs        runsPage
	Logs        logsPage
	Feed        *activityFeedView
	Searches    *activitySearchesView
	HostMetrics []engine.HostMetric
}

func (s *Server) activityPage(w http.ResponseWriter, r *http.Request) {
	tab := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tab")))
	switch tab {
	case "logs", "feed", "searches":
	default:
		tab = "runs"
	}
	data := activityPage{
		base: s.newBaseT(r, "activity.title", "activity"),
		Tab:  tab,
	}
	switch tab {
	case "feed":
		feed, err := s.activityFeed(r)
		if err != nil {
			s.renderError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		data.Feed = feed
		// Refresh only while the tab is visible; app.js implements the check.
		data.LiveRefresh = 10
	case "searches":
		searches, err := s.activitySearches(r)
		if err != nil {
			s.renderError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		data.Searches = searches
		data.LiveRefresh = 10
	case "logs":
		logs := s.logsData(r)
		data.Logs = logs
		data.AutoRefresh = logs.AutoRefresh
	default:
		runs, err := s.runsData(r)
		if err != nil {
			s.renderError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		data.Runs = runs
	}
	data.HostMetrics = s.polite.HostMetrics()
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

func (s *Server) activitySearches(r *http.Request) (*activitySearchesView, error) {
	page := intParam(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * activityPageSize
	total, err := s.store.CountAppSearches(r.Context())
	if err != nil {
		return nil, err
	}
	searches, err := s.store.ListAppSearches(r.Context(), activityPageSize, offset)
	if err != nil {
		return nil, err
	}
	view := &activitySearchesView{
		Pager: newPager(r, "/activity", total, activityPageSize, offset),
		Empty: uiEmpty{
			Title: s.translate(r, "activity.searches.emptyTitle"),
			Text:  s.translate(r, "activity.searches.emptyText"),
		},
	}
	for _, search := range searches {
		row := appSearchView{
			App:       search.App,
			Mode:      search.Mode,
			Query:     search.Query,
			Indexers:  strings.Join(search.IndexerNames, ", "),
			Total:     search.TotalResults,
			Error:     search.Error,
			CreatedAt: search.CreatedAt,
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
		view.Searches = append(view.Searches, row)
	}
	return view, nil
}
