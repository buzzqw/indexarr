package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/store"
)

// logsSectionPage is the Logs section: internal process log, the requests
// received from the downstream apps, and the aggregated request statistics.
type logsSectionPage struct {
	base
	Tab      string
	Internal logsPage
	Requests *logsRequestsView
	Stats    *logsStatsView
	// TotalRequests and ErrorRequests feed the tab chips; LogLines is the
	// number of lines currently held by the log buffer.
	TotalRequests int64
	ErrorRequests int64
	LogLines      int
}

// logsRequestsView is the "Richieste" tab: the request counters of a window
// plus the newest requests, optionally only the failed ones.
type logsRequestsView struct {
	Searches    []appSearchView
	Pager       pager
	Empty       uiEmpty
	Window      string
	WindowLabel string
	Windows     []logsWindowOption
	Totals      store.AppSearchTotals
	SuccessRate string
	AvgLatency  string
	OnlyErrors  bool
	ResetURL    string
}

// logsStatsView is the "Statistiche" tab: request counters per app and per
// indexer, plus the source-download ranking already shown in Maintenance.
type logsStatsView struct {
	Window      string
	WindowLabel string
	Windows     []logsWindowOption
	Totals      store.AppSearchTotals
	SuccessRate string
	AvgLatency  string
	Apps        []logsAppRow
	Indexers    []logsIndexerRow
	IndexerCap  int

	UsefulSources []usefulSourceView
	UsefulTotal   int64
	UsefulDays    int
}

// logsAppRow is one downstream app in the statistics table.
type logsAppRow struct {
	App      string
	Requests int64
	OK       int64
	Errors   int64
	Results  int64
	Avg      string
}

// logsIndexerRow is one indexer in the statistics table.
type logsIndexerRow struct {
	Name     string
	Requests int64
	Failures int64
	Results  int64
	Avg      string
}

// logsWindowOption is one selectable time window in the request views.
type logsWindowOption struct {
	Key    string
	Label  string
	URL    string
	Active bool
}

const (
	logsRequestPageSize = 50
	logsIndexerTop      = 30
	logsUsefulDays      = 30
)

// statWindowKeys lists the supported windows in display order. "all" has no
// lower bound.
var statWindowKeys = []string{"24h", "7d", "30d", "all"}

func normalizeStatWindow(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "7d", "30d", "all":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "24h"
	}
}

// statSince maps a window key to its start. The zero time means "all".
func statSince(key string, now time.Time) time.Time {
	switch key {
	case "7d":
		return now.AddDate(0, 0, -7)
	case "30d":
		return now.AddDate(0, 0, -30)
	case "all":
		return time.Time{}
	default:
		return now.Add(-24 * time.Hour)
	}
}

func windowLabelKey(key string) string {
	switch key {
	case "7d":
		return "logs.window.7d"
	case "30d":
		return "logs.window.30d"
	case "all":
		return "logs.window.all"
	default:
		return "logs.window.24h"
	}
}

// statWindows builds the option list used by both tabs. It keeps the current
// tab and the errors filter in the links.
func (s *Server) statWindows(r *http.Request, tab, selected string, onlyErrors bool) ([]logsWindowOption, string) {
	options := make([]logsWindowOption, 0, len(statWindowKeys))
	selectedLabel := ""
	for _, key := range statWindowKeys {
		values := url.Values{}
		values.Set("tab", tab)
		values.Set("window", key)
		if onlyErrors {
			values.Set("status", "errors")
		}
		label := s.translate(r, windowLabelKey(key))
		if key == selected {
			selectedLabel = label
		}
		options = append(options, logsWindowOption{
			Key:    key,
			Label:  label,
			URL:    "/logs?" + values.Encode(),
			Active: key == selected,
		})
	}
	return options, selectedLabel
}

// successRate formats the share of requests that answered without error.
func successRate(totals store.AppSearchTotals) string {
	if totals.Requests == 0 {
		return "—"
	}
	ok := totals.Requests - totals.Errors
	return fmt.Sprintf("%.1f%%", float64(ok)*100/float64(totals.Requests))
}

// humanMillis renders a duration stored in milliseconds.
func humanMillis(ms int64) string {
	switch {
	case ms <= 0:
		return "—"
	case ms < 1000:
		return strconv.FormatInt(ms, 10) + " ms"
	default:
		return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + " s"
	}
}

func (s *Server) logsSection(w http.ResponseWriter, r *http.Request) {
	tab := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tab")))
	switch tab {
	case "requests", "stats":
	default:
		tab = "internal"
	}
	data := logsSectionPage{
		base: s.newBaseT(r, "logs.title", "logs"),
		Tab:  tab,
	}
	data.TotalRequests, _ = s.store.CountAppSearches(r.Context())
	data.ErrorRequests, _ = s.store.CountAppSearchesFiltered(r.Context(), true)
	data.LogLines = s.logs.Len()

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
	default:
		logs := s.logsDataFor(r, "/logs", "internal")
		logs.base = data.base
		data.Internal = logs
		data.AutoRefresh = logs.AutoRefresh
	}
	s.render(w, r, "logs_section", data, http.StatusOK)
}

func (s *Server) logsRequests(r *http.Request) (*logsRequestsView, error) {
	window := normalizeStatWindow(r.URL.Query().Get("window"))
	onlyErrors := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("status")), "errors")
	page := intParam(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * logsRequestPageSize

	total, err := s.store.CountAppSearchesFiltered(r.Context(), onlyErrors)
	if err != nil {
		return nil, err
	}
	searches, err := s.store.ListAppSearchesFiltered(r.Context(), onlyErrors, logsRequestPageSize, offset)
	if err != nil {
		return nil, err
	}
	since := statSince(window, time.Now())
	totals, err := s.store.AppSearchTotals(r.Context(), since)
	if err != nil {
		return nil, err
	}
	windows, selectedLabel := s.statWindows(r, "requests", window, onlyErrors)

	view := &logsRequestsView{
		Pager:       newPager(r, "/logs", total, logsRequestPageSize, offset),
		Window:      window,
		WindowLabel: selectedLabel,
		Windows:     windows,
		Totals:      totals,
		SuccessRate: successRate(totals),
		AvgLatency:  humanMillis(totals.AvgMS),
		OnlyErrors:  onlyErrors,
		Empty: uiEmpty{
			Title: s.translate(r, "logs.requests.emptyTitle"),
			Text:  s.translate(r, "logs.requests.emptyText"),
		},
	}
	reset := url.Values{}
	reset.Set("tab", "requests")
	view.ResetURL = "/logs?" + reset.Encode()
	for _, search := range searches {
		view.Searches = append(view.Searches, s.appSearchView(search))
	}
	return view, nil
}

func (s *Server) logsStats(r *http.Request) (*logsStatsView, error) {
	window := normalizeStatWindow(r.URL.Query().Get("window"))
	since := statSince(window, time.Now())

	totals, err := s.store.AppSearchTotals(r.Context(), since)
	if err != nil {
		return nil, err
	}
	apps, err := s.store.AppSearchStatsByApp(r.Context(), since)
	if err != nil {
		return nil, err
	}
	indexers, err := s.store.AppSearchStatsByIndexer(r.Context(), since, logsIndexerTop)
	if err != nil {
		return nil, err
	}
	windows, selectedLabel := s.statWindows(r, "stats", window, false)
	sources, sourcesTotal := s.usefulSources(r, logsUsefulDays)

	appRows := make([]logsAppRow, 0, len(apps))
	for _, app := range apps {
		appRows = append(appRows, logsAppRow{
			App:      app.App,
			Requests: app.Requests,
			OK:       app.Requests - app.Errors,
			Errors:   app.Errors,
			Results:  app.Results,
			Avg:      humanMillis(app.AvgMS),
		})
	}
	indexerRows := make([]logsIndexerRow, 0, len(indexers))
	for _, item := range indexers {
		indexerRows = append(indexerRows, logsIndexerRow{
			Name:     item.Name,
			Requests: item.Requests,
			Failures: item.Failures,
			Results:  item.Results,
			Avg:      humanMillis(item.AvgMS),
		})
	}
	return &logsStatsView{
		Window:        window,
		WindowLabel:   selectedLabel,
		Windows:       windows,
		Totals:        totals,
		SuccessRate:   successRate(totals),
		AvgLatency:    humanMillis(totals.AvgMS),
		Apps:          appRows,
		Indexers:      indexerRows,
		IndexerCap:    logsIndexerTop,
		UsefulSources: sources,
		UsefulTotal:   sourcesTotal,
		UsefulDays:    logsUsefulDays,
	}, nil
}
