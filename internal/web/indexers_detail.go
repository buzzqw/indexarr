package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/health"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/native"
	"github.com/buzzqw/indexarr/internal/smartfeed"
	"github.com/buzzqw/indexarr/internal/store"
)

// --------------------------------------------------------------- detail page

type detailStatView struct {
	Queries    int
	AvgLatency string
	Grabs      int
}

type hostRequestView struct {
	Host        string
	RateLimited uint64
	Forbidden   uint64
	Breaker     uint64
}

// indexerAccessRow is one required credential of a source: whether the vault
// or the public configuration already holds it.
// topicCursorView is one forum of the local topic history on the indexer
// page: how many topics are stored, the next page and when it was read.
type topicCursorView struct {
	Forum     string
	Topics    int64
	NextStart int
	Done      bool
	LastRun   time.Time
}

type indexerAccessRow struct {
	Name       string
	Label      string
	Required   bool
	Configured bool
	UpdatedAt  time.Time
	HasUpdated bool
}

type indexerDetailView struct {
	base
	Tab               string
	Data              *indexerDataView
	Try               *indexerTryView
	Indexer           store.Indexer
	Status            statusDot
	Badges            []uiBadge
	DefinitionID      string
	DefinitionName    string
	DefinitionVersion int
	Hash              string
	HashShort         string
	Origin            string
	Connector         string
	Protocol          string
	ProtocolLabel     string
	LastRun           *store.IndexerHistory
	LastError         string
	NextUpdate        time.Time
	HasNext           bool
	// A failing source follows an escalating retry schedule; RetryPaused
	// means the 96 hour step already failed and the scheduler waits for the
	// operator.
	RetryFailures   int
	RetryPaused     bool
	RetryNext       time.Time
	HasRetryNext    bool
	FrequencyLabel  string
	FrequencySource string
	HasState        bool
	State           store.IndexerState
	// CircuitOpen reports that the live breaker of this source is open and the
	// next request would be refused; the Overview tab then offers "Riprendi
	// ora", the manual reset of the cooldown.
	CircuitOpen         bool
	Stats               detailStatView
	Categories          []string
	FilterRows          []indexerConfigEntry
	Public              []indexerConfigEntry
	SecretKeys          []string
	Releases            []store.Release
	Run                 *indexer.RunStats
	RunItems            []engine.Item
	Test                *engine.TestResult
	Problem             *uiProblem
	Problems            []uiProblem
	SlowProblem         *uiProblem
	CatalogState        string
	Health              health.Assessment
	TestFailure         *store.IndexerHistory
	Latest              bool
	CanImportCookie     bool
	CookieAlternative   bool
	MissingCookie       bool
	SearchOnly          bool
	UploaderOnly        bool
	UploaderMinutes     int
	CategoryMapped      bool
	LiveOnMiss          bool
	Smartfeed           bool
	SmartfeedUILink     string
	SmartfeedRSS        bool
	SmartfeedConvertURL string
	HasNative           bool
	NativeSearchURL     string
	LatencyP50          string
	LatencyP95          string
	HostRequests        []hostRequestView
	ApplicationCount    int
	NextStep            uiNextStep
	// Verdict, the address and the source log answer three questions right
	// on the Overview tab: does it work, what is the address, what happened.
	Verdict     healthVerdict
	SiteAddress string
	// ActiveMirror is the definition address the connector uses now, set only
	// when it moved away from the first one because that one stopped answering.
	ActiveMirror string
	// TopicCount is how many threads and series the local history holds for
	// this indexer; TopicCursors says how far the slow walk has gone.
	TopicCount   int64
	TopicCursors []topicCursorView
	// HasTopicHistory reports that the definition declares a catalog, so the
	// page offers the History tab and a manual page read even before the
	// first page is stored.
	HasTopicHistory bool
	ProblemDetail   string
	ProblemAt       time.Time
	HasProblemAt    bool
	LogRows         []indexerLogRow
	AppLog          []logView
	RunsLink        string
	AppLogLink      string
	AccessFields    []indexerAccessRow
	AccessMethods   []accessMethodView
	AccessURLKey    bool
	MissingAccess   []string
	AccessEditLink  string
	ListReturn      string
}

func (s *Server) indexerUnifiedDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "indexer.error.invalidID"))
		return
	}
	data, err := s.buildIndexerDetail(r, id)
	if err != nil {
		s.renderError(w, r, statusForIndexerError(err), s.indexerErrorMessage(r, err))
		return
	}
	tab := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("tab")))
	switch tab {
	case "data":
		data.Tab = "data"
		data.Data, err = s.buildIndexerData(r, id)
		if err != nil {
			s.renderError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
	case "history":
		data.Tab = "history"
	default:
		data.Tab = "overview"
	}
	s.render(w, r, "indexer", data, http.StatusOK)
}

func (s *Server) buildIndexerDetail(r *http.Request, id int64) (indexerDetailView, error) {
	in, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		return indexerDetailView{}, err
	}
	data := indexerDetailView{
		base:           s.newBase(r, in.Name, "indexers"),
		Tab:            "overview",
		Try:            &indexerTryView{Mode: "search"},
		Indexer:        in,
		DefinitionID:   in.DefinitionID,
		DefinitionName: in.DefinitionID,
		Protocol:       in.Protocol,
		ProtocolLabel:  s.protocolLabel(r, in.Protocol),
		Public:         publicConfig(in.ConfigJSON),
	}
	def, _ := s.parsedDefinition(r.Context(), in.DefinitionID)
	data.Latest = true
	if def != nil {
		data.DefinitionName = def.Meta.Name
		data.Connector = def.Connector
		data.DefinitionVersion = def.Schema
		data.Categories = s.definitionCategoryLabels(r, definitionCategoryIDs(def))
		data.Latest = def.Meta.HasLatest()
		data.CanImportCookie = definitionHasCookie(def)
		data.CookieAlternative = def.CookieAlternative()
	}
	data.SearchOnly = !data.Latest
	config := publicConfigMap(in.ConfigJSON)
	data.LiveOnMiss = truthy(config["live_on_miss"])
	address := strings.TrimSpace(config["url"])
	if address == "" {
		address = strings.TrimSpace(config["site"])
	}
	if address == "" {
		address = strings.TrimSpace(config["base_url"])
	}
	if address == "" && def != nil && len(def.Meta.URLs) > 0 {
		address = def.Meta.URLs[0]
	}
	data.SiteAddress = indexerSiteAddress(address)
	if s.native != nil && def != nil && len(def.Meta.URLs) > 1 && address == def.Meta.URLs[0] {
		if active, ok := s.native.ActiveBaseURL(in.ID); ok && !sameHost(active, address) {
			data.ActiveMirror = indexerSiteAddress(active)
		}
	}
	if indexer.LiveExcluded(in) {
		data.UploaderOnly = true
		interval := time.Hour
		if settings, ok := settingsFromContext(r.Context()); ok {
			interval = settings.DefaultInterval()
		} else if settings, loadErr := s.store.LoadSettings(r.Context()); loadErr == nil {
			interval = settings.DefaultInterval()
		}
		if in.IntervalSeconds > 0 {
			interval = time.Duration(in.IntervalSeconds) * time.Second
		}
		data.UploaderMinutes = int(interval.Minutes())
		if data.UploaderMinutes < 1 {
			data.UploaderMinutes = 1
		}
	} else if strings.EqualFold(in.DefinitionID, "extto") {
		data.CategoryMapped = true
	}
	if strings.EqualFold(strings.TrimSpace(in.DefinitionID), smartfeed.DefinitionID) ||
		truthy(config["smartfeed"]) {
		data.Smartfeed = true
		data.SmartfeedUILink = "https://" + smartfeed.Site + "/smartfeed/ui"
		data.HasNative, data.NativeSearchURL = s.smartfeedNativeChannel(r.Context(), config["site"])
	}
	if selected := splitCategoryList(config["categories"]); len(selected) > 0 {
		labels := make([]string, 0, len(selected))
		for _, raw := range selected {
			id, err := strconv.Atoi(raw)
			if err != nil {
				continue
			}
			labels = append(labels, s.categoryLabel(r, id))
		}
		if len(labels) > 0 {
			data.Categories = labels
		}
	}
	if stored, err := s.store.GetDefinition(r.Context(), in.DefinitionID); err == nil {
		data.Hash = stored.Hash
		data.HashShort = shortHash(stored.Hash)
		data.Origin = stored.Origin
		data.CatalogState = stored.State
	} else if entry, ok := catalog.Get(in.DefinitionID); ok {
		data.Hash = entry.Hash
		data.HashShort = shortHash(entry.Hash)
		data.Origin = catalog.OriginBuiltin
		data.CatalogState = entry.State
	}
	if data.Connector == "" {
		if parsed, ok := s.parsedDefinition(r.Context(), in.DefinitionID); ok {
			data.Connector = parsed.Connector
		}
	}
	data.Badges = s.indexerBadges(r, in, def)
	if def != nil {
		data.HasTopicHistory = def.HasCatalog()
	}
	data.TopicCount, data.TopicCursors = s.topicHistory(r.Context(), in.ID)
	healthView := s.loadIndexerHealth(r.Context(), in, def, nil)
	data.Health = healthView.Assessment
	data.TestFailure = healthView.TestFailure
	data.State = healthView.State
	data.HasState = healthView.HasState
	data.CircuitOpen = healthView.State.Circuit == string(engine.BreakerOpen)
	data.LastRun = healthView.LastRun
	history, _ := s.store.ListIndexerHistory(r.Context(), in.ID, 200)
	now := time.Now()
	for i := range history {
		item := history[i]
		if !item.StartedAt.Before(now.Add(-24 * time.Hour)) {
			switch item.Kind {
			case "search":
				data.Stats.Queries++
			case "grab":
				data.Stats.Grabs++
			}
		}
	}
	data.Status = s.statusDotFor(r, healthView)
	data.LastError = strings.TrimSpace(data.State.LastError)
	if data.LastError == "" && data.LastRun != nil {
		data.LastError = strings.TrimSpace(data.LastRun.Error)
	}
	data.Problems = s.healthProblems(r, healthView)
	if len(data.Problems) > 0 {
		data.Problem = &data.Problems[0]
	} else if data.Status.State == "problem" || data.Status.State == "blocked" || data.Status.State == "degraded" {
		data.Problem = s.problemFor(r, in, data.State, data.LastRun)
	}
	settings, settingsErr := s.store.LoadSettings(r.Context())
	if settingsErr != nil {
		settings = store.DefaultSettings()
	}
	data.FrequencyLabel, data.FrequencySource = s.frequencyLabels(r, in.IntervalSeconds, settings.DefaultInterval())
	detailPath := "/indexers/" + strconv.FormatInt(in.ID, 10)
	for i := range data.Problems {
		data.Problems[i].ActionReturn = detailPath
	}
	if data.Problem != nil {
		data.Problem.ActionReturn = detailPath
	}
	// One verdict, the concrete error and the source log, right on the
	// Overview tab: the operator must not have to open Data or the advanced
	// block to learn why a source is red.
	data.Verdict = s.verdict(r, healthView, data.Status, data.Problems)
	if data.LastError != "" {
		data.ProblemDetail = feed.RedactText(data.LastError, nil)
		if data.HasState && strings.TrimSpace(data.State.LastError) != "" && !data.State.UpdatedAt.IsZero() {
			data.ProblemAt = data.State.UpdatedAt
			data.HasProblemAt = true
		} else if data.LastRun != nil && !data.LastRun.StartedAt.IsZero() {
			data.ProblemAt = data.LastRun.StartedAt
			data.HasProblemAt = true
		}
	}
	data.LogRows = indexerLogRows(history, 8)
	for i := range data.LogRows {
		data.LogRows[i].Problem = s.transientLogMessage(r, data.LogRows[i].Message)
	}
	data.AppLog = logViews(requestLanguage(r), s.indexerAppLog(in, 10), map[string]string{strings.ToLower(in.Slug): in.Name})
	runsValues := url.Values{}
	runsValues.Set("tab", "runs")
	runsValues.Set("indexer", strconv.FormatInt(in.ID, 10))
	data.RunsLink = "/activity?" + runsValues.Encode()
	data.AppLogLink = "/logs"
	if slug := strings.TrimSpace(in.Slug); slug != "" {
		logValues := url.Values{}
		logValues.Set("q", slug)
		data.AppLogLink += "?" + logValues.Encode()
	}
	if latency, found, latencyErr := s.store.IndexerLatency(r.Context(), in.ID, 50); latencyErr == nil && found {
		data.LatencyP50 = formatLatency(latency.P50MS)
		data.LatencyP95 = formatLatency(latency.P95MS)
		timeout := time.Duration(settings.TorznabLiveTimeout) * time.Second
		if timeout <= 0 {
			timeout = 20 * time.Second
		}
		if time.Duration(latency.P95MS)*time.Millisecond > timeout {
			data.SlowProblem = &uiProblem{
				Text:        s.translate(r, "idx.detail.slow", timeout.Seconds()),
				ActionLabel: s.translate(r, "idx.detail.data"),
				ActionURL:   "/indexers/" + strconv.FormatInt(in.ID, 10) + "?tab=data",
			}
		}
	}
	if data.LastRun != nil && data.Latest {
		interval := settings.DefaultInterval()
		if in.IntervalSeconds > 0 {
			interval = time.Duration(in.IntervalSeconds) * time.Second
		}
		if interval <= 0 {
			interval = 30 * time.Minute
		}
		data.NextUpdate = data.LastRun.StartedAt.Add(interval)
		data.HasNext = true
	}
	// A source with repeated failures follows the retry schedule, not the
	// normal interval; after the last step it waits for the operator.
	if states, err := s.store.RunRetryStates(r.Context()); err == nil {
		state := states[in.ID]
		failures := state.Failures
		if failures > 0 && in.UpdatedAt.After(state.LastRun) {
			failures = 0
		}
		if failures > 0 {
			data.RetryFailures = failures
			if native.RetryExhausted(failures) {
				data.RetryPaused = true
			} else if delay, ok := native.RetryDelay(failures); ok {
				data.RetryNext = state.LastRun.Add(delay)
				data.HasRetryNext = true
			}
		}
	}
	data.Stats.AvgLatency = averageLatency(history)
	secretUpdated := map[string]time.Time{}
	if secrets, err := s.store.ListSecrets(r.Context(), in.ID); err == nil {
		for _, secret := range secrets {
			data.SecretKeys = append(data.SecretKeys, secret.Key)
			secretUpdated[strings.ToLower(strings.TrimSpace(secret.Key))] = secret.UpdatedAt
		}
	}
	if data.CanImportCookie {
		hasCookie := false
		for _, key := range data.SecretKeys {
			if strings.EqualFold(strings.TrimSpace(key), "cookie") {
				hasCookie = true
				break
			}
		}
		// On a form login the cookie is only an alternative to the login.
		data.MissingCookie = !hasCookie && !data.CookieAlternative
	}
	// A feed or Torznab URL is sealed when it carries an rsskey or an API key.
	// Show the redacted stored address instead of falling back to the
	// definition's placeholder (example.invalid).
	for _, key := range data.SecretKeys {
		if !strings.EqualFold(key, "url") {
			continue
		}
		if redacted := s.redactedSecretURL(r, in.ID, "url"); redacted != "" {
			data.SiteAddress = redacted
		}
		break
	}
	// A generic RSS source fed with a personal Smartfeed URL cannot grab
	// anything: its items are topic links, not .torrent or magnet links. Offer
	// the conversion to the paired Smartfeed definition.
	if strings.EqualFold(strings.TrimSpace(in.DefinitionID), "rss") {
		if value, ok := s.indexerSecretValue(r.Context(), in.ID, "url"); ok && smartfeed.IsSmartfeedURL(value) {
			data.SmartfeedRSS = true
			data.SmartfeedConvertURL = detailPath + "/smartfeed"
		}
	}
	// Credentials belong on the Overview tab too: the operator must see at
	// once whether the source can log in, and where to type them. For a
	// restricted source an optional field (the imported IPTorrents cookie)
	// is listed without being reported as missing.
	if def != nil {
		restricted := strings.EqualFold(def.Meta.Type, "private") || strings.EqualFold(def.Meta.Type, "semi-private")
		// A pasted session cookie stands for the username and password of
		// a form login: they are not reported missing next to it.
		_, cookieConfigured := secretUpdated["cookie"]
		for _, setting := range def.Settings {
			if !isAccessSetting(setting.Name) || (!setting.Required && !restricted) {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(setting.Name))
			configured := strings.TrimSpace(config[name]) != ""
			row := indexerAccessRow{Name: name, Required: setting.Required}
			if indexersvc.IsSecretKey(name) {
				updated, found := secretUpdated[name]
				configured = found
				row.UpdatedAt = updated
				row.HasUpdated = found && !updated.IsZero()
			}
			label := s.settingLabel(r, setting.Name, setting.Label)
			row.Label = label
			row.Configured = configured
			data.AccessFields = append(data.AccessFields, row)
			if !configured && setting.Required && !(def.CookieCovers(name) && cookieConfigured) {
				data.MissingAccess = append(data.MissingAccess, label)
			}
		}
		sortAccessRows(data.AccessFields)
		data.AccessMethods = s.accessMethods(r, def)
		keys := accessMethodKeys(def)
		data.AccessURLKey = len(keys) == 1 && keys[0] == "urlkey"
	}
	data.AccessEditLink = detailPath + "/edit"
	data.ListReturn = sanitizeIndexerReturn(r.URL.Query().Get("from"))
	if data.ListReturn == "" {
		if in.Enabled {
			data.ListReturn = "/indexers"
		} else {
			data.ListReturn = "/indexers?state=disabled"
		}
	}
	if len(data.MissingAccess) > 0 {
		missing := strings.Join(data.MissingAccess, ", ")
		text := s.translate(r, "idx.access.missingVerdict", missing)
		data.Verdict = healthVerdict{
			Label: s.translate(r, "idx.access.missingLabel"),
			Class: "warn",
			Text:  text,
		}
		data.Problems = append([]uiProblem{{
			Text:         text,
			ActionLabel:  s.translate(r, "idx.access.action"),
			ActionURL:    data.AccessEditLink,
			ActionReturn: detailPath,
		}}, data.Problems...)
	}
	for key, value := range indexerFilterValues(in) {
		data.FilterRows = append(data.FilterRows, indexerConfigEntry{
			Key: s.settingLabel(r, key, key), Value: value,
		})
	}
	sort.Slice(data.FilterRows, func(i, j int) bool { return data.FilterRows[i].Key < data.FilterRows[j].Key })
	if releases, err := s.store.ListIndexerReleases(r.Context(), in.ID, 10); err == nil {
		data.Releases = releases
	}
	if s.polite != nil {
		host := configHost(in.ConfigJSON)
		first := ""
		if def != nil && len(def.Meta.URLs) > 0 {
			first = def.Meta.URLs[0]
		}
		if data.ActiveMirror != "" {
			first = data.ActiveMirror
		}
		if host == "" && first != "" {
			if parsed, err := url.Parse(first); err == nil {
				host = strings.ToLower(parsed.Hostname())
			}
		}
		for _, metric := range s.polite.HostMetrics() {
			if host == "" || !strings.EqualFold(metric.Host, host) {
				continue
			}
			data.HostRequests = append(data.HostRequests, hostRequestView{
				Host: metric.Host, RateLimited: metric.RateLimited,
				Forbidden: metric.Forbidden, Breaker: metric.BreakerOpen,
			})
		}
	}
	if applications, err := s.store.ListApplications(r.Context()); err == nil {
		data.ApplicationCount = len(applications)
	}
	if data.ApplicationCount == 0 {
		data.NextStep = uiNextStep{
			Text:        s.translate(r, "idx.detail.nextConnect"),
			ActionLabel: s.translate(r, "idx.detail.connect"),
			ActionURL:   "/applications/new",
		}
	} else {
		data.NextStep = uiNextStep{
			Text:        s.translate(r, "idx.detail.connected", data.ApplicationCount),
			ActionLabel: s.translate(r, "idx.detail.sync"),
			ActionURL:   "/applications",
		}
	}
	return data, nil
}

func averageLatency(history []store.IndexerHistory) string {
	var total int64
	var count int64
	for _, item := range history {
		if item.Kind != "search" || item.LatencyMS <= 0 {
			continue
		}
		total += item.LatencyMS
		count++
	}
	if count == 0 {
		return "—"
	}
	ms := total / count
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	return fmt.Sprintf("%.1f s", float64(ms)/1000)
}

func formatLatency(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	if ms < 1000 {
		return fmt.Sprintf("%d ms", ms)
	}
	return fmt.Sprintf("%.1f s", float64(ms)/1000)
}

func configHost(raw string) string {
	config := publicConfigMap(raw)
	parsed, err := url.Parse(config["url"])
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// sameHost reports whether two addresses name the same host.
func sameHost(a, b string) bool {
	left, errA := url.Parse(strings.TrimSpace(a))
	right, errB := url.Parse(strings.TrimSpace(b))
	if errA != nil || errB != nil {
		return false
	}
	return strings.EqualFold(left.Hostname(), right.Hostname())
}

// topicHistory reads the local topic history of one indexer. A source without
// a catalog has no rows and the page shows nothing extra.
func (s *Server) topicHistory(ctx context.Context, indexerID int64) (int64, []topicCursorView) {
	count, err := s.store.TopicCount(ctx, indexerID)
	if err != nil || count == 0 {
		return 0, nil
	}
	cursors, err := s.store.TopicCursors(ctx, indexerID)
	if err != nil {
		return count, nil
	}
	counts, err := s.store.TopicCountsByForum(ctx, indexerID)
	if err != nil {
		return count, nil
	}
	forums := make([]string, 0, len(cursors))
	for forum := range cursors {
		forums = append(forums, forum)
	}
	sort.Slice(forums, func(i, j int) bool {
		left, errLeft := strconv.Atoi(forums[i])
		right, errRight := strconv.Atoi(forums[j])
		if errLeft == nil && errRight == nil {
			return left < right
		}
		return forums[i] < forums[j]
	})
	out := make([]topicCursorView, 0, len(forums))
	for _, forum := range forums {
		cursor := cursors[forum]
		out = append(out, topicCursorView{
			Forum:     forum,
			Topics:    counts[forum],
			NextStart: cursor.NextStart,
			Done:      cursor.NextStart == 0,
			LastRun:   cursor.LastRunAt,
		})
	}
	return count, out
}
