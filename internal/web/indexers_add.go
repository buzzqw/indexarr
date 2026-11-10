package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/categories"
	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/smartfeed"
	"github.com/buzzqw/indexarr/internal/store"
)

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
// list in https://github.com/buzzqw/indexarr/wiki/UI-Rules §3, not the order of the definition file.
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
	// The CSRF middleware parses the body only when the token is not already in
	// the query string, so a POST with ?csrf=… would reach this handler with an
	// empty r.Form: the chosen sites would be lost and the page would answer
	// "select at least one site".
	if err := r.ParseForm(); err != nil {
		redirect(w, r, "/indexers/new", "", s.translate(r, "idx.catalog.invalid"))
		return
	}
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
