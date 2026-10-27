package definition

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ValidationError reports every problem found in a definition.
type ValidationError struct {
	Problems []string
}

// Error implements error.
func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return "definition: " + e.Problems[0]
	}
	return "definition: " + strings.Join(e.Problems, "; ")
}

var (
	slugPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	windowsAbsPathRE = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
)

var (
	allowedProtocols = map[string]bool{
		"torrent": true,
		"usenet":  true,
		"ed2k":    true,
	}
	allowedTypes = map[string]bool{
		"public":       true,
		"semi-private": true,
		"private":      true,
	}
	allowedAuthStrategies = map[string]bool{
		"none":    true,
		"api_key": true,
		"basic":   true,
		"form":    true,
		"cookie":  true,
		"token":   true,
	}
	allowedResponseTypes = map[string]bool{
		"html": true,
		"json": true,
		"xml":  true,
	}
	allowedPaginationTypes = map[string]bool{
		"none":   true,
		"page":   true,
		"offset": true,
		"cursor": true,
	}
	allowedFieldTypes = map[string]bool{
		"text":     true,
		"int":      true,
		"size":     true,
		"date":     true,
		"url":      true,
		"category": true,
	}
	allowedResolveOn = map[string]bool{
		"search": true,
		"grab":   true,
	}
	allowedDownloadSelect = map[string]bool{
		"":               true,
		"season_episode": true,
	}
	allowedDownloadExpand = map[string]bool{
		"":     true,
		"file": true,
	}
	allowedSessionModes = map[string]bool{
		"":             true,
		"flaresolverr": true,
	}
	allowedFilterNames = map[string]bool{
		"trim":        true,
		"lower":       true,
		"upper":       true,
		"replace":     true,
		"query_param": true,
		"prefix":      true,
		"suffix":      true,
		"regex":       true,
		"base64":      true,
	}
	allowedCapabilityParams = map[string]bool{
		"q":        true,
		"season":   true,
		"ep":       true,
		"imdbid":   true,
		"tvdbid":   true,
		"tmdbid":   true,
		"tvmazeid": true,
		"rid":      true,
		"artist":   true,
		"album":    true,
		"author":   true,
		"title":    true,
		"year":     true,
		"genre":    true,
	}
)

// Validate checks a definition against the v1 rules and returns a
// ValidationError containing every problem found, if any.
func (d *Definition) Validate() error {
	if d == nil {
		return &ValidationError{Problems: []string{"definition: nil"}}
	}

	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if d.Schema != CurrentSchema {
		add("schema: must be %d (got %d)", CurrentSchema, d.Schema)
	}

	if d.Meta.ID == "" {
		add("meta.id: is required")
	} else if !slugPattern.MatchString(d.Meta.ID) {
		add("meta.id: must be a slug matching %s", slugPattern.String())
	}
	if strings.TrimSpace(d.Meta.Name) == "" {
		add("meta.name: is required")
	}
	if len(d.Meta.URLs) == 0 {
		add("meta.urls: at least one URL is required")
	}
	for i, raw := range d.Meta.URLs {
		if !isHTTPURL(raw) {
			add("meta.urls[%d]: must be an absolute http or https URL", i)
		}
	}
	if !allowedProtocols[d.Meta.Protocol] {
		add("meta.protocol: must be one of torrent, usenet, ed2k")
	}
	if !allowedTypes[d.Meta.Type] {
		add("meta.type: must be one of public, semi-private, private")
	}
	if d.Meta.DerivedFrom != "" && !slugPattern.MatchString(d.Meta.DerivedFrom) {
		add("meta.derived_from: must be a slug matching %s", slugPattern.String())
	}
	switch d.Meta.Mirror {
	case "", "official", "unofficial":
	default:
		add("meta.mirror: must be empty, official or unofficial")
	}
	if d.Meta.InsecureTLS && !strings.EqualFold(strings.TrimSpace(d.Meta.Type), "public") {
		add("meta.insecure_tls: is only allowed for a public definition")
	}
	if interval := d.Meta.Interval.Duration(); interval != 0 && interval < time.Minute {
		add("meta.interval: must be empty or at least 60s")
	}

	seenSettings := make(map[string]int, len(d.Settings))
	for i, setting := range d.Settings {
		if setting.Name == "" {
			add("settings[%d].name: is required", i)
			continue
		}
		if !slugPattern.MatchString(setting.Name) {
			add("settings[%d].name: must be a slug matching %s", i, slugPattern.String())
		}
		if first, ok := seenSettings[setting.Name]; ok {
			add("settings[%d].name: duplicate of settings[%d].name (%q)", i, first, setting.Name)
		} else {
			seenSettings[setting.Name] = i
		}
	}

	for i, category := range d.Categories {
		if category.Standard < 1000 || category.Standard > 8999 {
			add("categories[%d].standard: must be between 1000 and 8999 (got %d)", i, category.Standard)
		}
	}

	capabilityLists := []struct {
		field  string
		params []string
	}{
		{"search", d.Capabilities.Search},
		{"tv-search", d.Capabilities.TVSearch},
		{"movie-search", d.Capabilities.MovieSearch},
		{"music-search", d.Capabilities.MusicSearch},
		{"book-search", d.Capabilities.BookSearch},
	}
	for _, list := range capabilityLists {
		for i, param := range list.params {
			if !allowedCapabilityParams[param] {
				add("capabilities.%s[%d]: unknown parameter %q", list.field, i, param)
			}
		}
	}

	if d.Auth != nil {
		validateAuth(d.Auth, add)
	}
	if d.Session != nil {
		validateSession(d.Session, add)
	}

	searchConfigured := !reflect.DeepEqual(d.Search, Search{})
	if d.Connector == "" || searchConfigured {
		validateSearch(d.Search, add)
	}
	if d.Latest != nil {
		validateLatest(d.Latest, d.Search, add)
	}

	if d.Download != nil {
		validateDownload(d.Download, d, add)
	}

	for i, test := range d.Tests {
		if strings.TrimSpace(test.Fixture) == "" {
			add("tests[%d].fixture: is required", i)
		} else if isAbsoluteFixture(test.Fixture) || hasParentSegment(test.Fixture) {
			add("tests[%d].fixture: must be a relative path without %q", i, "..")
		}
		if test.Expect.MinRows < 0 {
			add("tests[%d].expect.min_rows: must be >= 0", i)
		}
	}

	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

func validateAuth(auth *Auth, add func(string, ...any)) {
	strategy := auth.Strategy
	if strategy == "" {
		strategy = "none"
	}
	if !allowedAuthStrategies[strategy] {
		add("auth.strategy: must be one of none, api_key, basic, form, cookie, token")
	}

	for i, step := range auth.Steps {
		hasGet := step.Get != ""
		hasPost := step.Post != ""
		switch {
		case hasGet && hasPost:
			add("auth.steps[%d]: get and post are mutually exclusive", i)
		case !hasGet && !hasPost:
			add("auth.steps[%d]: must have get or post", i)
		}
	}

	for i, condition := range auth.Failure {
		if condition.Selector == "" && condition.Status == 0 {
			add("auth.failure[%d]: needs selector or status", i)
		}
	}

	if auth.LoggedIn != nil && auth.LoggedIn.Selector == "" {
		// A status alone cannot tell a logged-out page: the forum answers 200
		// either way.
		add("auth.logged_in: needs a selector")
	}

	if auth.RefreshAfter < 0 {
		add("auth.refresh_after: must not be negative")
	}
}

func validateSession(session *Session, add func(string, ...any)) {
	if !allowedSessionModes[session.Mode] {
		add("session.mode: must be empty or flaresolverr")
	}
	if endpoint := strings.TrimSpace(session.Endpoint); endpoint != "" &&
		!strings.Contains(endpoint, "{{") && !isHTTPURL(endpoint) {
		add("session.endpoint: must be an http or https URL or a template")
	}
	if prefix := strings.TrimSpace(session.NamePrefix); prefix != "" {
		if !slugPattern.MatchString(prefix) {
			add("session.name_prefix: must be a slug matching %s", slugPattern.String())
		}
	}
	if session.MaxAge < 0 {
		add("session.max_age: must not be negative")
	}
	if session.IdleTimeout < 0 {
		add("session.idle_timeout: must not be negative")
	}
	if session.Timeout < 0 {
		add("session.timeout: must not be negative")
	}
}

func validateSearch(search Search, add func(string, ...any)) {
	if search.Preflight != nil {
		pre := search.Preflight
		if pre.Get != "" && pre.Post != "" {
			add("search.preflight: get and post are mutually exclusive")
		} else if pre.Get == "" && pre.Post == "" {
			add("search.preflight: must have get or post")
		}
		if len(pre.Extract) == 0 {
			add("search.preflight.extract: at least one value is required")
		}
		for name, extractor := range pre.Extract {
			if strings.TrimSpace(name) == "" {
				add("search.preflight.extract: name must not be empty")
			}
			if extractor.Selector == "" && extractor.JSON == "" && extractor.Regex == "" {
				add("search.preflight.extract.%s: needs selector, json or regex", name)
			}
			if extractor.Regex != "" {
				if _, err := regexp.Compile(extractor.Regex); err != nil {
					add("search.preflight.extract.%s: invalid regex", name)
				}
			}
		}
	}
	if search.Index == nil {
		if search.Request.Get != "" && search.Request.Post != "" {
			add("search.request: get and post are mutually exclusive")
		} else if search.Request.Get == "" && search.Request.Post == "" {
			add("search.request: must have get or post")
		}
		validateResponse(search.Response, "search.response", add)
	} else {
		validateSearchIndex(*search.Index, add)
		// The normal request/response stays as an optional fallback.
		if search.Request.Get != "" || search.Request.Post != "" || len(search.Response.Fields) > 0 {
			if search.Request.Get != "" && search.Request.Post != "" {
				add("search.request: get and post are mutually exclusive")
			}
			validateResponse(search.Response, "search.response", add)
		}
	}

	if search.Pagination.Type != "" && !allowedPaginationTypes[search.Pagination.Type] {
		add("search.pagination.type: must be one of none, page, offset, cursor")
	}

	switch strings.ToLower(strings.TrimSpace(search.KeywordsFormat)) {
	case "", "sxxeyy", "1x05", "separate":
	default:
		add("search.keywords_format: must be one of sxxeyy, 1x05, separate")
	}
}

func validateSearchIndex(index SearchIndex, add func(string, ...any)) {
	if len(index.Pages) == 0 {
		add("search.index.pages: at least one page is required")
	}
	letters := make(map[string]int, len(index.Pages))
	for i, page := range index.Pages {
		if strings.TrimSpace(page.Get) == "" {
			add("search.index.pages[%d].get: is required", i)
		}
		letter := strings.ToUpper(strings.TrimSpace(page.Letter))
		if letter != "" && letter != "#" && (len(letter) != 1 || letter < "A" || letter > "Z") {
			add("search.index.pages[%d].letter: must be empty, # or A-Z", i)
		}
		if letter != "" {
			if first, ok := letters[letter]; ok {
				add("search.index.pages[%d].letter: duplicate of pages[%d] (%s)", i, first, letter)
			} else {
				letters[letter] = i
			}
		}
		if page.Standard != 0 && (page.Standard < 1000 || page.Standard > 8999) {
			add("search.index.pages[%d].standard: must be between 1000 and 8999", i)
		}
	}
	if index.MaxTopics < 0 || index.MaxTopics > 20 {
		add("search.index.max_topics: must be between 1 and 20 (0 uses the default 5)")
	}
	validateResponse(index.Response, "search.index.response", add)
}

// validateResponse checks the row/field description shared by search and the
// optional latest list.
func validateResponse(response Response, prefix string, add func(string, ...any)) {
	switch response.Type {
	case "html", "json", "xml":
	default:
		add("%s.type: must be one of html, json, xml", prefix)
	}

	fieldNames := make([]string, 0, len(response.Fields))
	for name := range response.Fields {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)
	for _, name := range fieldNames {
		field := response.Fields[name]
		if field.Type != "" && !allowedFieldTypes[field.Type] {
			add("%s.fields.%s.type: must be one of text, int, size, date, url, category", prefix, name)
		}
	}

	for i, name := range response.Required {
		if name == "" {
			add("%s.required[%d]: field name must not be empty", prefix, i)
			continue
		}
		if _, ok := response.Fields[name]; !ok {
			add("%s.required[%d]: field %q is not defined in %s.fields", prefix, i, name, prefix)
		}
	}
}

// validateLatest checks the optional list of new releases of a search-only
// site. The request may be empty only when the definition has no search either
// (a pure connector definition); in practice a latest page always names its URL.
func validateLatest(latest *Latest, search Search, add func(string, ...any)) {
	if latest.Request.Get != "" && latest.Request.Post != "" {
		add("latest.request: get and post are mutually exclusive")
	} else if latest.Request.Get == "" && latest.Request.Post == "" {
		add("latest.request: must have get or post")
	}
	switch latest.Pagination.Type {
	case "", "none", "page", "offset":
	default:
		add("latest.pagination.type: must be one of none, page, offset")
	}
	if stop := strings.TrimSpace(latest.Pagination.StopWhen); stop != "" && stop != "empty_rows" {
		add("latest.pagination.stop_when: must be empty or empty_rows")
	}
	seen := make(map[int]int, len(latest.Categories))
	for i, category := range latest.Categories {
		if category.Standard < 1000 || category.Standard > 8999 {
			add("latest.categories[%d].standard: must be between 1000 and 8999 (got %d)", i, category.Standard)
		}
		if first, ok := seen[category.Standard]; ok {
			add("latest.categories[%d].standard: duplicate of latest.categories[%d] (%d)", i, first, category.Standard)
		} else {
			seen[category.Standard] = i
		}
		if strings.TrimSpace(category.Site) == "" {
			add("latest.categories[%d].site: is required", i)
		}
	}
	if latest.Response != nil {
		validateResponse(*latest.Response, "latest.response", add)
		return
	}
	if len(search.Response.Fields) == 0 {
		add("latest.response: is required when search.response has no fields")
	}
}

func validateDownload(download *Download, d *Definition, add func(string, ...any)) {
	resolveOn := strings.TrimSpace(download.ResolveOn)
	if resolveOn == "" {
		resolveOn = "grab"
	}
	if !allowedResolveOn[resolveOn] {
		add("download.resolve_on: must be one of search, grab")
	}
	if !allowedDownloadSelect[strings.TrimSpace(download.Select)] {
		add("download.select: must be empty or season_episode")
	}
	expand := strings.TrimSpace(download.Expand)
	if !allowedDownloadExpand[expand] {
		add("download.expand: must be empty or file")
	}
	if expand != "" && resolveOn != "search" {
		add("download.expand: requires resolve_on search")
	}
	if download.Before != nil && resolveOn != "grab" {
		add("download.before: requires resolve_on grab (a thank must never run during a search)")
	}

	if download.MaxConcurrent < 0 || download.MaxConcurrent > 8 {
		add("download.max_concurrent: must be between 1 and 8 (0 uses the default 2)")
	}

	from := strings.TrimSpace(download.From)
	if from == "" {
		from = "details"
	}
	if _, ok := d.Search.Response.Fields[from]; !ok {
		// An index-only search has no response fields: the index provides the
		// details URL directly.
		if !(d.Search.Index != nil && from == "details") {
			add("download.from: field %q is not declared in search.response.fields", from)
		}
	}

	if download.Title != nil {
		validateDownloadField("download.title", *download.Title, add)
	}
	if len(download.Extract) == 0 {
		add("download.extract: at least one target is required")
	}
	for i, target := range download.Extract {
		set := 0
		if target.Magnet != nil {
			set++
		}
		if target.Torrent != nil {
			set++
		}
		if target.Ed2k != nil {
			set++
		}
		switch {
		case set == 0:
			add("download.extract[%d]: must set magnet, torrent or ed2k", i)
		case set > 1:
			add("download.extract[%d]: magnet, torrent and ed2k are mutually exclusive", i)
		case target.Magnet != nil:
			validateDownloadField(fmt.Sprintf("download.extract[%d].magnet", i), *target.Magnet, add)
		case target.Torrent != nil:
			validateDownloadField(fmt.Sprintf("download.extract[%d].torrent", i), *target.Torrent, add)
		default:
			validateDownloadField(fmt.Sprintf("download.extract[%d].ed2k", i), *target.Ed2k, add)
		}
	}
	if download.Before != nil {
		validateDownloadBefore(download.Before, add)
	}
}

func validateDownloadBefore(before *DownloadBefore, add func(string, ...any)) {
	if before.Wait < 0 {
		add("download.before.wait: must not be negative")
	}
	for name, extractor := range before.Extract {
		if strings.TrimSpace(name) == "" {
			add("download.before.extract: name must not be empty")
		}
		if extractor.Selector == "" && extractor.JSON == "" && extractor.Regex == "" {
			add("download.before.extract.%s: needs selector, json or regex", name)
		}
		if extractor.Regex != "" {
			if _, err := regexp.Compile(extractor.Regex); err != nil {
				add("download.before.extract.%s: invalid regex", name)
			}
		}
	}
	action := before.Action
	if action.Get != "" && action.Post != "" {
		add("download.before.action: get and post are mutually exclusive")
	} else if action.Get == "" && action.Post == "" {
		add("download.before.action: must have get or post")
	}
	for name, value := range action.Headers {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(value) == "" {
			add("download.before.action.headers: name and value must not be empty")
		}
	}
}

func validateDownloadField(prefix string, field Field, add func(string, ...any)) {
	if !field.Body && !field.Self && len(field.Selector) == 0 && strings.TrimSpace(field.Path) == "" {
		add("%s: needs selector, path, body or self", prefix)
	}
	for i, selector := range field.Selector {
		if strings.TrimSpace(selector) == "" {
			add("%s.selector[%d]: must not be empty", prefix, i)
		}
	}
	if field.Type != "" && !allowedFieldTypes[field.Type] {
		add("%s.type: must be one of text, int, size, date, url, category", prefix)
	}
	for i, filter := range field.Filters {
		switch filter.Name {
		case "replace":
			if _, _, ok := strings.Cut(filter.Arg, ":"); !ok {
				add("%s.filters[%d]: replace argument must be old:new", prefix, i)
			}
		case "query_param":
			if strings.TrimSpace(filter.Arg) == "" {
				add("%s.filters[%d]: query_param needs a parameter name", prefix, i)
			}
		case "regex":
			if _, err := regexp.Compile(filter.Arg); err != nil {
				add("%s.filters[%d]: invalid regex", prefix, i)
			}
		}
		if !allowedFilterNames[filter.Name] {
			add("%s.filters[%d]: unknown filter %q", prefix, i, filter.Name)
		}
	}
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != ""
}

func isAbsoluteFixture(fixture string) bool {
	return path.IsAbs(fixture) ||
		filepath.IsAbs(fixture) ||
		windowsAbsPathRE.MatchString(fixture) ||
		strings.HasPrefix(fixture, `\`) ||
		strings.HasPrefix(fixture, "~")
}

func hasParentSegment(fixture string) bool {
	parts := strings.FieldsFunc(fixture, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	for _, part := range parts {
		if part == ".." {
			return true
		}
	}
	return false
}
