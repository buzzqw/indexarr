package web

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/store"
)

// logsSectionPage is the Log section: the process log only. The requests,
// counters and downloads live in the Statistics section (/activity).
type logsSectionPage struct {
	base
	Internal logsPage
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
	Name      string
	Requests  int64
	InTime    int64
	Late      int64
	Skipped   int64
	Failures  int64
	Results   int64
	Avg       string
	Downloads int64
}

// logsDownloadsView is the "Download" tab: what the apps actually grabbed,
// per source and one row per grab.
type logsDownloadsView struct {
	Window        string
	WindowLabel   string
	Windows       []logsWindowOption
	SourceID      int64
	SourceOptions []logsSourceOption
	Total         int64
	Torrents      int64
	Magnets       int64
	NZBs          int64
	Ed2k          int64
	HasEd2k       bool
	Sources       []logsGrabSourceRow
	Grabs         []logsGrabRow
	Pager         pager
	ProxyMagnets  bool
}

// logsSourceOption is one entry of the source filter.
type logsSourceOption struct {
	ID     int64
	Name   string
	Active bool
}

type logsGrabSourceRow struct {
	Name     string
	Total    int64
	Torrents int64
	Magnets  int64
	NZBs     int64
	Ed2k     int64
	Share    int
}

type logsGrabRow struct {
	At        time.Time
	App       string
	Title     string
	Source    string
	Via       string
	Copy      string
	CopyTitle string
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
		values := r.URL.Query()
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
			URL:    "/activity?" + values.Encode(),
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
	switch tab := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tab"))); tab {
	case "requests", "stats", "downloads":
		// These tabs moved to Statistics: keep old bookmarks working.
		redirectTab(w, r, "/activity", tab)
		return
	}
	data := logsSectionPage{
		base: s.newBaseT(r, "logs.title", "logs"),
	}
	logs := s.logsDataFor(r, "/logs", "")
	// Keep the page's own base, but not at the cost of the refresh the
	// filters asked for.
	data.AutoRefresh = logs.AutoRefresh
	logs.base = data.base
	data.Internal = logs
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
		Pager:       newPager(r, "/activity", total, logsRequestPageSize, offset),
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
	view.ResetURL = "/activity?" + reset.Encode()
	for _, search := range searches {
		view.Searches = append(view.Searches, s.appSearchView(requestLanguage(r), search))
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
	grabs, err := s.store.GrabStatsByIndexer(r.Context(), since)
	if err != nil {
		return nil, err
	}
	grabsByName := make(map[string]int64, len(grabs))
	for _, grab := range grabs {
		grabsByName[grab.Name] = grab.Total()
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
		inTime := item.Requests - item.Late - item.Skipped - item.Failures
		if inTime < 0 {
			inTime = 0
		}
		indexerRows = append(indexerRows, logsIndexerRow{
			Name:      item.Name,
			Requests:  item.Requests,
			InTime:    inTime,
			Late:      item.Late,
			Skipped:   item.Skipped,
			Failures:  item.Failures,
			Results:   item.Results,
			Avg:       humanMillis(item.AvgMS),
			Downloads: grabsByName[item.Name],
		})
	}
	// A feed-only source never answers a live search but can still feed many
	// downloads: list it too, it is exactly the one worth keeping.
	listed := make(map[string]bool, len(indexerRows))
	for _, row := range indexerRows {
		listed[row.Name] = true
	}
	for _, grab := range grabs {
		if !listed[grab.Name] {
			indexerRows = append(indexerRows, logsIndexerRow{Name: grab.Name, Avg: "—", Downloads: grab.Total()})
		}
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

const logsGrabPageSize = 50

func (s *Server) logsDownloads(r *http.Request) (*logsDownloadsView, error) {
	window := normalizeStatWindow(r.URL.Query().Get("window"))
	since := statSince(window, time.Now())
	page := intParam(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * logsGrabPageSize
	sourceID := int64Param(r.URL.Query().Get("source"), 0)

	allStats, err := s.store.GrabStatsByIndexer(r.Context(), since)
	if err != nil {
		return nil, err
	}
	total, err := s.store.CountGrabs(r.Context(), since, sourceID)
	if err != nil {
		return nil, err
	}
	grabs, err := s.store.ListGrabs(r.Context(), since, sourceID, logsGrabPageSize, offset)
	if err != nil {
		return nil, err
	}
	sources, err := s.store.GrabSources(r.Context())
	if err != nil {
		return nil, err
	}
	settings, err := s.store.LoadSettings(r.Context())
	if err != nil {
		return nil, err
	}
	windows, selectedLabel := s.statWindows(r, "downloads", window, false)
	view := &logsDownloadsView{
		Window:       window,
		WindowLabel:  selectedLabel,
		Windows:      windows,
		SourceID:     sourceID,
		Total:        total,
		Pager:        newPager(r, "/activity", total, logsGrabPageSize, offset),
		ProxyMagnets: settings.TorznabProxyMagnets,
	}
	for _, source := range sources {
		view.SourceOptions = append(view.SourceOptions, logsSourceOption{
			ID:     source.ID,
			Name:   source.Name,
			Active: source.ID == sourceID,
		})
	}
	stats := allStats
	if sourceID > 0 {
		stats = make([]store.GrabStat, 0, 1)
		for _, stat := range allStats {
			if stat.IndexerID == sourceID {
				stats = append(stats, stat)
			}
		}
	}
	for _, stat := range stats {
		view.Torrents += stat.Torrents
		view.Magnets += stat.Magnets
		view.NZBs += stat.NZBs
		view.Ed2k += stat.Ed2k
	}
	view.HasEd2k = view.Ed2k > 0
	for _, stat := range stats {
		share := 0
		if total > 0 {
			share = int(stat.Total() * 100 / total)
		}
		view.Sources = append(view.Sources, logsGrabSourceRow{
			Name: stat.Name, Total: stat.Total(), Torrents: stat.Torrents,
			Magnets: stat.Magnets, NZBs: stat.NZBs, Ed2k: stat.Ed2k, Share: share,
		})
	}
	lang := requestLanguage(r)
	base := s.cookieImportBaseURL(r)
	for _, grab := range grabs {
		row := logsGrabRow{
			At:     grab.At,
			App:    grab.App,
			Title:  grab.Title,
			Source: grab.IndexerName,
			Via:    grabViaLabel(lang, grab.Via),
		}
		row.Copy, row.CopyTitle = grabCopyLink(lang, grab, base)
		view.Grabs = append(view.Grabs, row)
	}
	return view, nil
}

// grabCopyLink is the value the "delivered as" chip writes to the clipboard:
// the magnet, the eD2k link or the indexarr .torrent link of the release. An
// empty value means the link was never stored (old rows, or a listing that
// carried only a sealed reference) and the chip stays a plain label.
func grabCopyLink(lang i18n.Language, grab store.Grab, base string) (string, string) {
	release := store.Release{
		ID:       grab.ReleaseID,
		Title:    grab.Title,
		Magnet:   grab.Magnet,
		InfoHash: grab.InfoHash,
		Ed2k:     grab.Ed2k,
	}
	switch grab.Via {
	case indexer.GrabViaMagnet:
		if magnet := magnetFor(release); magnet != "" {
			return magnet, i18n.T(lang, "logs.dl.copyMagnetTip")
		}
	case indexer.GrabViaEd2k:
		if link := strings.TrimSpace(grab.Ed2k); link != "" {
			return link, i18n.T(lang, "logs.dl.copyEd2kTip")
		}
	case indexer.GrabViaTorrent:
		if grab.HasTorrent && grab.ReleaseID > 0 && base != "" {
			return base + "/releases/" + strconv.FormatInt(grab.ReleaseID, 10) + "/torrent",
				i18n.T(lang, "logs.dl.copyTorrentTip")
		}
	}
	return "", i18n.T(lang, "logs.dl.noLinkTip")
}

func grabViaLabel(lang i18n.Language, via string) string {
	switch via {
	case "magnet":
		return i18n.T(lang, "logs.dl.viaMagnet")
	case "nzb":
		return i18n.T(lang, "logs.dl.viaNZB")
	case "ed2k":
		return i18n.T(lang, "logs.dl.viaEd2k")
	default:
		return i18n.T(lang, "logs.dl.viaTorrent")
	}
}
