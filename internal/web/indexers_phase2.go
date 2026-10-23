package web

import (
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
	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/indexer"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/store"
)

// ------------------------------------------------------------------ UI models

// statusDot is the shared status picture of an indexer, an app or a client.
// State is one of ok, problem, disabled and testing (UI-GUIDE §5).
type statusDot struct {
	State string
	Label string
}

// uiProblem is the one-sentence cause plus the action next to it.
type uiProblem struct {
	Text        string
	ActionLabel string
	ActionURL   string
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
	defs := s.definitionCache(r)
	apps, _ := s.store.ListApplications(r.Context())
	_ = apps

	for _, in := range indexers {
		view := s.indexerRowView(r, in, defs)
		data.Total++
		switch view.Status.State {
		case "problem":
			data.Problems++
		case "disabled":
			data.Disabled++
		}
		if data.State == "problem" && view.Status.State != "problem" {
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

// definitionCache parses every stored definition once per page load. Catalog
// entries are loaded too, so an indexer created from a built-in definition has
// its name and metadata before the row is installed.
func (s *Server) definitionCache(r *http.Request) map[string]*definition.Definition {
	out := map[string]*definition.Definition{}
	defs, err := s.store.ListDefinitions(r.Context())
	if err == nil {
		for _, def := range defs {
			if parsed, err := definition.Parse([]byte(def.Body)); err == nil {
				out[def.ID] = parsed
			}
		}
	}
	if entries, err := catalog.Builtins(); err == nil {
		for _, entry := range entries {
			if _, ok := out[entry.ID]; !ok {
				out[entry.ID] = entry.Definition
			}
		}
	}
	return out
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

func (s *Server) indexerRowView(r *http.Request, in store.Indexer, defs map[string]*definition.Definition) indexerRowView {
	def := defs[in.DefinitionID]
	row := indexerRowView{
		Indexer: in,
		Badges:  s.indexerBadges(r, in, def),
		Filters: len(indexerFilterValues(in)),
	}
	if row.Filters == 1 {
		row.FiltersLabel = s.translate(r, "idx.filtersOne")
	} else if row.Filters > 1 {
		row.FiltersLabel = s.translate(r, "idx.filters", row.Filters)
	}
	var state store.IndexerState
	hasState := false
	if value, err := s.store.GetIndexerState(r.Context(), in.ID); err == nil {
		state = value
		hasState = true
	}
	var lastRun *store.IndexerHistory
	if history, err := s.store.ListIndexerHistory(r.Context(), in.ID, 50); err == nil {
		for i := range history {
			if history[i].Kind == "run" {
				lastRun = &history[i]
				break
			}
		}
	}
	if lastRun != nil {
		row.LastUpdate = lastRun.StartedAt
	}
	row.Status = s.indexerStatus(r, in, state, hasState, lastRun)
	if row.Status.State == "problem" {
		row.Problem = s.problemFor(r, in, state, lastRun)
	}
	return row
}

func (s *Server) indexerStatus(r *http.Request, in store.Indexer, state store.IndexerState, hasState bool, lastRun *store.IndexerHistory) statusDot {
	switch {
	case !in.Enabled:
		return statusDot{State: "disabled", Label: s.translate(r, "idx.statusDisabled")}
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
func (s *Server) problemFor(r *http.Request, in store.Indexer, state store.IndexerState, lastRun *store.IndexerHistory) *uiProblem {
	raw := strings.ToLower(strings.TrimSpace(state.LastError))
	if raw == "" && lastRun != nil {
		raw = strings.ToLower(strings.TrimSpace(lastRun.Error))
	}
	detail := "/indexers/" + strconv.FormatInt(in.ID, 10)
	switch {
	case strings.Contains(raw, "429") || strings.Contains(raw, "rate limit") || strings.Contains(raw, "too many"):
		return &uiProblem{
			Text:        s.translate(r, "idx.problem.rate"),
			ActionLabel: s.translate(r, "idx.problem.rateAction"),
			ActionURL:   "/settings",
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
	}
	rawURL := ""
	if config := publicConfigMap(in.ConfigJSON); config != nil {
		rawURL = config["url"]
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

// indexerFilterValues lists the user-configured filter values of an indexer.
func indexerFilterValues(in store.Indexer) map[string]string {
	out := map[string]string{}
	config := publicConfigMap(in.ConfigJSON)
	for key, value := range config {
		switch key {
		case "url", "categories", "query", "user_agent", "allow_private", "default_category":
			continue
		}
		if strings.TrimSpace(value) != "" {
			out[key] = value
		}
	}
	return out
}

// -------------------------------------------------------------- site chooser

type siteChoice struct {
	ID          string
	Name        string
	Description string
	Public      bool
	State       string
	StateLabel  string
	Categories  []string
	Generic     bool
	URLs        []string
}

type sitePickerView struct {
	Title       string
	Subtitle    string
	Action      string
	Query       string
	Sites       []siteChoice
	Types       []siteChoice
	NotFound    bool
	Message     string
	SiteSearch  string
	BrowseLabel string
	BridgeLabel string
	FeedLabel   string
	BridgeURL   string
	FeedURL     string
	Empty       uiEmpty
}

func (s *Server) indexerUnifiedNew(w http.ResponseWriter, r *http.Request) {
	if s.svc == nil {
		s.renderError(w, r, http.StatusServiceUnavailable, s.translate(r, "indexer.error.serviceUnavailable"))
		return
	}
	defID := strings.TrimSpace(r.URL.Query().Get("definition"))
	rawURL := strings.TrimSpace(r.URL.Query().Get("url"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))

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
//   - a verified/stabil site whose meta.urls contains the host;
//   - a Jackett/Prowlarr endpoint (the Torznab bridge);
//   - a Torznab/Newznab endpoint;
//   - an RSS/Atom feed.
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
	host := normalizeSiteHost(parsed.Hostname())
	entries, err := catalog.Builtins()
	if err == nil {
		for _, entry := range entries {
			if !isUsableSiteState(entry.State) || entry.Definition == nil {
				continue
			}
			for _, siteURL := range entry.Definition.Meta.URLs {
				site, err := url.Parse(siteURL)
				if err != nil {
					continue
				}
				if normalizeSiteHost(site.Hostname()) == host {
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
	view := s.sitePickerView(r, query)
	view.Message = message
	data := struct {
		base
		Choosing bool
		Picker   *sitePickerView
	}{base: s.newBaseT(r, "idx.addTitle", "indexers"), Choosing: true, Picker: view}
	s.render(w, r, "indexer_form", data, http.StatusOK)
}

func (s *Server) sitePickerView(r *http.Request, query string) *sitePickerView {
	view := &sitePickerView{
		Title:       s.translate(r, "idx.addTitle"),
		Subtitle:    s.translate(r, "idx.addSubtitle"),
		Action:      "/indexers/new",
		Query:       query,
		SiteSearch:  s.translate(r, "idx.searchSite"),
		BrowseLabel: s.translate(r, "idx.supportedSites"),
		BridgeLabel: s.translate(r, "idx.bridge"),
		FeedLabel:   s.translate(r, "idx.feed"),
		BridgeURL:   "/indexers/new?definition=torznab-generic",
		FeedURL:     "/indexers/new?definition=rss",
	}
	view.Empty = uiEmpty{
		Title:       s.translate(r, "idx.noSitesTitle"),
		Text:        s.translate(r, "idx.noSitesText", query),
		ActionLabel: s.translate(r, "idx.clearSearch"),
		ActionURL:   "/indexers/new",
	}
	entries, err := catalog.Builtins()
	if err != nil {
		return view
	}
	q := strings.ToLower(strings.TrimSpace(query))
	for _, entry := range entries {
		if entry.ID == "example-html" || strings.HasPrefix(entry.ID, "legacy-") {
			continue
		}
		def := entry.Definition
		choice := siteChoice{
			ID:         entry.ID,
			Name:       entry.ID,
			State:      entry.State,
			StateLabel: s.siteStateLabel(r, entry.State),
			Public:     !strings.EqualFold(def.Meta.Type, "private"),
			Generic:    isGenericDefinition(entry.ID),
			URLs:       def.Meta.URLs,
		}
		choice.Name = def.Meta.Name
		choice.Description = def.Meta.Description
		choice.Categories = s.definitionCategoryLabels(r, definitionCategoryIDs(def))
		if q != "" && !siteChoiceMatches(choice, q) {
			continue
		}
		if choice.Generic {
			view.Types = append(view.Types, choice)
		} else {
			view.Sites = append(view.Sites, choice)
		}
	}
	sort.SliceStable(view.Sites, func(i, j int) bool {
		left, right := siteStateRank(view.Sites[i].State), siteStateRank(view.Sites[j].State)
		if left != right {
			return left < right
		}
		return strings.ToLower(view.Sites[i].Name) < strings.ToLower(view.Sites[j].Name)
	})
	sort.SliceStable(view.Types, func(i, j int) bool {
		return strings.ToLower(view.Types[i].Name) < strings.ToLower(view.Types[j].Name)
	})
	view.NotFound = len(view.Sites) == 0 && len(view.Types) == 0
	return view
}

func (s *Server) siteStateLabel(r *http.Request, state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "verificata":
		return s.translate(r, "idx.siteState.verified")
	case "stabile":
		return s.translate(r, "idx.siteState.stable")
	case "degradata":
		return s.translate(r, "idx.siteState.problems")
	default:
		return s.translate(r, "idx.siteState.draft")
	}
}

func siteStateRank(state string) int {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "verificata":
		return 0
	case "stabile":
		return 1
	case "degradata":
		return 2
	default:
		return 3
	}
}

func siteChoiceMatches(choice siteChoice, q string) bool {
	if strings.Contains(strings.ToLower(choice.Name), q) ||
		strings.Contains(strings.ToLower(choice.ID), q) ||
		strings.Contains(strings.ToLower(choice.Description), q) {
		return true
	}
	for _, raw := range choice.URLs {
		if strings.Contains(strings.ToLower(raw), q) {
			return true
		}
	}
	for _, label := range choice.Categories {
		if strings.Contains(strings.ToLower(label), q) {
			return true
		}
	}
	return false
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

func (s *Server) categoryLabel(r *http.Request, id int) string {
	key := "category." + strconv.Itoa(id)
	lang := requestLanguage(r)
	label := i18n.T(lang, key)
	if label == key || strings.TrimSpace(label) == "" {
		return categories.Name(strconv.Itoa(id))
	}
	return label
}

// ------------------------------------------------------------- compact form

type compactFormView struct {
	base
	Choosing        bool
	Picker          *sitePickerView
	IsNew           bool
	Indexer         store.Indexer
	DefinitionID    string
	DefinitionName  string
	Derived         bool
	Name            string
	Protocol        string
	Slug            string
	Priority        int
	Tags            string
	Query           string
	DefaultCategory string
	UserAgent       string
	AllowPrivate    bool
	Enabled         bool
	URLValue        string
	URLPlaceholder  string
	URLSecret       bool
	URLChoices      []indexerSettingOption
	ShowURL         bool
	URLLabel        string
	IntervalSeconds int
	Categories      []categoryChoice
	CategoryHelp    string
	Help            string
	RequiredFields  []indexerSettingField
	AdvancedFields  []indexerSettingField
	Problems        []string
	Note            string
	StateWarning    string
	ChangeSiteURL   string
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
	}
	parsed, _, err := s.loadDefinitionForForm(r, defID)
	if err != nil {
		return compactFormView{}, err
	}
	data.DefinitionName = parsed.Meta.Name
	data.Derived = parsed.IsDerived()
	if current == nil && in == nil && data.IntervalSeconds == 0 {
		if suggested := parsed.Meta.Interval.Duration(); suggested >= time.Minute {
			data.IntervalSeconds = int(suggested / time.Second)
		}
	}
	if !data.Derived {
		switch s.definitionState(r, defID) {
		case "bozza", "degradata":
			data.StateWarning = s.translate(r, "idx.testingWarning")
		}
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
	data.UserAgent = value("user_agent")
	data.AllowPrivate = truthy(value("allow_private"))
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
						Value: raw, Label: raw, Selected: raw == urlValue,
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
		if setting.Required {
			data.RequiredFields = append(data.RequiredFields, field)
		} else {
			data.AdvancedFields = append(data.AdvancedFields, field)
		}
	}
	if !definitionHasURL {
		// Definitions that take the address from meta.urls need no URL field.
		data.URLValue = ""
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
			Checked: checked[value] || defaultChecked,
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
func (s *Server) redactedSecretURL(r *http.Request, indexerID int64, key string) string {
	if s.vault == nil {
		return ""
	}
	secret, err := s.store.GetSecret(r.Context(), indexerID, key)
	if err != nil {
		return ""
	}
	value, err := s.vault.Open(secret.Nonce, secret.Ciphertext)
	if err != nil {
		return ""
	}
	return feed.RedactURLForDisplay(value)
}

func (s *Server) settingField(r *http.Request, setting definition.Setting, value string, hasSecret, forceRequired bool) indexerSettingField {
	label := s.settingLabel(r, setting.Name, setting.Label)
	field := indexerSettingField{
		Name:      setting.Name,
		Label:     label,
		Help:      setting.Help,
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

func splitCategoryList(raw string) []string {
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
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
	}
	parsed, _, err := s.loadDefinitionForForm(r, defID)
	if err != nil {
		s.renderIndexerFormProblems(w, r, defID, nil, nil, []string{s.translate(r, "indexerForm.invalidDefinition", err.Error())})
		return
	}
	in, err := indexerInputFromForm(r, parsed)
	if err != nil {
		s.renderIndexerFormProblems(w, r, defID, nil, nil, []string{err.Error()})
		return
	}
	if err := s.ensureStoredDefinition(r, defID); err != nil {
		s.renderIndexerFormProblems(w, r, defID, nil, nil, []string{err.Error()})
		return
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
	parsed, _, err := s.loadDefinitionForForm(r, defID)
	if err != nil {
		s.renderCompactFormProblems(w, r, defID, &current, nil, []string{s.translate(r, "indexerForm.definitionInvalid", err.Error())})
		return
	}
	in, err := indexerInputFromForm(r, parsed)
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
	HasState          bool
	State             store.IndexerState
	Stats             detailStatView
	Categories        []string
	FilterRows        []indexerConfigEntry
	Public            []indexerConfigEntry
	SecretKeys        []string
	Releases          []store.Release
	Run               *indexer.RunStats
	RunItems          []engine.Item
	Test              *engine.TestResult
	Problem           *uiProblem
	HostRequests      []hostRequestView
	ApplicationCount  int
	NextStep          uiNextStep
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
	def := s.definitionCache(r)[in.DefinitionID]
	if def != nil {
		data.DefinitionName = def.Meta.Name
		data.Connector = def.Connector
		data.DefinitionVersion = def.Schema
		data.Categories = s.definitionCategoryLabels(r, definitionCategoryIDs(def))
	}
	if selected := splitCategoryList(publicConfigMap(in.ConfigJSON)["categories"]); len(selected) > 0 {
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
	} else if entry, ok := catalog.Get(in.DefinitionID); ok {
		data.Hash = entry.Hash
		data.HashShort = shortHash(entry.Hash)
		data.Origin = catalog.OriginBuiltin
	}
	if data.Connector == "" {
		if stored, err := s.store.GetDefinition(r.Context(), in.DefinitionID); err == nil {
			if parsed, err := definition.Parse([]byte(stored.Body)); err == nil {
				data.Connector = parsed.Connector
			}
		}
	}
	data.Badges = s.indexerBadges(r, in, def)
	if state, err := s.store.GetIndexerState(r.Context(), in.ID); err == nil {
		data.State = state
		data.HasState = true
	}
	history, _ := s.store.ListIndexerHistory(r.Context(), in.ID, 200)
	now := time.Now()
	for i := range history {
		item := history[i]
		if item.Kind == "run" && data.LastRun == nil {
			data.LastRun = &history[i]
		}
		if !item.StartedAt.Before(now.Add(-24 * time.Hour)) {
			switch item.Kind {
			case "search":
				data.Stats.Queries++
			case "grab":
				data.Stats.Grabs++
			}
		}
	}
	data.Status = s.indexerStatus(r, in, data.State, data.HasState, data.LastRun)
	data.LastError = strings.TrimSpace(data.State.LastError)
	if data.LastError == "" && data.LastRun != nil {
		data.LastError = strings.TrimSpace(data.LastRun.Error)
	}
	if data.Status.State == "problem" {
		data.Problem = s.problemFor(r, in, data.State, data.LastRun)
	}
	if data.LastRun != nil {
		settings, _ := s.store.LoadSettings(r.Context())
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
	data.Stats.AvgLatency = averageLatency(history)
	if secrets, err := s.store.ListSecrets(r.Context(), in.ID); err == nil {
		for _, secret := range secrets {
			data.SecretKeys = append(data.SecretKeys, secret.Key)
		}
	}
	for key, value := range indexerFilterValues(in) {
		data.FilterRows = append(data.FilterRows, indexerConfigEntry{
			Key: s.settingLabel(r, key, key), Value: value,
		})
	}
	sort.Slice(data.FilterRows, func(i, j int) bool { return data.FilterRows[i].Key < data.FilterRows[j].Key })
	if releases, _, err := s.store.SearchReleases(r.Context(), store.Filter{IndexerID: in.ID, Limit: 10, Sort: "seen"}); err == nil {
		data.Releases = releases
	}
	if s.polite != nil {
		host := configHost(in.ConfigJSON)
		if host == "" && def != nil && len(def.Meta.URLs) > 0 {
			if parsed, err := url.Parse(def.Meta.URLs[0]); err == nil {
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
	id, err := pathID(r)
	if err != nil {
		redirect(w, r, "/indexers", "", s.translate(r, "indexer.error.invalidID"))
		return
	}
	if err := s.svc.Delete(r.Context(), id); err != nil {
		redirect(w, r, "/indexers", "", s.indexerFriendlyError(r, err))
		return
	}
	redirect(w, r, "/indexers", s.translate(r, "indexer.deleted"), "")
}

func (s *Server) indexerToggle(w http.ResponseWriter, r *http.Request) {
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
	enabled := !current.Enabled
	if _, err := s.svc.Update(r.Context(), id, indexersvc.Input{Enabled: &enabled}); err != nil {
		redirect(w, r, "/indexers", "", s.indexerFriendlyError(r, err))
		return
	}
	if enabled {
		redirect(w, r, "/indexers", s.translate(r, "idx.enabledMessage", current.Name), "")
		return
	}
	redirect(w, r, "/indexers", s.translate(r, "idx.disabledMessage", current.Name), "")
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
	ids := make([]int64, 0)
	for _, raw := range r.Form["ids"] {
		if id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	action := strings.ToLower(strings.TrimSpace(r.FormValue("action")))
	if len(ids) == 0 {
		redirect(w, r, "/indexers", "", s.translate(r, "idx.bulkNone"))
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
	redirect(w, r, "/indexers", s.translate(r, "idx.bulkDone", count), "")
}

// ------------------------------------------------------------------- helpers
