package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/catalog"
	"github.com/buzzqw/indexarr/internal/definition"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/i18n"
	"github.com/buzzqw/indexarr/internal/indexersvc"
	"github.com/buzzqw/indexarr/internal/smartfeed"
	"github.com/buzzqw/indexarr/internal/store"
)

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
	CatalogSource             bool
	LiveOnMiss                bool
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
	RequestTimeoutSeconds     int
	IntervalChoices           []indexerSettingOption
	FrequencyLabel            string
	Categories                []categoryChoice
	CategoryHelp              string
	Help                      string
	RequiredFields            []indexerSettingField
	AccessFields              []indexerSettingField
	AccessMethods             []accessMethodView
	IsPrivate                 bool
	CanImportCookie           bool
	CookieAlternative         bool
	LoginVariantID            string
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
		data.RequestTimeoutSeconds = current.RequestTimeoutSeconds
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
		if in.RequestTimeoutSeconds != nil {
			data.RequestTimeoutSeconds = *in.RequestTimeoutSeconds
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
	data.CatalogSource = parsed.HasCatalog()
	data.CanImportCookie = definitionHasCookie(parsed)
	data.CookieAlternative = parsed.CookieAlternative()
	if strings.EqualFold(strings.TrimSpace(parsed.Meta.ID), "corsaro") {
		data.LoginVariantID = "corsaro-account"
	}
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
	isPrivate := strings.EqualFold(strings.TrimSpace(parsed.Meta.Type), "private")
	// A restricted source may have optional access fields too: IPTorrents
	// keeps the session cookie optional because it can be imported from the
	// browser. They still belong in the visible Access section, not in
	// Advanced, otherwise the form says "no credentials" for a source that
	// cannot work without them.
	isRestricted := isPrivate || strings.EqualFold(strings.TrimSpace(parsed.Meta.Type), "semi-private")

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
	data.LiveOnMiss = truthy(value("live_on_miss"))
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
		if parsed.CookieAlternative() {
			// Username and password or the browser cookie: either one is
			// enough, so neither carries the asterisk on its own.
			if parsed.CookieCovers(name) {
				field.Required = false
			}
			if setting.Alternative {
				field.Help = s.translate(r, "setting.cookie.alternativeHelp")
			}
		}
		switch {
		case isAccessSetting(name) && (setting.Required || isRestricted):
			// Credentials are the first thing the user must enter: keep them
			// at the top of the form instead of mixing them with the filters.
			// Required fields stay required; optional ones (an imported
			// session cookie) are shown without the asterisk.
			data.AccessFields = append(data.AccessFields, field)
		case setting.Required:
			data.RequiredFields = append(data.RequiredFields, field)
		default:
			data.AdvancedFields = append(data.AdvancedFields, field)
		}
	}
	data.IsPrivate = isPrivate
	sortAccessFields(data.AccessFields)
	data.AccessMethods = s.accessMethods(r, parsed)
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
	in.AllowPendingCookie = r.FormValue("import_cookie") != ""
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
	if r.FormValue("import_cookie") != "" {
		redirect(w, r, "/indexers/"+strconv.FormatInt(created.ID, 10)+"/cookie", s.translate(r, "indexer.created"), "")
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
	in.AllowPendingCookie = r.FormValue("import_cookie") != ""
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
	if r.FormValue("import_cookie") != "" {
		redirect(w, r, "/indexers/"+strconv.FormatInt(id, 10)+"/cookie", s.translate(r, "indexer.saved"), "")
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
