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
	"github.com/buzzqw/indexarr/internal/categories"
	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/health"
	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/native"
	"github.com/buzzqw/indexarr/internal/smartfeed"
	"github.com/buzzqw/indexarr/internal/store"
)

// ------------------------------------------------------------------ UI models

// statusDot is the shared status picture of an indexer, an app or a client.
// State is one of ok, problem, disabled and testing (docs/UI-RULES.md §4).
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
	Query        string
	State        string
	Unavailable  bool
	Empty        uiEmpty
	Bulk         bool
	NoPublicOnly bool
	// ListReturn is the current list URL and DetailQuery carries it into the
	// detail page, so an action started there comes back to the same filter.
	ListReturn  string
	DetailQuery string
}

func (s *Server) indexersUnifiedPage(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("tab")), "legacy") {
		http.Redirect(w, r, "/indexers", http.StatusFound)
		return
	}
	data := indexersListView{
		base:  s.newBaseT(r, "idx.title", "indexers"),
		Query: strings.TrimSpace(r.URL.Query().Get("q")),
		State: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("state"))),
	}
	data.Empty = uiEmpty{
		Title:       s.translate(r, "idx.emptyTitle"),
		Text:        s.translate(r, "idx.emptyText"),
		ActionLabel: s.translate(r, "idx.add"),
		ActionURL:   "/indexers/new",
	}
	data.ListReturn = indexerListPath(data.State, data.Query)
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
		data.Empty = uiEmpty{
			Title:       s.translate(r, "idx.noMatchTitle"),
			Text:        s.translate(r, "idx.noMatchText", data.Query),
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

func (s *Server) indexerStatus(r *http.Request, in store.Indexer, state store.IndexerState, hasState bool, lastRun *store.IndexerHistory) statusDot {
	switch {
	case !in.Enabled:
		return statusDot{State: "disabled", Label: s.translate(r, "idx.statusDisabled")}
	case lastRun != nil && lastRun.Status == "blocked":
		return statusDot{State: "blocked", Label: s.translate(r, "idx.statusBlocked")}
	case hasState && isChallengeText(state.LastError):
		// A challenge recorded outside a scheduled run (a live app search,
		// for example) still shows up as blocked, not as a generic problem.
		return statusDot{State: "blocked", Label: s.translate(r, "idx.statusBlocked")}
	case (hasState && (strings.TrimSpace(state.LastError) != "" || strings.EqualFold(state.Circuit, "open"))) ||
		(lastRun != nil && lastRun.Status == "error"):
		return statusDot{State: "problem", Label: s.translate(r, "idx.statusProblem")}
	case lastRun == nil:
		return statusDot{State: "testing", Label: s.translate(r, "idx.statusTesting")}
	default:
		return statusDot{State: "ok", Label: s.translate(r, "idx.statusOk")}
	}
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

// -------------------------------------------------------------- site chooser

type siteFilterChip struct {
	Label  string
	URL    string
	Active bool
}

type siteFilterGroup struct {
	Label string
	Chips []siteFilterChip
}

// siteFilterSelect is one option of the type and language dropdowns.
type siteFilterSelect struct {
	Value    string
	Label    string
	Selected bool
}

type catalogQueryPair struct {
	Name  string
	Value string
}

type siteChoice struct {
	ID            string
	Name          string
	Description   string
	Note          string
	Type          string
	TypeLabel     string
	Language      string
	LanguageBadge string
	Protocol      string
	State         string
	StateLabel    string
	Categories    []string
	AccessLabel   string
	URL           string
	Mirrors       []string
	Italian       bool
	Usage         int
	Latest        bool
	LatestList    bool
	Storable      bool
	Experimental  bool
	QuickAdd      bool
	Configurable  bool
	Probe         bool
	ToWrite       bool
	Reachable     bool
	OptionalID    string
	Added         bool
	AddedID       int64
}

// needChoice is one card of the guided add flow ("Series", "Films", ...).
type needChoice struct {
	Key    string
	Label  string
	Tip    string
	URL    string
	Active bool
}

// guideSite is a recommendation: the ordinary site choice plus the three
// plain-language facts §2 requires (what it needs, what it gives, reliability).
type guideSite struct {
	siteChoice
	Reason           string
	NeedsLabel       string
	ProvidesLabel    string
	ReliabilityLabel string
}

// guideView backs the first screen of "Add source": needs, recommendations
// and the way to the full catalogue. The full list is never removed.
type guideView struct {
	Needs        []needChoice
	ActiveNeed   string
	ActiveLabel  string
	Recommended  []guideSite
	ShowAllURL   string
	BackURL      string
	SearchAllURL string
}

type sitePickerView struct {
	Title           string
	Subtitle        string
	Action          string
	Query           string
	CSRF            string
	Counter         string
	Filtered        string
	Groups          []siteFilterGroup
	TypeFilter      string
	LanguageFilter  string
	TypeOptions     []siteFilterSelect
	LanguageOptions []siteFilterSelect
	Sites           []siteChoice
	Types           []siteChoice
	Pager           pager
	Coverage        string
	QueueLine       string
	HasFilters      bool
	ClearURL        string
	Hidden          []catalogQueryPair
	Empty           uiEmpty
	BridgeURL       string
	FeedURL         string
	TorznabURL      string
	NewznabURL      string
	BrowseLabel     string
	NotFound        bool
	Message         string
	Guide           *guideView
	ShowAll         bool
}

// catalogCategoryOrder is the order the filters and row badges use. It is the
// list in docs/UI-RULES.md §3, not the order of the definition file.
var catalogCategoryOrder = []struct {
	Key      string
	LabelKey string
}{
	{"movies", "category.2000"},
	{"tv", "category.5000"},
	{"music", "category.3000"},
	{"books", "category.7000"},
	{"anime", "idx.catalog.cat.anime"},
	{"games", "category.4000"},
	{"software", "idx.catalog.cat.software"},
	{"xxx", "category.6000"},
}

func (s *Server) indexerCatalogAdd(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		redirect(w, r, "/indexers/new", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	index, err := s.siteCatalog()
	if err != nil {
		redirect(w, r, "/indexers/new", "", err.Error())
		return
	}
	byID := make(map[string]catalog.IndexEntry, len(index))
	for _, entry := range index {
		byID[entry.ID] = entry
	}

	selected := make([]string, 0, len(r.Form["definition_id"]))
	seen := map[string]bool{}
	for _, id := range r.Form["definition_id"] {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		selected = append(selected, id)
	}
	if len(selected) == 0 {
		redirect(w, r, "/indexers/new", "", s.translate(r, "idx.catalog.selectFirst"))
		return
	}

	addedByDef, _ := s.catalogAlreadyAdded(r, index)
	added, already, skipped, invalid := 0, 0, 0, 0
	var lastID int64
	for _, id := range selected {
		entry, ok := byID[id]
		if !ok {
			invalid++
			continue
		}
		if !catalogQuickAdd(entry) {
			skipped++
			continue
		}
		if _, ok := addedByDef[id]; ok {
			already++
			continue
		}
		if err := s.ensureStoredDefinition(r, id); err != nil {
			invalid++
			continue
		}
		enabled := true
		priority := 25
		created, err := s.svc.Create(r.Context(), indexersvc.Input{
			DefinitionID: id,
			Name:         entry.Name,
			Slug:         id,
			Protocol:     entry.Protocol,
			Priority:     &priority,
			Enabled:      &enabled,
		})
		if err != nil {
			skipped++
			continue
		}
		added++
		lastID = created.ID
		addedByDef[id] = created
	}

	if added == 1 && len(selected) == 1 {
		redirect(w, r, "/indexers/"+strconv.FormatInt(lastID, 10), s.translate(r, "idx.detail.added", byID[selected[0]].Name), "")
		return
	}

	switch {
	case added > 0:
		redirect(w, r, "/indexers", s.translate(r, "idx.catalog.addedSome", added, already, skipped+invalid), "")
	case already > 0:
		redirect(w, r, "/indexers/new", s.translate(r, "idx.catalog.allAlready"), "")
	case skipped > 0:
		redirect(w, r, "/indexers/new", "", s.translate(r, "idx.catalog.noneEligible"))
	default:
		redirect(w, r, "/indexers/new", "", s.translate(r, "idx.catalog.invalid"))
	}
}

func (s *Server) indexerUnifiedNew(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		s.renderError(w, r, http.StatusServiceUnavailable, s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	defID := strings.TrimSpace(r.URL.Query().Get("definition"))
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))

	if defID == "" && query != "" && smartfeed.IsSmartfeedURL(query) {
		s.smartfeedRecognized(w, r, query)
		return
	}
	if defID == "" && query != "" {
		if target, ok := s.resolveSiteAddress(query); ok {
			location := "/indexers/new?definition=" + url.QueryEscape(target.DefinitionID)
			if target.URL != "" {
				location += "&url=" + url.QueryEscape(target.URL)
			}
			http.Redirect(w, r, location, http.StatusFound)
			return
		}
	}
	if defID == "" {
		s.renderSiteChooser(w, r, query, "")
		return
	}
	if defID == smartfeed.DefinitionID {
		if rawURL == "" {
			redirect(w, r, "/indexers/new", "", s.translate(r, "idx.smartfeed.pasteURL"))
			return
		}
		s.smartfeedRecognized(w, r, rawURL)
		return
	}
	if defID == "rss" {
		if err := s.ensureRSSDefinition(r); err != nil {
			s.renderError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
	}
	data, err := s.buildCompactIndexerForm(r, defID, nil, nil, nil, rawURL)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	data.base = s.newBaseT(r, "idx.formTitle", "indexers", data.DefinitionName)
	if note := siteAddressNote(r, rawURL); note != "" {
		data.Note = note
	}
	s.render(w, r, "indexer_form", data, http.StatusOK)
}

type resolvedAddress struct {
	DefinitionID string
	URL          string
}

// resolveSiteAddress maps a pasted address to the right add form:
//   - a verified/stabil site whose hosts are in the catalog index;
//   - a Jackett/Prowlarr endpoint (the Torznab bridge);
//   - a Torznab/Newznab endpoint;
//   - an RSS/Atom feed.
//
// It reads the generated index only and never parses the definition YAML.
func (s *Server) resolveSiteAddress(raw string) (resolvedAddress, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return resolvedAddress{}, false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		if retry, retryErr := url.Parse("https://" + raw); retryErr == nil && retry.Hostname() != "" {
			parsed, raw = retry, "https://"+raw
		} else {
			return resolvedAddress{}, false
		}
	}
	// A personal phpBB Smartfeed is not a generic RSS feed: it belongs to
	// MirCrew and becomes its latest channel, never a search indexer.
	if smartfeed.IsSmartfeedURL(raw) {
		return resolvedAddress{DefinitionID: smartfeed.DefinitionID, URL: raw}, true
	}
	host := normalizeSiteHost(parsed.Hostname())
	if index, err := s.siteCatalog(); err == nil {
		for _, entry := range index {
			if !isUsableSiteState(entry.State) || isGenericDefinition(entry.ID) {
				continue
			}
			for _, siteHost := range entry.Hosts {
				if siteHost == host {
					return resolvedAddress{DefinitionID: entry.ID, URL: raw}, true
				}
			}
		}
	}
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "jackett") || strings.Contains(lower, "/api/v2.0/indexers/") || urlHasPort(lower, "9117") ||
		strings.Contains(lower, "prowlarr") || urlHasPort(lower, "9696") || looksProwlarrEndpoint(lower) {
		return resolvedAddress{DefinitionID: "torznab-generic", URL: raw}, true
	}
	if strings.Contains(lower, "/api") || strings.Contains(lower, "torznab") || strings.Contains(lower, "newznab") || strings.Contains(lower, "t=caps") {
		id := "torznab-generic"
		if strings.Contains(lower, "newznab") {
			id = "newznab-generic"
		}
		return resolvedAddress{DefinitionID: id, URL: raw}, true
	}
	suffix := strings.ToLower(parsed.Path)
	if strings.HasSuffix(suffix, ".xml") || strings.HasSuffix(suffix, ".rss") || strings.HasSuffix(suffix, ".atom") ||
		strings.Contains(lower, "rss") || strings.Contains(lower, "feed") || strings.Contains(lower, "atom") {
		return resolvedAddress{DefinitionID: "rss", URL: raw}, true
	}
	return resolvedAddress{}, false
}

func normalizeSiteHost(raw string) string {
	return strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), "."), "www.")
}

func isUsableSiteState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "verificata", "stabile":
		return true
	default:
		return false
	}
}

func siteAddressNote(r *http.Request, raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "jackett") || strings.Contains(lower, "/api/v2.0/indexers/") || urlHasPort(lower, "9117"):
		return i18n.T(requestLanguage(r), "idx.noteJackett")
	case strings.Contains(lower, "prowlarr") || urlHasPort(lower, "9696") || looksProwlarrEndpoint(lower):
		return i18n.T(requestLanguage(r), "idx.noteProwlarr")
	case strings.Contains(lower, "torznab") || strings.Contains(lower, "newznab") || strings.Contains(lower, "/api"):
		return i18n.T(requestLanguage(r), "idx.noteTorznab")
	default:
		return ""
	}
}

func (s *Server) renderSiteChooser(w http.ResponseWriter, r *http.Request, query, message string) {
	view, err := s.sitePickerView(r, query)
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	view.Message = message
	data := struct {
		base
		Choosing bool
		Picker   *sitePickerView
	}{base: s.newBaseT(r, "idx.addTitle", "indexers"), Choosing: true, Picker: view}
	s.render(w, r, "indexer_form", data, http.StatusOK)
}

func (s *Server) siteCatalog() ([]catalog.IndexEntry, error) {
	if s.catalogIndex != nil {
		return s.catalogIndex()
	}
	return catalog.Index()
}

func (s *Server) sitePickerView(r *http.Request, query string) (*sitePickerView, error) {
	index, err := s.siteCatalog()
	if err != nil {
		return nil, err
	}
	q := parseCatalogQuery(r)
	view := &sitePickerView{
		Title:      s.translate(r, "idx.addTitle"),
		Subtitle:   s.translate(r, "idx.addSubtitle"),
		Action:     "/indexers/new",
		Query:      query,
		CSRF:       csrfToken(r.Context()),
		ClearURL:   "/indexers/new",
		BridgeURL:  "/indexers/new?definition=torznab-generic",
		FeedURL:    "/indexers/new?definition=rss",
		TorznabURL: "/indexers/new?definition=torznab-generic",
		NewznabURL: "/indexers/new?definition=newznab-generic",
	}
	view.Empty = uiEmpty{
		Title:       s.translate(r, "idx.noSitesTitle"),
		Text:        s.translate(r, "idx.noSitesText"),
		ActionLabel: s.translate(r, "idx.clearFilters"),
		ActionURL:   "/indexers/new",
	}

	added, _ := s.catalogAlreadyAdded(r, index)
	var sites []catalog.IndexEntry
	var types []catalog.IndexEntry
	for _, entry := range index {
		if entry.ID == "example-html" || entry.State == "morta" || entry.ID == "corsaro-account" || entry.ID == smartfeed.DefinitionID {
			continue
		}
		if isGenericDefinition(entry.ID) {
			types = append(types, entry)
			continue
		}
		// A site with no native definition and no known software cannot work
		// yet: hide it from the user instead of offering an entry that cannot
		// be used. The maintenance view keeps it so the queue stays visible to
		// the people who write definitions.
		if !q.Queue && entry.Source != "" && entry.Source != "native" && entry.Family == "" {
			continue
		}
		// The user-facing catalog only lists sources that already work. The
		// full inventory (drafts, to-try, blocked) stays in the index and is
		// reachable with ?queue=1 for maintenance.
		if !q.Queue && !isUsableSiteState(entry.State) {
			continue
		}
		sites = append(sites, entry)
	}

	counterTotal, counterVerified := 0, 0
	for _, entry := range append(append([]catalog.IndexEntry{}, sites...), types...) {
		counterTotal++
		if isUsableSiteState(entry.State) {
			counterVerified++
		}
	}
	view.Counter = s.translate(r, "idx.catalog.counter", counterTotal, counterVerified)
	if q.Queue {
		nativeCount, familyCount := 0, 0
		for _, entry := range sites {
			if entry.Source == "" || entry.Source == "native" {
				nativeCount++
			} else if entry.Family != "" {
				familyCount++
			}
		}
		view.Coverage = s.translate(r, "idx.catalog.coverage", nativeCount, familyCount)
		// The queue is the only place where the maintainer sees the whole
		// inventory, so the head says how it is composed instead of leaving the
		// counts to a manual grep of index.json.
		var toWrite, drafts, blocked, dead int
		for _, entry := range index {
			switch entry.State {
			case catalog.SiteStateBlocked:
				blocked++
			case catalog.SiteStateDead:
				dead++
			case "bozza":
				drafts++
			}
			if entry.State == catalog.SiteStateTry && entry.Source != "" && entry.Source != "native" && entry.Family == "" {
				toWrite++
			}
		}
		view.QueueLine = s.translate(r, "idx.catalog.queueLine", toWrite, drafts, blocked, dead)
	}

	filtered := make([]catalog.IndexEntry, 0, len(sites))
	for _, entry := range sites {
		if !catalogEntryMatches(entry, q, query) {
			continue
		}
		filtered = append(filtered, entry)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if q.Sort == "used" {
			leftUsage, rightUsage := filtered[i].Usage, filtered[j].Usage
			if _, ok := added[filtered[i].ID]; ok {
				leftUsage++
			}
			if _, ok := added[filtered[j].ID]; ok {
				rightUsage++
			}
			if leftUsage != rightUsage {
				return leftUsage > rightUsage
			}
			leftLang := strings.HasPrefix(strings.ToLower(filtered[i].Language), strings.ToLower(string(requestLanguage(r))))
			rightLang := strings.HasPrefix(strings.ToLower(filtered[j].Language), strings.ToLower(string(requestLanguage(r))))
			if leftLang != rightLang {
				return leftLang
			}
		}
		left, right := strings.ToLower(filtered[i].Name), strings.ToLower(filtered[j].Name)
		if left == right {
			return filtered[i].ID < filtered[j].ID
		}
		return left < right
	})
	view.Filtered = s.translate(r, "idx.catalog.filtered", len(filtered), len(sites))
	view.HasFilters = query != "" || q.Type != "" || q.Language != "" || q.Category != "" || q.Protocol != "" || q.State != "" || q.Route != ""
	view.ShowAll = view.HasFilters || q.Queue || r.URL.Query().Get("all") == "1"
	if !view.ShowAll {
		view.Guide = s.buildAddGuide(r, index, added, q.Needs)
	}

	limit := 50
	total := len(filtered)
	pages := (total + limit - 1) / limit
	if pages < 1 {
		pages = 1
	}
	page := q.Page
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	offset := (page - 1) * limit
	end := offset + limit
	if end > total {
		end = total
	}
	if offset > total {
		offset = total
	}
	for _, entry := range filtered[offset:end] {
		choice := s.siteChoice(r, entry, added[entry.ID])
		if _, ok := added[entry.ID]; ok && choice.Usage == 0 {
			choice.Usage = 1
		}
		view.Sites = append(view.Sites, choice)
	}
	view.Pager = newPager(r, "/indexers/new", int64(total), limit, offset)
	view.NotFound = len(filtered) == 0

	for _, entry := range types {
		view.Types = append(view.Types, s.siteChoice(r, entry, added[entry.ID]))
	}
	sort.SliceStable(view.Types, func(i, j int) bool {
		return strings.ToLower(view.Types[i].Name) < strings.ToLower(view.Types[j].Name)
	})

	view.Groups = s.catalogFilterGroups(r, index, q)
	view.TypeFilter = q.Type
	view.LanguageFilter = q.Language
	view.TypeOptions = []siteFilterSelect{
		{Value: "", Label: s.translate(r, "idx.catalog.allTypes"), Selected: q.Type == ""},
		{Value: "public", Label: s.translate(r, "idx.badgePublic"), Selected: q.Type == "public"},
		{Value: "semi-private", Label: s.translate(r, "idx.badgeSemiPrivate"), Selected: q.Type == "semi-private"},
		{Value: "private", Label: s.translate(r, "idx.badgePrivate"), Selected: q.Type == "private"},
	}
	view.LanguageOptions = []siteFilterSelect{
		{Value: "", Label: s.translate(r, "idx.catalog.allLanguages"), Selected: q.Language == ""},
	}
	languages := catalogLanguages(index)
	languageNames := make(map[string]int, len(languages))
	for _, language := range languages {
		languageNames[catalogLanguageName(language, string(requestLanguage(r)))]++
	}
	for _, language := range languages {
		label := catalogLanguageName(language, string(requestLanguage(r)))
		if languageNames[label] > 1 {
			label += " (" + catalogLanguageBadge(language) + ")"
		}
		view.LanguageOptions = append(view.LanguageOptions, siteFilterSelect{
			Value:    language,
			Label:    label,
			Selected: strings.EqualFold(q.Language, language),
		})
	}
	view.Hidden = catalogHiddenFilters(query, q)
	return view, nil
}

// addNeeds is the fixed list of user needs in the order §2 lists them. The
// italian need is independent of the site language: it uses the catalogue's
// Italian priority list.
var addNeeds = []struct {
	Key   string
	Label string
}{
	{"tv", "idx.guide.need.tv"},
	{"movies", "idx.guide.need.movies"},
	{"anime", "idx.guide.need.anime"},
	{"music", "idx.guide.need.music"},
	{"books", "idx.guide.need.books"},
	{"italian", "idx.guide.need.italian"},
	{"other", "idx.guide.need.other"},
}

func (s *Server) buildAddGuide(r *http.Request, index []catalog.IndexEntry, added map[string]store.Indexer, needs []string) *guideView {
	active := make(map[string]bool, len(needs))
	for _, need := range needs {
		active[need] = true
	}
	guide := &guideView{
		ActiveNeed:   strings.Join(needs, ","),
		ShowAllURL:   "/indexers/new?all=1",
		BackURL:      "/indexers/new",
		SearchAllURL: "/indexers/new?all=1",
	}
	var labels []string
	for _, need := range addNeeds {
		choice := needChoice{
			Key:    need.Key,
			Label:  s.translate(r, need.Label),
			Tip:    s.translate(r, "tip.addNeed"),
			URL:    needToggleURL(r, need.Key, needs),
			Active: active[need.Key],
		}
		if choice.Active {
			labels = append(labels, choice.Label)
		}
		guide.Needs = append(guide.Needs, choice)
	}
	if len(labels) > 0 {
		guide.ActiveLabel = strings.Join(labels, " · ")
	} else {
		guide.ActiveNeed = ""
		guide.ActiveLabel = s.translate(r, "idx.guide.all")
	}

	lang := strings.ToLower(string(requestLanguage(r)))
	candidates := make([]catalog.IndexEntry, 0, len(index))
	for _, entry := range index {
		if entry.ID == "example-html" || entry.ID == "corsaro-account" || entry.State == "morta" || isGenericDefinition(entry.ID) {
			continue
		}
		if entry.Source != "" && entry.Source != "native" && entry.Family == "" {
			continue
		}
		if !matchesAnyNeed(entry, needs) {
			continue
		}
		candidates = append(candidates, entry)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := guideScore(candidates[i], lang, needs), guideScore(candidates[j], lang, needs)
		if left != right {
			return left > right
		}
		return strings.ToLower(candidates[i].Name) < strings.ToLower(candidates[j].Name)
	})
	if len(candidates) > 6 {
		candidates = candidates[:6]
	}
	for _, entry := range candidates {
		site := s.siteChoice(r, entry, added[entry.ID])
		guide.Recommended = append(guide.Recommended, guideSite{
			siteChoice:       site,
			Reason:           s.guideReason(r, entry, lang),
			NeedsLabel:       s.guideNeeds(r, entry),
			ProvidesLabel:    s.guideProvides(r, entry),
			ReliabilityLabel: s.guideReliability(r, entry),
		})
	}
	return guide
}

// parseNeeds reads a multi-select need filter. The value may arrive as one
// comma-separated parameter or as repeated parameters; unknown keys are
// dropped and the catalogue order is restored.
func parseNeeds(values url.Values) []string {
	selected := map[string]bool{}
	for _, raw := range values["need"] {
		for _, part := range strings.Split(raw, ",") {
			selected[strings.ToLower(strings.TrimSpace(part))] = true
		}
	}
	out := make([]string, 0, len(selected))
	for _, need := range addNeeds {
		if selected[need.Key] {
			out = append(out, need.Key)
		}
	}
	return out
}

// needToggleURL keeps every other filter and adds or removes one need, so
// "Films" and "Books" can stay active together.
func needToggleURL(r *http.Request, key string, active []string) string {
	next := make([]string, 0, len(active)+1)
	found := false
	for _, need := range active {
		if need == key {
			found = true
			continue
		}
		next = append(next, need)
	}
	if !found {
		next = append(next, key)
	}
	values := r.URL.Query()
	values.Del("page")
	values.Del("need")
	if len(next) > 0 {
		values.Set("need", strings.Join(next, ","))
	}
	if len(values) == 0 {
		return "/indexers/new"
	}
	return "/indexers/new?" + values.Encode()
}

func matchesAnyNeed(entry catalog.IndexEntry, needs []string) bool {
	if len(needs) == 0 {
		return true
	}
	for _, need := range needs {
		if addNeedMatches(entry, need) {
			return true
		}
	}
	return false
}

// guideScore rewards an entry that matches more of the selected needs.
func guideScore(entry catalog.IndexEntry, lang string, needs []string) int {
	base := addNeedScore(entry, lang, "")
	if len(needs) == 0 {
		return base
	}
	score := base
	for _, need := range needs {
		if addNeedMatches(entry, need) {
			score++
			if need == "italian" && entry.Italian {
				score += 20
			}
		}
	}
	return score
}

func addNeedMatches(entry catalog.IndexEntry, need string) bool {
	switch need {
	case "tv", "movies", "anime", "music", "books":
		return containsFold(entry.Categories, need)
	case "italian":
		return entry.Italian || strings.HasPrefix(strings.ToLower(entry.Language), "it")
	case "other":
		return !entry.Italian && !strings.HasPrefix(strings.ToLower(entry.Language), "it")
	default:
		return true
	}
}

// addNeedScore puts verified sites first, then the user's language, then the
// catalogue usage count. An unofficial mirror loses a few points because the
// official address is the better first choice.
func addNeedScore(entry catalog.IndexEntry, lang, need string) int {
	score := entry.Usage * 2
	if isUsableSiteState(entry.State) {
		score += 100
	}
	if entry.Italian || (lang != "" && strings.HasPrefix(strings.ToLower(entry.Language), lang)) {
		score += 20
	}
	if need == "italian" && entry.Italian {
		score += 20
	}
	if strings.EqualFold(entry.Mirror, "unofficial") {
		score -= 5
	}
	return score
}

func (s *Server) guideReason(r *http.Request, entry catalog.IndexEntry, lang string) string {
	verified := isUsableSiteState(entry.State)
	italian := entry.Italian || strings.HasPrefix(strings.ToLower(entry.Language), "it")
	langMatch := lang != "" && strings.HasPrefix(strings.ToLower(entry.Language), lang)
	switch {
	case verified && (italian || langMatch):
		return s.translate(r, "idx.guide.reasonVerifiedLang")
	case italian:
		return s.translate(r, "idx.guide.reasonItalian")
	case verified:
		return s.translate(r, "idx.guide.reasonVerified")
	case entry.Usage > 0:
		return s.translate(r, "idx.guide.reasonUsed")
	default:
		return s.translate(r, "idx.guide.reasonDraft")
	}
}

func (s *Server) guideNeeds(r *http.Request, entry catalog.IndexEntry) string {
	required := make([]string, 0, len(entry.Required))
	for _, name := range entry.Required {
		required = append(required, strings.ToLower(strings.TrimSpace(name)))
	}
	switch {
	case len(required) == 0:
		return s.translate(r, "idx.guide.needNone")
	case containsFold(required, "cookie"):
		return s.translate(r, "idx.guide.needCookie")
	case containsFold(required, "apikey") || containsFold(required, "api_key") || containsFold(required, "passkey"):
		return s.translate(r, "idx.guide.needAPIKey")
	case containsFold(required, "username") || containsFold(required, "password"):
		return s.translate(r, "idx.guide.needAccount")
	default:
		return s.translate(r, "idx.guide.needURL")
	}
}

func (s *Server) guideProvides(r *http.Request, entry catalog.IndexEntry) string {
	lower := strings.ToLower(entry.ID + " " + entry.Family + " " + entry.Type)
	switch {
	case strings.Contains(lower, "rss"):
		return s.translate(r, "idx.guide.providesFeed")
	case strings.EqualFold(entry.Protocol, "usenet"):
		return s.translate(r, "idx.guide.providesUsenet")
	case entry.FeedOnly && entry.LatestList:
		return s.translate(r, "idx.guide.providesFeedOnly")
	case entry.FeedOnly:
		return s.translate(r, "idx.guide.providesFeedOnlyNoList")
	case entry.Storable:
		// The definition declares a real latest page and no feed of its own:
		// the card says both that the list exists and that it can be stored.
		return s.translate(r, "idx.guide.providesStorable")
	case entry.LatestList:
		return s.translate(r, "idx.guide.providesLatestList")
	case entry.Scheduled:
		return s.translate(r, "idx.guide.providesUpdates")
	default:
		return s.translate(r, "idx.guide.providesSearchOnly")
	}
}

func (s *Server) guideReliability(r *http.Request, entry catalog.IndexEntry) string {
	label := s.siteStateLabel(r, entry.State)
	if strings.EqualFold(entry.Mirror, "unofficial") {
		label += " · " + s.translate(r, "idx.badgeMirror")
	}
	return label
}

func (s *Server) siteChoice(r *http.Request, entry catalog.IndexEntry, added store.Indexer) siteChoice {
	choice := siteChoice{
		ID:            entry.ID,
		Name:          entry.Name,
		Description:   entry.Description,
		Note:          entry.Note,
		Type:          entry.Type,
		TypeLabel:     s.catalogTypeLabel(r, entry.Type),
		Language:      entry.Language,
		LanguageBadge: catalogLanguageBadge(entry.Language),
		Protocol:      entry.Protocol,
		State:         entry.State,
		StateLabel:    s.siteStateLabel(r, entry.State),
		Categories:    s.catalogCategoryLabels(r, entry.Categories),
		AccessLabel:   s.catalogAccessLabel(r, entry.Required),
		QuickAdd:      catalogQuickAdd(entry),
		Configurable:  entry.Source == "" || entry.Source == "native" || entry.Family != "",
		Probe:         entry.Source != "" && entry.Source != "native",
		ToWrite:       entry.Source != "" && entry.Source != "native" && entry.Family == "" && entry.State != catalog.SiteStateBlocked,
		Reachable:     entry.Reachable,
		Italian:       entry.Italian || strings.HasPrefix(strings.ToLower(entry.Language), "it"),
		Usage:         entry.Usage,
		Latest:        entry.Latest,
		LatestList:    entry.LatestList,
		Storable:      entry.Storable,
		Experimental:  !isUsableSiteState(entry.State),
	}
	if len(entry.Hosts) > 0 {
		choice.URL = "https://" + entry.Hosts[0] + "/"
		for _, host := range entry.Hosts[1:] {
			choice.Mirrors = append(choice.Mirrors, "https://"+host+"/")
		}
	}
	choice.Description = s.catalogDescription(r, entry, choice.Categories)
	if entry.ID == "corsaro" {
		choice.OptionalID = "corsaro-account"
	}
	if added.ID != 0 {
		choice.Added = true
		choice.AddedID = added.ID
	}
	return choice
}

func (s *Server) catalogDescription(r *http.Request, entry catalog.IndexEntry, categories []string) string {
	key := "idx.catalog.descPublic"
	switch strings.ToLower(strings.TrimSpace(entry.Type)) {
	case "private":
		key = "idx.catalog.descPrivate"
	case "semi-private":
		key = "idx.catalog.descSemiPrivate"
	}
	if len(categories) > 0 {
		return s.translate(r, key, strings.Join(categories, ", "))
	}
	return s.translate(r, key, s.translate(r, "idx.catalog.descGeneric"))
}

func (s *Server) catalogFilterGroups(r *http.Request, index []catalog.IndexEntry, current catalogQuery) []siteFilterGroup {
	groups := []siteFilterGroup{
		{
			Label: s.translate(r, "idx.catalog.quickFilters"),
			Chips: []siteFilterChip{
				{Label: s.translate(r, "idx.catalog.italian"), URL: catalogFilterURL(r, "language", "it"), Active: current.Language == "it"},
				{Label: s.translate(r, "idx.catalog.mostUsed"), URL: catalogFilterURL(r, "sort", "used"), Active: current.Sort == "used"},
			},
		},
	}
	categoryGroup := siteFilterGroup{Label: s.translate(r, "idx.catalog.category")}
	for _, category := range catalogCategoryOrder {
		categoryGroup.Chips = append(categoryGroup.Chips, siteFilterChip{
			Label:  s.translate(r, category.LabelKey),
			URL:    catalogFilterURL(r, "cat", category.Key),
			Active: current.Category == category.Key,
		})
	}
	groups = append(groups,
		categoryGroup,
		siteFilterGroup{
			Label: s.translate(r, "common.protocol"),
			Chips: []siteFilterChip{
				{Label: "torrent", URL: catalogFilterURL(r, "proto", "torrent"), Active: current.Protocol == "torrent"},
				{Label: "usenet", URL: catalogFilterURL(r, "proto", "usenet"), Active: current.Protocol == "usenet"},
				{Label: "ed2k", URL: catalogFilterURL(r, "proto", "ed2k"), Active: current.Protocol == "ed2k"},
			},
		},
	)
	if !current.Queue {
		return groups
	}
	statusChips := []siteFilterChip{
		{Label: s.translate(r, "idx.siteState.verified"), URL: catalogFilterURL(r, "state", "verified"), Active: current.State == "verified"},
		{Label: s.translate(r, "idx.siteState.draft"), URL: catalogFilterURL(r, "state", "testing"), Active: current.State == "testing"},
		{Label: s.translate(r, "idx.siteState.blocked"), URL: catalogFilterURL(r, "state", "blocked"), Active: current.State == "blocked"},
	}
	return append(groups,
		siteFilterGroup{
			Label: s.translate(r, "common.status"),
			Chips: statusChips,
		},
		siteFilterGroup{
			Label: s.translate(r, "idx.catalog.route"),
			Chips: []siteFilterChip{
				{Label: s.translate(r, "idx.catalog.routeNative"), URL: catalogFilterURL(r, "route", "native"), Active: current.Route == "native"},
				{Label: s.translate(r, "idx.catalog.routeFamily"), URL: catalogFilterURL(r, "route", "family"), Active: current.Route == "family"},
			},
		},
	)
}

func (s *Server) catalogTypeLabel(r *http.Request, raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "public":
		return s.translate(r, "idx.badgePublic")
	case "semi-private":
		return s.translate(r, "idx.badgeSemiPrivate")
	default:
		return s.translate(r, "idx.badgePrivate")
	}
}

func (s *Server) catalogAccessLabel(r *http.Request, required []string) string {
	account, key, address := false, false, false
	for _, name := range required {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "username", "user", "email", "password", "pass", "login":
			account = true
		case "apikey", "api_key", "api-key", "token", "passkey", "key":
			key = true
		case "url", "base_url", "feed", "rss":
			address = true
		}
	}
	switch {
	case account:
		return s.translate(r, "idx.catalog.accountRequired")
	case key:
		return s.translate(r, "idx.catalog.apiKeyRequired")
	case address:
		return s.translate(r, "idx.catalog.urlRequired")
	default:
		return ""
	}
}

func (s *Server) catalogCategoryLabels(r *http.Request, groups []string) []string {
	if len(groups) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(groups))
	for _, group := range groups {
		seen[group] = true
	}
	out := make([]string, 0, len(groups))
	for _, category := range catalogCategoryOrder {
		if seen[category.Key] {
			out = append(out, s.translate(r, category.LabelKey))
		}
	}
	return out
}

func catalogQuickAdd(entry catalog.IndexEntry) bool {
	return strings.EqualFold(entry.Type, "public") && len(entry.Required) == 0 &&
		entry.ID != "example-html" && !isGenericDefinition(entry.ID)
}

func catalogLanguageBadge(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	return strings.ToUpper(raw)
}

var catalogLanguageNames = map[string]map[string]string{
	"bg": {"it": "Bulgaro", "en": "Bulgarian"},
	"cs": {"it": "Ceco", "en": "Czech"},
	"de": {"it": "Tedesco", "en": "German"},
	"en": {"it": "Inglese", "en": "English"},
	"es": {"it": "Spagnolo", "en": "Spanish"},
	"fr": {"it": "Francese", "en": "French"},
	"hu": {"it": "Ungherese", "en": "Hungarian"},
	"is": {"it": "Islandese", "en": "Icelandic"},
	"it": {"it": "Italiano", "en": "Italian"},
	"ko": {"it": "Coreano", "en": "Korean"},
	"pl": {"it": "Polacco", "en": "Polish"},
	"pt": {"it": "Portoghese", "en": "Portuguese"},
	"ru": {"it": "Russo", "en": "Russian"},
	"uk": {"it": "Ucraino", "en": "Ukrainian"},
	"vi": {"it": "Vietnamita", "en": "Vietnamese"},
	"zh": {"it": "Cinese", "en": "Chinese"},
}

func catalogLanguageName(raw, uiLanguage string) string {
	key := strings.ToLower(strings.TrimSpace(raw))
	if index := strings.IndexAny(key, "-_"); index > 0 {
		key = key[:index]
	}
	if names := catalogLanguageNames[key]; names != nil {
		lang := strings.ToLower(strings.TrimSpace(uiLanguage))
		if index := strings.IndexAny(lang, "-_"); index > 0 {
			lang = lang[:index]
		}
		if name := names[lang]; name != "" {
			return name
		}
		return names["en"]
	}
	if lang, ok := i18n.Lookup(raw); ok {
		return lang.Name()
	}
	return raw
}

func catalogLanguages(index []catalog.IndexEntry) []string {
	seen := map[string]bool{}
	for _, entry := range index {
		language := strings.TrimSpace(entry.Language)
		if language == "" {
			continue
		}
		key := strings.ToLower(language)
		if seen[key] {
			continue
		}
		seen[key] = true
	}
	out := make([]string, 0, len(seen))
	for _, entry := range index {
		language := strings.TrimSpace(entry.Language)
		if language == "" || !seen[strings.ToLower(language)] {
			continue
		}
		delete(seen, strings.ToLower(language))
		out = append(out, language)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

type catalogQuery struct {
	Type     string
	Language string
	Category string
	Protocol string
	State    string
	Route    string
	Sort     string
	Page     int
	Needs    []string
	Queue    bool
}

var catalogFilterValues = map[string]map[string]bool{
	"type":  {"public": true, "semi-private": true, "private": true},
	"proto": {"torrent": true, "usenet": true},
	"state": {"verified": true, "testing": true, "blocked": true},
	"route": {"native": true, "family": true, "pending": true},
	"sort":  {"used": true, "name": true},
	"cat": {
		"movies": true, "tv": true, "music": true, "books": true,
		"anime": true, "games": true, "software": true, "xxx": true,
	},
}

func parseCatalogQuery(r *http.Request) catalogQuery {
	values := r.URL.Query()
	out := catalogQuery{
		Type:     catalogFilterValue(values.Get("type"), "type"),
		Language: strings.ToLower(strings.TrimSpace(values.Get("language"))),
		Category: catalogFilterValue(values.Get("cat"), "cat"),
		Protocol: catalogFilterValue(values.Get("proto"), "proto"),
		State:    catalogFilterValue(values.Get("state"), "state"),
		Route:    catalogFilterValue(values.Get("route"), "route"),
		Sort:     catalogFilterValue(values.Get("sort"), "sort"),
		Page:     intParam(values.Get("page"), 1),
		Needs:    parseNeeds(values),
		Queue:    values.Get("queue") == "1",
	}
	if out.Sort == "" {
		out.Sort = "used"
	}
	return out
}

func catalogFilterValue(raw, key string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	if allowed := catalogFilterValues[key]; allowed != nil && !allowed[raw] {
		return ""
	}
	return raw
}

func catalogEntryMatches(entry catalog.IndexEntry, q catalogQuery, query string) bool {
	if q.Type != "" && !strings.EqualFold(entry.Type, q.Type) {
		return false
	}
	if q.Language != "" && !strings.EqualFold(entry.Language, q.Language) {
		return false
	}
	if q.Category != "" && !containsFold(entry.Categories, q.Category) {
		return false
	}
	if q.Protocol != "" && !strings.EqualFold(entry.Protocol, q.Protocol) {
		return false
	}
	switch q.Route {
	case "native":
		if entry.Source != "" && entry.Source != "native" {
			return false
		}
	case "family":
		if entry.Family == "" || entry.Source == "" || entry.Source == "native" {
			return false
		}
	case "pending":
		if entry.Source == "" || entry.Source == "native" || entry.Family != "" {
			return false
		}
	}
	switch q.State {
	case "verified":
		if !isUsableSiteState(entry.State) {
			return false
		}
	case "testing":
		if isUsableSiteState(entry.State) || entry.State == catalog.SiteStateBlocked {
			return false
		}
	case "blocked":
		if entry.State != catalog.SiteStateBlocked {
			return false
		}
	}
	if query = strings.ToLower(strings.TrimSpace(query)); query != "" {
		matched := strings.Contains(strings.ToLower(entry.Name), query) ||
			strings.Contains(strings.ToLower(entry.ID), query) ||
			strings.Contains(strings.ToLower(entry.Description), query) ||
			strings.Contains(strings.ToLower(entry.Language), query) ||
			containsFold(entry.Hosts, query)
		if !matched {
			return false
		}
	}
	return true
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

func catalogFilterURL(r *http.Request, key, value string) string {
	values := r.URL.Query()
	values.Del("page")
	if strings.EqualFold(values.Get(key), value) {
		values.Del(key)
	} else {
		values.Set(key, value)
	}
	if len(values) == 0 {
		return "/indexers/new"
	}
	return "/indexers/new?" + values.Encode()
}

func catalogHiddenFilters(query string, q catalogQuery) []catalogQueryPair {
	var out []catalogQueryPair
	if query != "" {
		out = append(out, catalogQueryPair{"q", query})
	}
	if q.Type != "" {
		out = append(out, catalogQueryPair{"type", q.Type})
	}
	if q.Language != "" {
		out = append(out, catalogQueryPair{"language", q.Language})
	}
	if q.Category != "" {
		out = append(out, catalogQueryPair{"cat", q.Category})
	}
	if q.Protocol != "" {
		out = append(out, catalogQueryPair{"proto", q.Protocol})
	}
	if q.State != "" {
		out = append(out, catalogQueryPair{"state", q.State})
	}
	if q.Route != "" {
		out = append(out, catalogQueryPair{"route", q.Route})
	}
	if len(q.Needs) > 0 {
		out = append(out, catalogQueryPair{"need", strings.Join(q.Needs, ",")})
	}
	return out
}

func (s *Server) catalogAlreadyAdded(r *http.Request, index []catalog.IndexEntry) (map[string]store.Indexer, error) {
	indexers, err := s.svc.Indexers(r.Context())
	if err != nil {
		return nil, err
	}
	byDefinition := make(map[string]store.Indexer, len(indexers))
	bridgeHosts := map[string]store.Indexer{}
	for _, in := range indexers {
		if _, exists := byDefinition[in.DefinitionID]; !exists {
			byDefinition[in.DefinitionID] = in
		}
		if !isBridgeDefinition(in.DefinitionID) {
			continue
		}
		for _, raw := range publicConfigMap(in.ConfigJSON) {
			parsed, err := url.Parse(strings.TrimSpace(raw))
			if err != nil || parsed.Hostname() == "" {
				continue
			}
			host := normalizeSiteHost(parsed.Hostname())
			if _, exists := bridgeHosts[host]; !exists {
				bridgeHosts[host] = in
			}
		}
	}
	out := make(map[string]store.Indexer, len(index))
	for _, entry := range index {
		if in, ok := byDefinition[entry.ID]; ok {
			out[entry.ID] = in
			continue
		}
		for _, host := range entry.Hosts {
			if in, ok := bridgeHosts[host]; ok {
				out[entry.ID] = in
				break
			}
		}
	}
	return out, nil
}

func isBridgeDefinition(id string) bool {
	switch id {
	case "torznab-generic", "newznab-generic", "rss":
		return true
	default:
		return false
	}
}

func (s *Server) siteStateLabel(r *http.Request, state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "verificata":
		return s.translate(r, "idx.siteState.verified")
	case "stabile":
		return s.translate(r, "idx.siteState.stable")
	case "degradata":
		return s.translate(r, "idx.siteState.problems")
	case catalog.SiteStateBlocked:
		return s.translate(r, "idx.siteState.blocked")
	default:
		return s.translate(r, "idx.siteState.draft")
	}
}

func isGenericDefinition(id string) bool {
	switch id {
	case "unit3d-generic", "newznab-generic", "torznab-generic", "gazelle-generic",
		"nexusphp-generic", "torrenttrader-generic", "xbtit-generic", "rss":
		return true
	}
	return strings.HasSuffix(id, "-generic")
}

func definitionCategoryIDs(def *definition.Definition) []int {
	if def == nil {
		return nil
	}
	seen := map[int]bool{}
	out := make([]int, 0, len(def.Categories))
	for _, category := range def.Categories {
		parent := categories.Parent(category.Standard)
		if parent == 0 {
			parent = category.Standard
		}
		if seen[parent] {
			continue
		}
		seen[parent] = true
		out = append(out, parent)
	}
	sort.Ints(out)
	return out
}

func (s *Server) definitionCategoryLabels(r *http.Request, ids []int) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, s.categoryLabel(r, id))
	}
	return out
}

func isAdultCategory(id int) bool {
	return id >= 6000 && id < 7000
}

func (s *Server) defaultCategoryGroups(r *http.Request, selected string) []categoryOptionGroup {
	selected = strings.TrimSpace(selected)
	groups := make([]categoryOptionGroup, 0, len(categories.Groups()))
	for _, group := range categories.Groups() {
		item := categoryOptionGroup{Label: s.categoryLabel(r, group.Top.ID)}
		item.Options = append(item.Options, indexerSettingOption{
			Value:    strconv.Itoa(group.Top.ID),
			Label:    strconv.Itoa(group.Top.ID) + " · " + s.categoryLabel(r, group.Top.ID),
			Selected: selected == strconv.Itoa(group.Top.ID),
		})
		for _, child := range group.Children {
			item.Options = append(item.Options, indexerSettingOption{
				Value:    strconv.Itoa(child.ID),
				Label:    strconv.Itoa(child.ID) + " · " + s.categoryLabel(r, child.ID),
				Selected: selected == strconv.Itoa(child.ID),
			})
		}
		groups = append(groups, item)
	}
	return groups
}

func (s *Server) protocolLabel(r *http.Request, protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "usenet":
		return s.translate(r, "idx.protocolUsenet")
	case "ed2k":
		return s.translate(r, "idx.protocolEd2k")
	default:
		return s.translate(r, "idx.protocolTorrentMagnet")
	}
}

func (s *Server) categoryLabel(r *http.Request, id int) string {
	key := "category." + strconv.Itoa(id)
	lang := requestLanguage(r)
	label := i18n.T(lang, key)
	if label == key || strings.TrimSpace(label) == "" {
		name := categories.Name(strconv.Itoa(id))
		if strings.HasPrefix(strings.ToLower(name), "category ") {
			if parent := categories.Parent(id); parent != 0 && parent != id {
				return categories.Name(strconv.Itoa(parent))
			}
		}
		return name
	}
	return label
}

// ------------------------------------------------------------- compact form

type categoryOptionGroup struct {
	Label   string
	Options []indexerSettingOption
}

type compactFormView struct {
	base
	Choosing                  bool
	Picker                    *sitePickerView
	IsNew                     bool
	Indexer                   store.Indexer
	DefinitionID              string
	DefinitionName            string
	Derived                   bool
	Name                      string
	Protocol                  string
	ProtocolKnown             bool
	ProtocolLabel             string
	Slug                      string
	Priority                  int
	Tags                      string
	ProxyID                   int64
	Proxies                   []proxyOption
	QueryLimit                int
	GrabLimit                 int
	MinSeeders                int
	SeedRatio                 float64
	SeedTimeMinutes           int
	SeasonPackSeedTimeMinutes int
	Query                     string
	DefaultCategory           string
	CategoryGroups            []categoryOptionGroup
	UserAgent                 string
	AllowPrivate              bool
	StoreLatest               bool
	MinSize                   string
	Challenge                 string
	ChallengeOptions          []indexerSettingOption
	FlareSolverrURL           string
	FlareSolverrTimeout       string
	Enabled                   bool
	URLValue                  string
	URLPlaceholder            string
	URLSecret                 bool
	URLChoices                []indexerSettingOption
	ShowURL                   bool
	URLLabel                  string
	IntervalSeconds           int
	IntervalChoices           []indexerSettingOption
	FrequencyLabel            string
	Categories                []categoryChoice
	CategoryHelp              string
	Help                      string
	RequiredFields            []indexerSettingField
	AccessFields              []indexerSettingField
	IsPrivate                 bool
	NoAccessFields            bool
	AdvancedFields            []indexerSettingField
	Problems                  []string
	Note                      string
	StateWarning              string
	MirrorWarning             string
	TLSWarning                string
	ChangeSiteURL             string
	IndexSources              []indexSourceView
}

type indexSourceView struct {
	Label string
	URL   string
}

func (s *Server) buildCompactIndexerForm(r *http.Request, defID string, current *store.Indexer, in *indexersvc.Input, problems []string, prefillURL string) (compactFormView, error) {
	data := compactFormView{
		base:          s.newBaseT(r, "idx.formTitle", "indexers", defID),
		IsNew:         current == nil,
		DefinitionID:  defID,
		Priority:      25,
		Enabled:       true,
		Problems:      problems,
		ChangeSiteURL: "/indexers/new",
		URLLabel:      s.translate(r, "idx.siteAddress"),
	}
	if current != nil {
		data.Indexer = *current
		data.Name = current.Name
		data.Protocol = current.Protocol
		data.Slug = current.Slug
		data.Priority = current.Priority
		data.Tags = current.Tags
		data.Enabled = current.Enabled
		data.QueryLimit = current.QueryLimit
		data.GrabLimit = current.GrabLimit
		data.MinSeeders = current.MinSeeders
		data.SeedRatio = current.SeedRatio
		data.SeedTimeMinutes = current.SeedTimeMinutes
		data.SeasonPackSeedTimeMinutes = current.SeasonPackSeedTimeMinutes
		if current.ProxyID != nil {
			data.ProxyID = *current.ProxyID
		}
	}
	if current != nil {
		data.IntervalSeconds = current.IntervalSeconds
	}
	if in != nil {
		data.Name = in.Name
		data.Slug = in.Slug
		data.Protocol = in.Protocol
		if in.Priority != nil {
			data.Priority = *in.Priority
		}
		if in.IntervalSeconds != nil {
			data.IntervalSeconds = *in.IntervalSeconds
		}
		data.Tags = strings.Join(in.Tags, ", ")
		if in.Enabled != nil {
			data.Enabled = *in.Enabled
		}
		if in.QueryLimit != nil {
			data.QueryLimit = *in.QueryLimit
		}
		if in.GrabLimit != nil {
			data.GrabLimit = *in.GrabLimit
		}
		if in.MinSeeders != nil {
			data.MinSeeders = *in.MinSeeders
		}
		if in.SeedRatio != nil {
			data.SeedRatio = *in.SeedRatio
		}
		if in.SeedTimeMinutes != nil {
			data.SeedTimeMinutes = *in.SeedTimeMinutes
		}
		if in.SeasonPackSeedTimeMinutes != nil {
			data.SeasonPackSeedTimeMinutes = *in.SeasonPackSeedTimeMinutes
		}
		if in.ProxyID != nil {
			data.ProxyID = *in.ProxyID
		}
		if in.ClearProxy {
			data.ProxyID = 0
		}
	}
	proxies, err := s.proxyOptions(r)
	if err != nil {
		return compactFormView{}, err
	}
	data.Proxies = proxies
	parsed, _, err := s.loadDefinitionForForm(r, defID)
	if err != nil {
		return compactFormView{}, err
	}
	data.DefinitionName = parsed.Meta.Name
	data.Derived = parsed.IsDerived()
	if parsed.Search.Index != nil {
		data.IndexSources = indexSourceViews(parsed,
			s.translate(r, "idx.indexSources.tv"), s.translate(r, "idx.indexSources.movies"))
	}
	if current == nil && in == nil && data.IntervalSeconds == 0 {
		if suggested := parsed.Meta.Interval.Duration(); suggested >= time.Minute {
			data.IntervalSeconds = int(suggested / time.Second)
		}
	}
	globalInterval := store.DefaultSettings().DefaultInterval()
	if settings, err := s.store.LoadSettings(r.Context()); err == nil {
		globalInterval = settings.DefaultInterval()
	}
	data.IntervalChoices = intervalChoices(requestLanguage(r), data.IntervalSeconds)
	data.FrequencyLabel, _ = s.frequencyLabels(r, data.IntervalSeconds, globalInterval)
	if !data.Derived {
		switch s.definitionState(r, defID) {
		case "bozza", "degradata":
			data.StateWarning = s.translate(r, "idx.testingWarning")
		}
	}
	if strings.EqualFold(strings.TrimSpace(parsed.Meta.Mirror), "unofficial") {
		data.MirrorWarning = s.translate(r, "indexerForm.mirrorWarning")
	}
	if parsed.Meta.InsecureTLS {
		data.TLSWarning = s.translate(r, "indexerForm.tlsWarning")
	}
	if current == nil && in == nil && strings.TrimSpace(data.Name) == "" {
		data.Name = parsed.Meta.Name
	}
	if data.Protocol == "" {
		data.Protocol = parsed.Meta.Protocol
	}
	if data.Protocol == "" {
		data.Protocol = "torrent"
	}
	switch strings.ToLower(strings.TrimSpace(defID)) {
	case "torznab-generic", "newznab-generic", "rss":
	default:
		data.ProtocolKnown = parsed.Meta.Protocol != ""
	}
	data.ProtocolLabel = s.protocolLabel(r, data.Protocol)

	config := map[string]string{}
	secretKeys := map[string]bool{}
	if current != nil {
		config = publicConfigMap(current.ConfigJSON)
		secrets, err := s.store.ListSecrets(r.Context(), current.ID)
		if err != nil {
			return compactFormView{}, err
		}
		for _, secret := range secrets {
			secretKeys[secret.Key] = true
		}
	}
	value := func(key string) string {
		if in != nil {
			if submitted, ok := in.Settings[key]; ok {
				return submitted
			}
		}
		return config[key]
	}
	data.Query = value("query")
	data.DefaultCategory = value("default_category")
	if current == nil && in == nil && strings.TrimSpace(data.DefaultCategory) == "" {
		for _, setting := range parsed.Settings {
			if setting.Name == "default-category" {
				data.DefaultCategory = strings.TrimSpace(setting.Default)
				break
			}
		}
	}
	if strings.TrimSpace(data.DefaultCategory) == "" {
		if ids := definitionCategoryIDs(parsed); len(ids) > 0 {
			data.DefaultCategory = strconv.Itoa(ids[0])
		}
	}
	data.CategoryGroups = s.defaultCategoryGroups(r, data.DefaultCategory)
	data.UserAgent = value("user_agent")
	data.AllowPrivate = truthy(value("allow_private"))
	data.StoreLatest = truthy(value("store_latest"))
	data.MinSize = value("min_size_mb")
	if data.MinSize == "" && strings.EqualFold(defID, "corsaro-user") {
		data.MinSize = "50"
	}
	data.Challenge = strings.ToLower(strings.TrimSpace(value("challenge")))
	switch data.Challenge {
	case "none", "flaresolverr":
	default:
		data.Challenge = ""
	}
	data.ChallengeOptions = []indexerSettingOption{
		{Value: "", Label: s.translate(r, "indexerForm.challengeGlobal"), Selected: data.Challenge == ""},
		{Value: "none", Label: s.translate(r, "indexerForm.challengeNone"), Selected: data.Challenge == "none"},
		{Value: "flaresolverr", Label: s.translate(r, "indexerForm.challengeFlareSolverr"), Selected: data.Challenge == "flaresolverr"},
	}
	data.FlareSolverrURL = value("flaresolverr_url")
	data.FlareSolverrTimeout = value("flaresolverr_timeout")
	if current != nil && secretKeys["url"] {
		data.URLSecret = true
		data.URLPlaceholder = s.redactedSecretURL(r, current.ID, "url")
	}

	definitionHasURL := false
	for _, setting := range parsed.Settings {
		if setting.Name == "url" {
			definitionHasURL = true
		}
	}
	urlValue := value("url")
	if !data.URLSecret {
		if urlValue == "" {
			urlValue = strings.TrimSpace(prefillURL)
		}
		knownSite := siteHasRealAddress(defID, parsed)
		if urlValue == "" && knownSite {
			urlValue = parsed.Meta.URLs[0]
		}
	}
	data.URLValue = urlValue
	knownSite := siteHasRealAddress(defID, parsed)

	for _, setting := range parsed.Settings {
		name := setting.Name
		if name == "url" {
			if data.URLSecret {
				data.ShowURL = true
				continue
			}
			if knownSite {
				// A known site already knows its address: never ask for it
				// outside the mirrors selector in Advanced.
				for _, raw := range parsed.Meta.URLs {
					data.URLChoices = append(data.URLChoices, indexerSettingOption{
						Value: raw, Label: raw, Selected: strings.TrimRight(raw, "/") == strings.TrimRight(urlValue, "/"),
					})
				}
				continue
			}
			data.ShowURL = true
			continue
		}
		if fixedSettingNames[name] || name == "default-category" {
			if name == "default-category" {
				raw := value(name)
				if current == nil && in == nil && raw == "" {
					raw = setting.Default
				}
				data.AdvancedFields = append(data.AdvancedFields, s.settingField(r, setting, raw, secretKeys[name], false))
			}
			continue
		}
		raw := value(name)
		if current == nil && in == nil && raw == "" {
			raw = setting.Default
		}
		field := s.settingField(r, setting, raw, secretKeys[name], false)
		switch {
		case setting.Required && isAccessSetting(name):
			// Credentials are the first thing the user must enter: keep them
			// at the top of the form instead of mixing them with the filters.
			data.AccessFields = append(data.AccessFields, field)
		case setting.Required:
			data.RequiredFields = append(data.RequiredFields, field)
		default:
			data.AdvancedFields = append(data.AdvancedFields, field)
		}
	}
	data.IsPrivate = strings.EqualFold(strings.TrimSpace(parsed.Meta.Type), "private")
	data.NoAccessFields = data.IsPrivate && len(data.AccessFields) == 0
	if !definitionHasURL {
		// Definitions that take the address from meta.urls need no URL field,
		// but when several mirrors exist the user still picks one.
		if len(parsed.Meta.URLs) > 1 {
			if data.URLValue == "" {
				data.URLValue = parsed.Meta.URLs[0]
			}
			for _, raw := range parsed.Meta.URLs {
				data.URLChoices = append(data.URLChoices, indexerSettingOption{
					Value: raw, Label: raw, Selected: strings.TrimRight(raw, "/") == strings.TrimRight(data.URLValue, "/"),
				})
			}
		}
	}

	selected := splitCategoryList(value("categories"))
	checked := map[string]bool{}
	for _, id := range selected {
		checked[id] = true
	}
	defaultChecked := len(selected) == 0
	for _, id := range definitionCategoryIDs(parsed) {
		value := strconv.Itoa(id)
		data.Categories = append(data.Categories, categoryChoice{
			ID:      value,
			Label:   s.categoryLabel(r, id),
			Checked: checked[value] || (defaultChecked && !isAdultCategory(id)),
		})
	}
	data.CategoryHelp = s.translate(r, "idx.categoriesHelp")
	data.Help = data.CategoryHelp
	return data, nil
}

func (s *Server) definitionState(r *http.Request, id string) string {
	if stored, err := s.store.GetDefinition(r.Context(), id); err == nil {
		return stored.State
	}
	if entry, ok := catalog.Get(id); ok {
		return entry.State
	}
	return ""
}

// siteHasRealAddress reports whether meta.urls names a concrete site rather
// than the placeholder of a generic definition.
func siteHasRealAddress(defID string, def *definition.Definition) bool {
	if def == nil || len(def.Meta.URLs) == 0 || isGenericDefinition(defID) {
		return false
	}
	raw := strings.ToLower(def.Meta.URLs[0])
	return !strings.Contains(raw, ".invalid") && !strings.Contains(raw, "example.")
}

// redactedSecretURL decrypts a URL secret only to build a redacted label for
// the edit form. The plaintext never reaches the response.
// indexerSecretValue opens one sealed setting. It is used for display
// decisions (is this URL a Smartfeed?) and never logged.
func (s *Server) indexerSecretValue(ctx context.Context, indexerID int64, key string) (string, bool) {
	if s.vault == nil {
		return "", false
	}
	secret, err := s.store.GetSecret(ctx, indexerID, key)
	if err != nil {
		return "", false
	}
	value, err := s.vault.Open(secret.Nonce, secret.Ciphertext)
	if err != nil {
		return "", false
	}
	return string(value), true
}

func (s *Server) redactedSecretURL(r *http.Request, indexerID int64, key string) string {
	value, ok := s.indexerSecretValue(r.Context(), indexerID, key)
	if !ok {
		return ""
	}
	return feed.RedactURLForDisplay(value)
}

func (s *Server) settingField(r *http.Request, setting definition.Setting, value string, hasSecret, forceRequired bool) indexerSettingField {
	label := s.settingLabel(r, setting.Name, setting.Label)
	field := indexerSettingField{
		Name:      setting.Name,
		Label:     label,
		Help:      s.settingHelp(r, setting.Name, setting.Help),
		Type:      settingFieldType(setting),
		Value:     value,
		Required:  setting.Required || forceRequired,
		HasSecret: hasSecret,
	}
	if indexersvc.IsSecretKey(setting.Name) {
		field.Type = "password"
		field.Secret = true
	}
	if field.Type == "checkbox" {
		field.Checked = truthy(value)
	}
	if field.Type == "select" {
		for _, option := range setting.Options {
			field.Options = append(field.Options, indexerSettingOption{
				Value: option, Label: option, Selected: option == value,
			})
		}
	}
	return field
}

// isAccessSetting marks the required fields that are credentials. They are
// pulled to the top of the form so nobody has to hunt for the username and
// password fields in Advanced.
func isAccessSetting(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch lower {
	case "username", "user", "email", "login", "password", "pass", "apikey",
		"api_key", "api-key", "token", "passkey", "cookie", "cookievalue",
		"session", "session_cookie", "rsskey", "auth", "secret":
		return true
	}
	for _, suffix := range []string{"_key", "_token", "_password", "_cookie", "_secret"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func (s *Server) settingLabel(r *http.Request, name, fallback string) string {
	key := "setting." + name + ".label"
	lang := requestLanguage(r)
	label := i18n.T(lang, key)
	if label != key && strings.TrimSpace(label) != "" {
		return label
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return name
}

// settingHelp prefers a translated help line over the help text written in
// the definition. Definitions stay language-neutral while the UI can explain
// a field (the pasted session cookie, for example) in all seven languages.
func (s *Server) settingHelp(r *http.Request, name, fallback string) string {
	key := "setting." + name + ".help"
	lang := requestLanguage(r)
	help := i18n.T(lang, key)
	if help != key && strings.TrimSpace(help) != "" {
		return help
	}
	return fallback
}

func splitCategoryList(raw string) []string {
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if value, err := strconv.Atoi(part); err != nil || value >= 100000 {
			continue
		}
		out = append(out, part)
	}
	return out
}

func (s *Server) indexerUnifiedCreate(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		s.renderError(w, r, http.StatusServiceUnavailable, s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	defID := strings.TrimSpace(r.FormValue("definition_id"))
	if defID == "rss" {
		if err := s.ensureRSSDefinition(r); err != nil {
			s.renderIndexerFormProblems(w, r, defID, nil, nil, []string{err.Error()})
			return
		}
		if rawURL := strings.TrimSpace(r.FormValue("url")); smartfeed.IsSmartfeedURL(rawURL) {
			// A personal Smartfeed URL is not a plain RSS feed: send the user
			// to the dedicated form, which pairs the native MirCrew indexer.
			s.smartfeedRecognized(w, r, rawURL)
			return
		}
	}
	parsed, _, err := s.loadDefinitionForForm(r, defID)
	if err != nil {
		s.renderIndexerFormProblems(w, r, defID, nil, nil, []string{s.translate(r, "indexerForm.invalidDefinition", err.Error())})
		return
	}
	in, err := s.indexerInputFromForm(r, parsed)
	if err != nil {
		s.renderIndexerFormProblems(w, r, defID, nil, nil, []string{err.Error()})
		return
	}
	if err := s.ensureStoredDefinition(r, defID); err != nil {
		s.renderIndexerFormProblems(w, r, defID, nil, nil, []string{err.Error()})
		return
	}
	if parsed.HasLatestList() && !parsed.Meta.HasLatest() {
		if needed, _, _, warnErr := s.storeWarningNeeded(r.Context(), in, 0); warnErr == nil && needed {
			s.showStoreWarning(w, r, defID, 0, in)
			return
		}
	}
	created, err := s.svc.Create(r.Context(), in)
	if err != nil {
		s.renderIndexerFormProblems(w, r, defID, nil, &in, []string{s.indexerFriendlyError(r, err)})
		return
	}
	if r.FormValue("test") != "" {
		data, buildErr := s.buildIndexerDetail(r, created.ID)
		if buildErr != nil {
			redirect(w, r, "/indexers/"+strconv.FormatInt(created.ID, 10), s.translate(r, "idx.detail.added", created.Name), "")
			return
		}
		data.Message = s.translate(r, "idx.detail.added", created.Name)
		if s.native != nil {
			result, testErr := s.native.Test(r.Context(), created.ID)
			data.Test = &result
			if testErr != nil {
				data.Error = s.translate(r, "indexer.testFailedDetail", testErr.Error())
			}
			if refreshed, err := s.buildIndexerDetail(r, created.ID); err == nil {
				data.State = refreshed.State
				data.HasState = refreshed.HasState
				data.NextUpdate = refreshed.NextUpdate
				data.HasNext = refreshed.HasNext
				data.LastRun = refreshed.LastRun
			}
		} else {
			data.Error = s.translate(r, "indexer.error.nativeEngineTest")
		}
		s.render(w, r, "indexer", data, http.StatusOK)
		return
	}
	redirect(w, r, "/indexers/"+strconv.FormatInt(created.ID, 10), s.translate(r, "indexer.created"), "")
}

func (s *Server) renderIndexerFormProblems(w http.ResponseWriter, r *http.Request, defID string, current *store.Indexer, in *indexersvc.Input, problems []string) {
	data, err := s.buildCompactIndexerForm(r, defID, current, in, problems, "")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if current != nil {
		data.Title = s.translate(r, "indexerForm.edit", current.Name)
	}
	data.Error = s.translate(r, "indexerForm.checkFields")
	s.render(w, r, "indexer_form", data, http.StatusBadRequest)
}

func (s *Server) indexerUnifiedEdit(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		s.renderError(w, r, http.StatusServiceUnavailable, s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	id, err := pathID(r)
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, s.translate(r, "indexer.error.invalidID"))
		return
	}
	current, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		s.renderError(w, r, statusForIndexerError(err), s.indexerErrorMessage(r, err))
		return
	}
	data, err := s.buildCompactIndexerForm(r, current.DefinitionID, &current, nil, nil, "")
	if err != nil {
		s.renderError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	data.Title = s.translate(r, "indexerForm.edit", current.Name)
	s.render(w, r, "indexer_form", data, http.StatusOK)
}

func (s *Server) indexerUnifiedUpdate(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		s.renderError(w, r, http.StatusServiceUnavailable, s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	current, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		redirect(w, r, "/indexers", "", s.indexerErrorMessage(r, err))
		return
	}
	defID := strings.TrimSpace(r.FormValue("definition_id"))
	if defID == "" {
		defID = current.DefinitionID
	}
	if defID == "rss" && strings.EqualFold(current.DefinitionID, "rss") {
		if rawURL := strings.TrimSpace(r.FormValue("url")); smartfeed.IsSmartfeedURL(rawURL) {
			s.smartfeedRecognized(w, r, rawURL)
			return
		}
	}
	parsed, _, err := s.loadDefinitionForForm(r, defID)
	if err != nil {
		s.renderCompactFormProblems(w, r, defID, &current, nil, []string{s.translate(r, "indexerForm.definitionInvalid", err.Error())})
		return
	}
	in, err := s.indexerInputFromForm(r, parsed)
	if err != nil {
		s.renderCompactFormProblems(w, r, defID, &current, nil, []string{err.Error()})
		return
	}
	if err := s.addExtraSecretValues(r, current.ID, parsed, &in); err != nil {
		s.renderCompactFormProblems(w, r, defID, &current, &in, []string{err.Error()})
		return
	}
	if err := s.ensureStoredDefinition(r, defID); err != nil {
		s.renderCompactFormProblems(w, r, defID, &current, &in, []string{err.Error()})
		return
	}
	if parsed.HasLatestList() && !parsed.Meta.HasLatest() {
		if needed, _, _, warnErr := s.storeWarningNeeded(r.Context(), in, id); warnErr == nil && needed {
			s.showStoreWarning(w, r, defID, id, in)
			return
		}
	}
	if _, err := s.svc.Update(r.Context(), id, in); err != nil {
		s.renderCompactFormProblems(w, r, defID, &current, &in, []string{s.indexerFriendlyError(r, err)})
		return
	}
	redirect(w, r, "/indexers/"+strconv.FormatInt(id, 10), s.translate(r, "indexer.saved"), "")
}

func (s *Server) renderCompactFormProblems(w http.ResponseWriter, r *http.Request, defID string, current *store.Indexer, in *indexersvc.Input, problems []string) {
	data, err := s.buildCompactIndexerForm(r, defID, current, in, problems, "")
	if err != nil {
		s.renderError(w, r, http.StatusBadRequest, err.Error())
		return
	}
	if current != nil {
		data.Title = s.translate(r, "indexerForm.edit", current.Name)
	}
	data.Error = s.translate(r, "indexerForm.checkFields")
	s.render(w, r, "indexer_form", data, http.StatusBadRequest)
}

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
type indexerAccessRow struct {
	Label      string
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
	LastRun           *store.IndexerHistory
	LastError         string
	NextUpdate        time.Time
	HasNext           bool
	// A failing source follows an escalating retry schedule; RetryPaused
	// means the 96 hour step already failed and the scheduler waits for the
	// operator.
	RetryFailures       int
	RetryPaused         bool
	RetryNext           time.Time
	HasRetryNext        bool
	FrequencyLabel      string
	FrequencySource     string
	HasState            bool
	State               store.IndexerState
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
	Latest              bool
	SearchOnly          bool
	UploaderOnly        bool
	UploaderMinutes     int
	CategoryMapped      bool
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
	ActiveMirror   string
	ProblemDetail  string
	ProblemAt      time.Time
	HasProblemAt   bool
	LogRows        []indexerLogRow
	AppLog         []logView
	RunsLink       string
	AppLogLink     string
	AccessFields   []indexerAccessRow
	MissingAccess  []string
	AccessEditLink string
	ListReturn     string
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
	default:
		data.Tab = "overview"
	}
	if r.URL.Query().Get("try") != "" {
		data.Tab = "overview"
		data.Try, err = s.buildIndexerTry(r, data.Indexer)
		if err != nil {
			s.renderError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
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
	}
	data.SearchOnly = !data.Latest
	config := publicConfigMap(in.ConfigJSON)
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
	healthView := s.loadIndexerHealth(r.Context(), in, def, nil)
	data.Health = healthView.Assessment
	data.State = healthView.State
	data.HasState = healthView.HasState
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
	data.AppLog = logViews(requestLanguage(r), s.indexerAppLog(in, 10), map[string]string{strings.ToLower(in.Slug): in.Name})
	runsValues := url.Values{}
	runsValues.Set("tab", "runs")
	runsValues.Set("indexer", strconv.FormatInt(in.ID, 10))
	data.RunsLink = "/activity?" + runsValues.Encode()
	logValues := url.Values{}
	logValues.Set("tab", "logs")
	if slug := strings.TrimSpace(in.Slug); slug != "" {
		logValues.Set("q", slug)
	}
	data.AppLogLink = "/activity?" + logValues.Encode()
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
	// Required credentials belong on the Overview tab too: the operator must
	// see at once whether the source can log in, and where to type them.
	if def != nil {
		for _, setting := range def.Settings {
			if !setting.Required || !isAccessSetting(setting.Name) {
				continue
			}
			name := strings.ToLower(strings.TrimSpace(setting.Name))
			configured := strings.TrimSpace(config[name]) != ""
			row := indexerAccessRow{}
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
			if !configured {
				data.MissingAccess = append(data.MissingAccess, label)
			}
		}
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

func (s *Server) indexerUnifiedTest(w http.ResponseWriter, r *http.Request) {
	s.indexerAction(w, r, false)
}

func (s *Server) indexerUnifiedRun(w http.ResponseWriter, r *http.Request) {
	s.indexerAction(w, r, true)
}

func (s *Server) indexerAction(w http.ResponseWriter, r *http.Request, run bool) {
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	if _, err := s.store.GetIndexer(r.Context(), id); err != nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.notFound"))
		return
	}
	data, buildErr := s.buildIndexerDetail(r, id)
	if buildErr != nil {
		redirect(w, r, "/indexers", "", s.indexerErrorMessage(r, buildErr))
		return
	}
	if s.native == nil {
		if run {
			data.Error = s.translate(r, "indexer.error.nativeEngineRun")
		} else {
			data.Error = s.translate(r, "indexer.error.nativeEngineTest")
		}
		s.render(w, r, "indexer", data, http.StatusOK)
		return
	}
	if run {
		stats, runErr := s.native.Run(r.Context(), id)
		if stats != nil {
			data.Run = stats
			if len(stats.Items) > 20 {
				data.RunItems = stats.Items[:20]
			} else {
				data.RunItems = stats.Items
			}
		}
		if runErr != nil {
			data.Error = s.translate(r, "indexer.runFailed", runErr.Error())
		}
	} else {
		result, testErr := s.native.Test(r.Context(), id)
		data.Test = &result
		if testErr != nil {
			data.Error = s.translate(r, "indexer.testFailedDetail", testErr.Error())
		}
	}
	if refreshed, err := s.buildIndexerDetail(r, id); err == nil {
		refreshed.Test = data.Test
		refreshed.Run = data.Run
		refreshed.RunItems = data.RunItems
		refreshed.Error = data.Error
		data = refreshed
	}
	s.render(w, r, "indexer", data, http.StatusOK)
}

func (s *Server) indexerUnifiedDelete(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		s.renderError(w, r, http.StatusServiceUnavailable, s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	returnPath := sanitizeIndexerReturn(r.FormValue("return"))
	if returnPath == "" {
		returnPath = "/indexers"
	}
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, returnPath, "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	if err := s.svc.Delete(r.Context(), id); err != nil {
		redirect(w, r, returnPath, "", s.indexerFriendlyError(r, err))
		return
	}
	redirect(w, r, returnPath, s.translate(r, "indexer.deleted"), "")
}

func (s *Server) indexerToggle(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	returnPath := sanitizeIndexerReturn(r.FormValue("return"))
	if returnPath == "" {
		returnPath = "/indexers"
	}
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, returnPath, "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	current, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		redirect(w, r, returnPath, "", s.indexerErrorMessage(r, err))
		return
	}
	enabled := !current.Enabled
	if _, err := s.svc.Update(r.Context(), id, indexersvc.Input{Enabled: &enabled}); err != nil {
		redirect(w, r, returnPath, "", s.indexerFriendlyError(r, err))
		return
	}
	if enabled {
		redirect(w, r, returnPath, s.translate(r, "idx.enabledMessage", current.Name), "")
		return
	}
	redirect(w, r, returnPath, s.translate(r, "idx.disabledMessage", current.Name), "")
}

// indexerListPath rebuilds the source list URL from the two supported
// filters, so a return path never carries anything else.
func indexerListPath(state, query string) string {
	values := url.Values{}
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "problem", "disabled", "ok":
		values.Set("state", strings.ToLower(strings.TrimSpace(state)))
	}
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		values.Set("q", trimmed)
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
	return indexerListPath(parsed.Query().Get("state"), parsed.Query().Get("q"))
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

// indexerReduceFrequency implements the "Riduci la frequenza" action of a rate
// limited source. It doubles the interval that is currently in force (the
// source's own value, or the global default when it has none), keeps it within
// the same 30-day ceiling the form accepts, and never touches the other
// sources. Live app searches are not delayed: the interval governs the
// automatic updates only.
func (s *Server) indexerReduceFrequency(w http.ResponseWriter, r *http.Request) {
	const (
		minInterval = 30 * 60
		maxInterval = 30 * 24 * 60 * 60
	)
	if s.svc == nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	current, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		redirect(w, r, "/indexers", "", s.indexerErrorMessage(r, err))
		return
	}
	target := frequencyReturnPath(r.FormValue("return"), id)
	settings, settingsErr := s.store.LoadSettings(r.Context())
	if settingsErr != nil {
		settings = store.DefaultSettings()
	}
	base := current.IntervalSeconds
	if base <= 0 {
		base = settings.DefaultIntervalSeconds
	}
	if base <= 0 {
		base = 3600
	}
	if base >= maxInterval {
		redirect(w, r, target, "", s.translate(r, "idx.frequencyMax", current.Name))
		return
	}
	next := base * 2
	if next < minInterval {
		next = minInterval
	}
	if next > maxInterval {
		next = maxInterval
	}
	if _, err := s.svc.Update(r.Context(), id, indexersvc.Input{IntervalSeconds: &next}); err != nil {
		redirect(w, r, target, "", s.indexerFriendlyError(r, err))
		return
	}
	redirect(w, r, target,
		s.translate(r, "idx.frequencyReduced", current.Name, intervalEveryLabel(requestLanguage(r), next)), "")
}

func (s *Server) indexerDuplicate(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	current, err := s.store.GetIndexer(r.Context(), id)
	if err != nil {
		redirect(w, r, "/indexers", "", s.indexerErrorMessage(r, err))
		return
	}
	settings := publicConfigMap(current.ConfigJSON)
	secrets, err := s.store.ListSecrets(r.Context(), id)
	if err != nil {
		redirect(w, r, "/indexers", "", err.Error())
		return
	}
	for _, secret := range secrets {
		if s.vault == nil {
			redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.vaultRequired"))
			return
		}
		value, err := s.vault.Open(secret.Nonce, secret.Ciphertext)
		if err != nil {
			redirect(w, r, "/indexers", "", err.Error())
			return
		}
		settings[secret.Key] = value
	}
	indexers, _ := s.store.ListIndexers(r.Context())
	names := make(map[string]bool, len(indexers))
	for _, in := range indexers {
		names[strings.ToLower(in.Name)] = true
	}
	name := uniqueIndexerName(current.Name, names)
	enabled := current.Enabled
	priority := current.Priority
	created, err := s.svc.Create(r.Context(), indexersvc.Input{
		DefinitionID: current.DefinitionID,
		Name:         name,
		Protocol:     current.Protocol,
		Priority:     &priority,
		Tags:         splitTagList(current.Tags),
		Enabled:      &enabled,
		Settings:     settings,
	})
	if err != nil {
		redirect(w, r, "/indexers", "", s.indexerFriendlyError(r, err))
		return
	}
	redirect(w, r, "/indexers/"+strconv.FormatInt(created.ID, 10), s.translate(r, "idx.duplicated", name), "")
}

func (s *Server) indexersBulk(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	returnPath := sanitizeIndexerReturn(r.FormValue("return"))
	if returnPath == "" {
		returnPath = "/indexers"
	}
	ids := make([]int64, 0)
	for _, raw := range r.Form["ids"] {
		if id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	action := strings.ToLower(strings.TrimSpace(r.FormValue("action")))
	if len(ids) == 0 {
		redirect(w, r, returnPath, "", s.translate(r, "idx.bulkNone"))
		return
	}
	count := 0
	for _, id := range ids {
		switch action {
		case "delete":
			if err := s.svc.Delete(r.Context(), id); err == nil {
				count++
			}
		case "disable", "enable":
			enabled := action == "enable"
			if _, err := s.svc.Update(r.Context(), id, indexersvc.Input{Enabled: &enabled}); err == nil {
				count++
			}
		}
	}
	redirect(w, r, returnPath, s.translate(r, "idx.bulkDone", count), "")
}

// ------------------------------------------------------------------- helpers

// indexSourceViews lists the read-only index pages of a definition for the
// indexer form. They are definition data, not user settings: the operator can
// see where the search reads titles without editing the YAML.
func indexSourceViews(def *definition.Definition, tvLabel, movieLabel string) []indexSourceView {
	if def == nil || def.Search.Index == nil {
		return nil
	}
	base := ""
	if len(def.Meta.URLs) > 0 {
		base = strings.TrimSpace(def.Meta.URLs[0])
	}
	resolve := func(ref string) string {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return ""
		}
		parsedRef, err := url.Parse(ref)
		if err != nil {
			return ref
		}
		if parsedRef.IsAbs() {
			return parsedRef.String()
		}
		parsedBase, err := url.Parse(base)
		if err != nil {
			return ref
		}
		return parsedBase.ResolveReference(parsedRef).String()
	}
	var out []indexSourceView
	for _, page := range def.Search.Index.Pages {
		if strings.TrimSpace(page.Letter) != "" {
			continue
		}
		// A list without letters is labelled by what it indexes: the TV
		// list is the common case, a movie list keeps the movie label.
		label := tvLabel
		if page.Standard/1000 == 2 {
			label = movieLabel
		}
		if link := resolve(page.Get); link != "" {
			out = append(out, indexSourceView{Label: label, URL: link})
		}
	}
	if link := resolve(def.Search.Index.Forum); link != "" {
		out = append(out, indexSourceView{Label: movieLabel, URL: link})
	}
	return out
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
