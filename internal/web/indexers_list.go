package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/store"
)

// ------------------------------------------------------------------ UI models

// statusDot is the shared status picture of an indexer, an app or a client.
// State is one of ok, problem, disabled and testing (https://github.com/buzzqw/indexarr/wiki/UI-Rules §4).
type statusDot struct {
	State string
	Label string
}

// uiProblem is the one-sentence cause plus the action next to it. Most actions
// are links; a few change one setting in place, so they are POSTed with the
// page CSRF token instead of navigating away from the list.
type uiProblem struct {
	Text         string
	ActionLabel  string
	ActionURL    string
	ActionPost   string
	ActionReturn string
	ActionTitle  string
	CSRF         string
}

// uiEmpty is the empty state used by the list and the site chooser.
type uiEmpty struct {
	Title       string
	Text        string
	ActionLabel string
	ActionURL   string
}

// uiNextStep closes a flow with the thing the user most likely needs next.
type uiNextStep struct {
	Text        string
	ActionLabel string
	ActionURL   string
}

// uiBadge is one access badge ("Public", "Via Prowlarr", "Feed", …).
type uiBadge struct {
	Kind  string
	Label string
}

// advancedView feeds the "advanced-open" component. The block is closed by
// default and opens only when it contains a validation problem.
type advancedView struct {
	Title string
	Open  bool
}

func advancedSection(title string) advancedView { return advancedView{Title: title} }

// categoryChoice is one checkbox of the category picker. The value is always a
// standard Torznab id; the label is the localized name, never the number.
type categoryChoice struct {
	ID      string
	Label   string
	Checked bool
}

type indexerRowView struct {
	Indexer      store.Indexer
	Status       statusDot
	Badges       []uiBadge
	Filters      int
	FiltersLabel string
	LastUpdate   time.Time
	Problem      *uiProblem
	// FrequencyLabel is how often the scheduler runs this source ("ogni 1 h").
	// FrequencySource says whether it comes from the source or the global
	// default, so the list never hides which setting is in force.
	FrequencyLabel  string
	FrequencySource string
	// SearchOnly means the definition has no periodic latest run. The list
	// must say that instead of "never updated".
	SearchOnly bool
	FeedOnly   bool
	// Channels groups the latest/search rows of one site in a single row.
	Channels []indexerChannelView
}

type indexerChannelView struct {
	ID     int64
	Name   string
	Label  string
	State  string
	Active bool
}

type indexersListView struct {
	base
	Rows         []indexerRowView
	Total        int
	Problems     int
	Disabled     int
	Usenet       int
	Query        string
	State        string
	Protocol     string
	Unavailable  bool
	Empty        uiEmpty
	Bulk         bool
	NoPublicOnly bool
	// ListReturn is the current list URL and DetailQuery carries it into the
	// detail page, so an action started there comes back to the same filter.
	ListReturn  string
	DetailQuery string
}

// ListURL builds one filter link while keeping the other filters. The Usenet
// filter is the only protocol filter offered here: it answers the question
// "where do my NZB sources go?" without turning the list into a manager.
func (v indexersListView) ListURL(state, protocol string) string {
	return indexerListPath(state, v.Query, protocol)
}

func (s *Server) indexersUnifiedPage(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("tab")), "legacy") {
		http.Redirect(w, r, "/indexers", http.StatusFound)
		return
	}
	protocol := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("protocol")))
	if protocol != "usenet" {
		protocol = ""
	}
	data := indexersListView{
		base:     s.newBaseT(r, "idx.title", "indexers"),
		Query:    strings.TrimSpace(r.URL.Query().Get("q")),
		State:    strings.ToLower(strings.TrimSpace(r.URL.Query().Get("state"))),
		Protocol: protocol,
	}
	data.Empty = uiEmpty{
		Title:       s.translate(r, "idx.emptyTitle"),
		Text:        s.translate(r, "idx.emptyText"),
		ActionLabel: s.translate(r, "idx.add"),
		ActionURL:   "/indexers/new",
	}
	data.ListReturn = indexerListPath(data.State, data.Query, data.Protocol)
	if data.ListReturn != "/indexers" {
		data.DetailQuery = "?from=" + url.QueryEscape(data.ListReturn)
	}
	if s.svc == nil {
		data.Unavailable = true
		s.render(w, r, "indexers", data, http.StatusOK)
		return
	}
	indexers, err := s.svc.Indexers(r.Context())
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	defs := s.indexerDefinitions(r.Context(), indexers)
	batch := s.loadHealthBatch(r.Context(), indexers)
	apps, _ := s.store.ListApplications(r.Context())
	_ = apps
	settings, settingsErr := s.store.LoadSettings(r.Context())
	if settingsErr != nil {
		settings = store.DefaultSettings()
	}
	globalInterval := settings.DefaultInterval()

	for _, in := range indexers {
		view := s.indexerRowView(r, in, defs, globalInterval, batch)
		view.setProblemReturn("/indexers")
		data.Total++
		attention := view.Status.State == "problem" || view.Status.State == "degraded" || view.Status.State == "blocked"
		if attention {
			data.Problems++
		}
		if view.Status.State == "disabled" {
			data.Disabled++
		}
		if strings.EqualFold(strings.TrimSpace(in.Protocol), "usenet") {
			data.Usenet++
		}
		if data.Protocol != "" && !strings.EqualFold(strings.TrimSpace(in.Protocol), data.Protocol) {
			continue
		}
		if data.State == "problem" && !attention {
			continue
		}
		if data.State == "disabled" && view.Status.State != "disabled" {
			continue
		}
		if data.State == "ok" && view.Status.State != "ok" {
			continue
		}
		if data.Query != "" && !indexerRowMatches(view, data.Query, defs[in.DefinitionID]) {
			continue
		}
		data.Rows = append(data.Rows, view)
	}
	data.Rows = groupIndexerRows(data.Rows)
	if data.Total > 0 && len(data.Rows) == 0 {
		filterLabel := data.Query
		if filterLabel == "" && data.Protocol != "" {
			filterLabel = s.protocolLabel(r, data.Protocol)
		}
		data.Empty = uiEmpty{
			Title:       s.translate(r, "idx.noMatchTitle"),
			Text:        s.translate(r, "idx.noMatchText", filterLabel),
			ActionLabel: s.translate(r, "idx.clearFilters"),
			ActionURL:   "/indexers",
		}
	}
	s.render(w, r, "indexers", data, http.StatusOK)
}

func indexerRowMatches(row indexerRowView, query string, def *definition.Definition) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return true
	}
	if strings.Contains(strings.ToLower(row.Indexer.Name), q) {
		return true
	}
	if strings.Contains(strings.ToLower(row.Indexer.DefinitionID), q) {
		return true
	}
	for _, badge := range row.Badges {
		if strings.Contains(strings.ToLower(badge.Label), q) {
			return true
		}
	}
	if def != nil {
		for _, raw := range def.Meta.URLs {
			if strings.Contains(strings.ToLower(raw), q) {
				return true
			}
		}
	}
	return false
}

// setProblemReturn records the page a one-click problem action comes back to.
// The handler accepts only the list or this source's detail page.
func (v *indexerRowView) setProblemReturn(path string) {
	if v.Problem != nil {
		v.Problem.ActionReturn = path
	}
}

// frequencyLabels renders the effective frequency of one source and where it
// comes from. A zero interval means "use the global default", which is shown
// explicitly so the list never hides the setting in force.
func (s *Server) frequencyLabels(r *http.Request, own int, global time.Duration) (string, string) {
	seconds := own
	source := s.translate(r, "idx.frequencyOwn")
	if seconds <= 0 {
		seconds = int(global / time.Second)
		if seconds <= 0 {
			seconds = 3600
		}
		source = s.translate(r, "idx.frequencyGlobal")
	}
	return intervalEveryLabel(requestLanguage(r), seconds), source
}

// intervalPresets are the quick choices of the per-source frequency. They are
// a shortcut only: the number field next to the list stays the value that is
// submitted, so the form keeps working without JavaScript.
var intervalPresets = []int{0, 900, 1800, 3600, 7200, 10800, 21600, 43200, 86400}

func intervalChoices(lang i18n.Language, current int) []indexerSettingOption {
	out := make([]indexerSettingOption, 0, len(intervalPresets)+1)
	found := false
	for _, seconds := range intervalPresets {
		selected := seconds == current
		if selected {
			found = true
		}
		out = append(out, indexerSettingOption{
			Value:    strconv.Itoa(seconds),
			Label:    intervalEveryLabel(lang, seconds),
			Selected: selected,
		})
	}
	out = append(out, indexerSettingOption{
		Value:    "custom",
		Label:    i18n.T(lang, "indexerForm.intervalCustom"),
		Selected: !found,
	})
	return out
}

// intervalEveryLabel is the localized "every N minutes/hours/days" used by the
// list, the detail page and the one-click confirmation.
func intervalEveryLabel(lang i18n.Language, seconds int) string {
	switch {
	case seconds <= 0:
		return i18n.T(lang, "indexerForm.intervalGlobal")
	case seconds >= 86400 && seconds%86400 == 0:
		return i18n.T(lang, "time.everyDays", seconds/86400)
	case seconds >= 3600 && seconds%3600 == 0:
		return i18n.T(lang, "time.everyHours", seconds/3600)
	case seconds >= 60 && seconds%60 == 0:
		return i18n.T(lang, "time.everyMinutes", seconds/60)
	default:
		return i18n.T(lang, "time.everySeconds", seconds)
	}
}

func (s *Server) indexerRowView(r *http.Request, in store.Indexer, defs map[string]*definition.Definition, globalInterval time.Duration, batch *healthBatch) indexerRowView {
	def := defs[in.DefinitionID]
	filters := listFilterValues(in)
	row := indexerRowView{
		Indexer: in,
		Badges:  s.indexerBadges(r, in, def),
		Filters: len(filters),
	}
	row.FrequencyLabel, row.FrequencySource = s.frequencyLabels(r, in.IntervalSeconds, globalInterval)
	if row.Filters == 1 {
		row.FiltersLabel = s.translate(r, "idx.filtersOne")
		if detail := filterSummary(filters); detail != "" {
			row.FiltersLabel += ": " + detail
		}
	} else if row.Filters > 1 {
		row.FiltersLabel = s.translate(r, "idx.filters", row.Filters)
		if detail := filterSummary(filters); detail != "" {
			row.FiltersLabel += ": " + detail
		}
	}
	state := s.loadIndexerHealth(r.Context(), in, def, batch)
	row.SearchOnly = !state.Latest
	row.FeedOnly = state.FeedOnly
	if state.LastRun != nil {
		row.LastUpdate = state.LastRun.StartedAt
	}
	row.Status = s.statusDotFor(r, state)
	if row.SearchOnly && row.Status.State == "testing" {
		row.Status = statusDot{State: "ok", Label: s.translate(r, "idx.statusSearchOnly")}
	}
	if row.Status.State == "problem" || row.Status.State == "degraded" || row.Status.State == "blocked" {
		row.Problem = s.healthProblem(r, state)
		if row.Problem == nil {
			row.Problem = s.problemFor(r, in, state.State, state.LastRun)
		}
	}
	return row
}

// groupIndexerRows keeps the latest/search channels of one site on a single
// row. The row links to the primary indexer and expands the other channel.
func groupIndexerRows(rows []indexerRowView) []indexerRowView {
	groups := make(map[string][]indexerRowView)
	order := make([]string, 0, len(rows))
	for _, row := range rows {
		key := ""
		if config := publicConfigMap(row.Indexer.ConfigJSON); config != nil {
			key = strings.TrimSpace(config["site"])
		}
		if key == "" {
			key = "indexer:" + strconv.FormatInt(row.Indexer.ID, 10)
		}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], row)
	}
	out := make([]indexerRowView, 0, len(order))
	for _, key := range order {
		group := groups[key]
		primary := group[0]
		if len(group) > 1 {
			primary.Channels = make([]indexerChannelView, 0, len(group))
			for _, channel := range group {
				label := ""
				if config := publicConfigMap(channel.Indexer.ConfigJSON); config != nil {
					switch strings.ToLower(strings.TrimSpace(config["channel"])) {
					case "latest":
						label = "latest"
					case "search":
						label = "search"
					}
				}
				if label == "" {
					label = channel.Status.Label
				}
				if label == "latest" {
					primary.SearchOnly = false
					if primary.LastUpdate.IsZero() && !channel.LastUpdate.IsZero() {
						primary.LastUpdate = channel.LastUpdate
					}
				}
				primary.Channels = append(primary.Channels, indexerChannelView{
					ID: channel.Indexer.ID, Name: channel.Indexer.Name,
					Label: label, State: channel.Status.State, Active: channel.Indexer.Enabled,
				})
			}
		}
		out = append(out, primary)
	}
	return out
}

// problemFor turns a known error into one sentence plus an action. The raw
// error stays in the diagnostics block of the detail page.
func isChallengeText(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "cloudflare") || strings.Contains(lower, "challenge") ||
		strings.Contains(lower, "flaresolverr")
}

func (s *Server) problemFor(r *http.Request, in store.Indexer, state store.IndexerState, lastRun *store.IndexerHistory) *uiProblem {
	raw := strings.ToLower(strings.TrimSpace(state.LastError))
	if raw == "" && lastRun != nil {
		raw = strings.ToLower(strings.TrimSpace(lastRun.Error))
	}
	detail := "/indexers/" + strconv.FormatInt(in.ID, 10)
	switch {
	case strings.Contains(raw, "flaresolverr") && (strings.Contains(raw, "unreachable") ||
		strings.Contains(raw, "connection refused") || strings.Contains(raw, "no such host") ||
		strings.Contains(raw, "timeout") || strings.Contains(raw, "deadline") ||
		strings.Contains(raw, "solver failed")):
		return &uiProblem{
			Text:        s.translate(r, "idx.problem.blockedSolver"),
			ActionLabel: s.translate(r, "idx.problem.blockedAction"),
			ActionURL:   "/settings?section=network",
		}
	case isChallengeText(raw):
		return &uiProblem{
			Text:        s.translate(r, "idx.problem.blocked"),
			ActionLabel: s.translate(r, "idx.problem.blockedAction"),
			ActionURL:   "/settings?section=network",
		}
	case strings.Contains(raw, "429") || strings.Contains(raw, "rate limit") || strings.Contains(raw, "too many"):
		return &uiProblem{
			Text:        s.translate(r, "idx.problem.rate"),
			ActionLabel: s.translate(r, "idx.problem.rateAction"),
			ActionPost:  detail + "/frequency",
			ActionTitle: s.translate(r, "idx.problem.rateTip"),
			CSRF:        csrfToken(r.Context()),
		}
	case strings.Contains(raw, "403") || strings.Contains(raw, "forbidden") || strings.Contains(raw, "cloudflare"):
		return &uiProblem{
			Text:        s.translate(r, "idx.problem.forbidden"),
			ActionLabel: s.translate(r, "idx.problem.forbiddenAction"),
			ActionURL:   "/settings",
		}
	case strings.Contains(raw, "401") || strings.Contains(raw, "unauthorized") ||
		strings.Contains(raw, "login") || strings.Contains(raw, "credential") || strings.Contains(raw, "password"):
		return &uiProblem{
			Text:        s.translate(r, "idx.problem.auth"),
			ActionLabel: s.translate(r, "idx.problem.authAction"),
			ActionURL:   detail + "/edit",
		}
	case strings.Contains(raw, "timeout") || strings.Contains(raw, "deadline") ||
		strings.Contains(raw, "no such host") || strings.Contains(raw, "connection refused") ||
		strings.Contains(raw, "unreachable") || strings.Contains(raw, "dial"):
		return &uiProblem{
			Text:        s.translate(r, "idx.problem.unreachable"),
			ActionLabel: s.translate(r, "idx.problem.unreachableAction"),
			ActionURL:   detail,
		}
	default:
		return &uiProblem{
			Text:        s.translate(r, "idx.problem.generic"),
			ActionLabel: s.translate(r, "idx.problem.genericAction"),
			ActionURL:   detail,
		}
	}
}

func (s *Server) indexerBadges(r *http.Request, in store.Indexer, def *definition.Definition) []uiBadge {
	var out []uiBadge
	if entry, ok := catalog.Get(in.DefinitionID); ok && !isUsableSiteState(entry.State) {
		out = append(out, uiBadge{Kind: "experimental", Label: s.translate(r, "idx.badgeExperimental")})
	}
	connector := strings.ToLower(strings.TrimSpace(in.DefinitionID))
	if def != nil {
		if strings.TrimSpace(def.Connector) != "" {
			connector = strings.ToLower(strings.TrimSpace(def.Connector))
		}
		switch strings.ToLower(strings.TrimSpace(def.Meta.Type)) {
		case "private":
			out = append(out, uiBadge{Kind: "access", Label: s.translate(r, "idx.badgePrivate")})
		default:
			out = append(out, uiBadge{Kind: "access", Label: s.translate(r, "idx.badgePublic")})
		}
		if strings.EqualFold(strings.TrimSpace(def.Meta.Mirror), "unofficial") {
			out = append(out, uiBadge{Kind: "mirror", Label: s.translate(r, "idx.badgeMirror")})
		}
		if def.Meta.InsecureTLS {
			out = append(out, uiBadge{Kind: "tls", Label: s.translate(r, "idx.badgeInsecureTLS")})
		}
	}
	rawURL := ""
	if config := publicConfigMap(in.ConfigJSON); config != nil {
		rawURL = config["url"]
		switch strings.ToLower(strings.TrimSpace(config["channel"])) {
		case "latest":
			out = append(out, uiBadge{Kind: "channel", Label: s.translate(r, "idx.channelLatest")})
		case "search":
			out = append(out, uiBadge{Kind: "channel", Label: s.translate(r, "idx.channelSearch")})
		}
	}
	lower := strings.ToLower(rawURL)
	switch {
	case connector == "rss":
		out = append(out, uiBadge{Kind: "feed", Label: s.translate(r, "idx.badgeFeed")})
	case connector == "torznab" || in.DefinitionID == "torznab-generic":
		switch {
		case strings.Contains(lower, "jackett") || strings.Contains(lower, "/api/v2.0/indexers/") || urlHasPort(lower, "9117"):
			out = append(out, uiBadge{Kind: "manager", Label: s.translate(r, "idx.badgeJackett")})
		case strings.Contains(lower, "prowlarr") || urlHasPort(lower, "9696") || looksProwlarrEndpoint(lower):
			out = append(out, uiBadge{Kind: "manager", Label: s.translate(r, "idx.badgeProwlarr")})
		default:
			out = append(out, uiBadge{Kind: "manager", Label: s.translate(r, "idx.badgeTorznab")})
		}
	case connector == "newznab":
		out = append(out, uiBadge{Kind: "manager", Label: s.translate(r, "idx.badgeNewznab")})
	}
	if strings.EqualFold(strings.TrimSpace(in.Protocol), "usenet") {
		out = append(out, uiBadge{Kind: "protocol", Label: s.translate(r, "idx.protocolUsenet")})
	}
	return out
}

func urlHasPort(raw, port string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return parsed.Port() == port
}

func looksProwlarrEndpoint(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		if _, err := strconv.Atoi(parts[i]); err == nil && parts[i+1] == "api" {
			return true
		}
	}
	return false
}

func filterSummary(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if strings.TrimSpace(value) != "" {
			if key == "filter" {
				keys = append(keys, value)
			} else {
				keys = append(keys, key+"="+value)
			}
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// indexerFilterValues lists the user-configured filter values of an indexer.
func indexerFilterValues(in store.Indexer) map[string]string {
	out := map[string]string{}
	config := publicConfigMap(in.ConfigJSON)
	for key, value := range config {
		switch key {
		case "url", "categories", "query", "user_agent", "allow_private", "default_category", "default-category",
			"challenge", "flaresolverr_url", "flaresolverr_timeout", "site", "channel", "latest",
			"username", "password", "apikey", "api_key", "passkey", "cookie":
			continue
		}
		if strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	return out
}

// listFilterValues is indexerFilterValues without the mode switches. The list
// row already shows the mode as a badge (Feed, search channel, search only),
// so a chip reading "store_latest=false" or "smartfeed=true" repeated it with
// an internal key name. The detail page keeps every value, with its label.
func listFilterValues(in store.Indexer) map[string]string {
	out := indexerFilterValues(in)
	delete(out, "store_latest")
	delete(out, "smartfeed")
	return out
}

// indexerListPath rebuilds the source list URL from the three supported
// filters, so a return path never carries anything else.
func indexerListPath(state, query, protocol string) string {
	values := url.Values{}
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "problem", "disabled", "ok":
		values.Set("state", strings.ToLower(strings.TrimSpace(state)))
	}
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		values.Set("q", trimmed)
	}
	if strings.EqualFold(strings.TrimSpace(protocol), "usenet") {
		values.Set("protocol", "usenet")
	}
	if encoded := values.Encode(); encoded != "" {
		return "/indexers?" + encoded
	}
	return "/indexers"
}

// sanitizeIndexerReturn accepts only the source list with its two filters.
// Anything else falls back to the plain list, so the form cannot redirect
// somewhere else.
func sanitizeIndexerReturn(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Path != "/indexers" {
		return ""
	}
	return indexerListPath(parsed.Query().Get("state"), parsed.Query().Get("q"), parsed.Query().Get("protocol"))
}

// frequencyReturnPath limits the post-action redirect to the two pages that
// can show the one-click frequency action. Anything else falls back to the
// source detail page, so a crafted form cannot turn the handler into an open
// redirect.
func frequencyReturnPath(raw string, id int64) string {
	detail := "/indexers/" + strconv.FormatInt(id, 10)
	switch strings.TrimSpace(raw) {
	case "/indexers", detail:
		return strings.TrimSpace(raw)
	default:
		return detail
	}
}
