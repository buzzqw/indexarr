package engine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// templatePattern matches one {{ ... }} expression. Templates are deliberately
// tiny: no logic, no arbitrary functions, so a definition can never execute
// code inside indexarr.
var templatePattern = regexp.MustCompile(`\{\{\s*(.*?)\s*\}\}`)

// queryVariableNames lists the normalized query variables available to
// templates. Optional values render as an empty string when unset.
var queryVariableNames = []string{
	"keywords", "keywords_raw", "season", "ep", "imdbid", "tvdbid", "tmdbid",
	"tvmazeid", "rid", "artist", "album", "author", "title", "year",
}

var queryEpisodeTag = regexp.MustCompile(`(?i)\bs\d{1,2}(?:[ ._-]?e\d{1,3})?\b|\b\d{1,2}x\d{1,3}\b`)

// templateValues is the data a definition's templates can read. settings is
// the effective map (defaults applied); declared records setting names so a
// reference to an undeclared setting is an error rather than an empty string.
type templateValues struct {
	settings         map[string]string
	declaredSettings map[string]struct{}
	query            map[string]string
	extracted        map[string]string
	categories       []string
	scalars          map[string]string
}

func newTemplateValues() *templateValues {
	return &templateValues{
		settings:         make(map[string]string),
		declaredSettings: make(map[string]struct{}),
		query:            make(map[string]string),
		extracted:        make(map[string]string),
		scalars:          make(map[string]string),
	}
}

// render expands every template expression in src. An unknown variable, an
// unknown filter or a malformed expression is an error so definitions fail
// loudly instead of silently sending broken requests.
func (v *templateValues) render(src string) (string, error) {
	if !strings.Contains(src, "{{") {
		return src, nil
	}

	var out strings.Builder
	last := 0
	matches := templatePattern.FindAllStringSubmatchIndex(src, -1)
	if strings.Count(src, "{{") != len(matches) || strings.Count(src, "}}") != len(matches) {
		return "", fmt.Errorf("template: unterminated expression in %q", src)
	}
	for _, match := range matches {
		if match[0] > last {
			out.WriteString(src[last:match[0]])
		}
		expr := strings.TrimSpace(src[match[2]:match[3]])
		value, err := v.evaluate(expr)
		if err != nil {
			return "", err
		}
		out.WriteString(value)
		last = match[1]
	}
	out.WriteString(src[last:])
	return out.String(), nil
}

// renderMap renders every value of a template map, preserving keys.
func (v *templateValues) renderMap(in map[string]string) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		rendered, err := v.render(value)
		if err != nil {
			return nil, fmt.Errorf("template %q: %w", key, err)
		}
		out[key] = rendered
	}
	return out, nil
}

func (v *templateValues) evaluate(expr string) (string, error) {
	if expr == "" {
		return "", fmt.Errorf("template: empty expression")
	}

	parts := strings.Split(expr, "|")
	name := strings.TrimSpace(parts[0])
	filter := ""
	arg := ""
	if len(parts) > 1 {
		if len(parts) != 2 {
			return "", fmt.Errorf("template: %q has more than one filter", expr)
		}
		filterExpr := strings.TrimSpace(parts[1])
		filter, arg = splitFilter(filterExpr)
	}

	if filter != "" {
		if name != "categories" {
			return "", fmt.Errorf("template: filter %q is not supported on %q", filter, name)
		}
		switch filter {
		case "join":
			return strings.Join(v.categories, arg), nil
		default:
			return "", fmt.Errorf("template: unknown filter %q", filter)
		}
	}

	if name == "categories" {
		return strings.Join(v.categories, ","), nil
	}

	if value, ok := v.scalars[name]; ok {
		return value, nil
	}

	if strings.HasPrefix(name, "settings.") {
		key := strings.TrimPrefix(name, "settings.")
		if _, declared := v.declaredSettings[key]; !declared {
			if _, exists := v.settings[key]; !exists {
				return "", fmt.Errorf("template: unknown setting %q", key)
			}
		}
		return v.settings[key], nil
	}

	if strings.HasPrefix(name, "query.") {
		key := strings.TrimPrefix(name, "query.")
		if !isQueryVariable(key) {
			return "", fmt.Errorf("template: unknown query variable %q", key)
		}
		return v.query[key], nil
	}

	if value, ok := v.extracted[name]; ok {
		return value, nil
	}
	return "", fmt.Errorf("template: unknown variable %q", name)
}

func isQueryVariable(name string) bool {
	for _, known := range queryVariableNames {
		if name == known {
			return true
		}
	}
	return false
}

// splitFilter parses "join(\",\")" into its name and unquoted argument.
func splitFilter(expr string) (string, string) {
	open := strings.IndexByte(expr, '(')
	if open < 0 {
		return expr, ""
	}
	close := strings.LastIndexByte(expr, ')')
	if close < open {
		return expr, ""
	}
	name := strings.TrimSpace(expr[:open])
	raw := strings.TrimSpace(expr[open+1 : close])
	return name, unquoteTemplateArg(raw)
}

func unquoteTemplateArg(raw string) string {
	if len(raw) >= 2 {
		if raw[0] == '"' && raw[len(raw)-1] == '"' {
			if value, err := strconv.Unquote(raw); err == nil {
				return value
			}
		}
		if raw[0] == '\'' && raw[len(raw)-1] == '\'' {
			return raw[1 : len(raw)-1]
		}
	}
	return raw
}

// queryTemplateMap builds the query.* variables for a SearchRequest.
// keywords is the effective text sent to the site: for a TV search it carries
// the episode marker in the format chosen by the definition. keywords_raw is
// always the original query, and season/ep stay available for definitions that
// prefer separate parameters.
func queryTemplateMap(req SearchRequest, keywordsFormat string) map[string]string {
	out := make(map[string]string, len(queryVariableNames))
	out["keywords"] = effectiveKeywords(req, keywordsFormat)
	out["keywords_raw"] = req.Query
	out["season"] = optionalInt(req.Season)
	out["ep"] = optionalInt(req.Episode)
	out["imdbid"] = req.IMDbID
	out["tvdbid"] = req.TVDbID
	out["tmdbid"] = req.TMDbID
	out["tvmazeid"] = req.TVMazeID
	out["rid"] = req.RID
	out["artist"] = req.Artist
	out["album"] = req.Album
	out["author"] = req.Author
	out["title"] = req.Title
	out["year"] = optionalInt(req.Year)
	return out
}

func effectiveKeywords(req SearchRequest, keywordsFormat string) string {
	raw := strings.TrimSpace(req.Query)
	if raw == "" {
		return raw
	}
	if req.Mode == ModeMovie && req.Year > 0 && !queryEpisodeTag.MatchString(raw) {
		year := strconv.Itoa(req.Year)
		found := false
		for _, field := range strings.Fields(raw) {
			if field == year {
				found = true
				break
			}
		}
		if !found {
			return strings.TrimSpace(raw + " " + year)
		}
		return raw
	}
	if req.Mode != ModeTV || req.Season <= 0 {
		return raw
	}
	// A query that already carries a season/episode marker is left untouched,
	// exactly like the fallback in Jackett/Prowlarr.
	if queryEpisodeTag.MatchString(raw) {
		return raw
	}
	switch strings.ToLower(strings.TrimSpace(keywordsFormat)) {
	case "separate":
		return raw
	case "1x05":
		if req.Episode > 0 {
			return fmt.Sprintf("%s %dx%02d", raw, req.Season, req.Episode)
		}
		return fmt.Sprintf("%s S%02d", raw, req.Season)
	default:
		tag := fmt.Sprintf("S%02d", req.Season)
		if req.Episode > 0 {
			tag += fmt.Sprintf("E%02d", req.Episode)
		}
		return strings.TrimSpace(raw + " " + tag)
	}
}

func optionalInt(value int) string {
	if value == 0 {
		return ""
	}
	return strconv.Itoa(value)
}

// siteCategoryIDs maps requested standard categories to the site ids declared
// in the definition, preserving order and removing duplicates.
func (c *DefinitionConnector) siteCategoryIDs(requested []int) []string {
	if len(requested) == 0 {
		return nil
	}
	byStandard := make(map[int][]string)
	for _, category := range c.def.Categories {
		byStandard[category.Standard] = append(byStandard[category.Standard], category.Site)
	}

	seen := make(map[string]struct{})
	var out []string
	for _, standard := range requested {
		for _, site := range byStandard[standard] {
			site = strings.TrimSpace(site)
			if site == "" {
				continue
			}
			if _, exists := seen[site]; exists {
				continue
			}
			seen[site] = struct{}{}
			out = append(out, site)
		}
	}
	return out
}
