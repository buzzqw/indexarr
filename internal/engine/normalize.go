package engine

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/definition"
)

const otherCategory = 8000

var (
	sizePattern   = regexp.MustCompile(`(?i)^([0-9][0-9.,\s]*)\s*(B|KB|MB|GB|TB|KIB|MIB|GIB|TIB)$`)
	digitsPattern = regexp.MustCompile(`-?[0-9]+`)
)

// applyFilters runs the definition's filters in declaration order.
func applyFilters(value string, filters []definition.Filter) (string, error) {
	for _, filter := range filters {
		switch filter.Name {
		case "trim":
			value = strings.TrimSpace(value)
		case "lower":
			value = strings.ToLower(value)
		case "upper":
			value = strings.ToUpper(value)
		case "replace":
			old, replacement, ok := strings.Cut(filter.Arg, ":")
			if !ok {
				return "", fmt.Errorf("filter replace: argument must be old:new (got %q)", filter.Arg)
			}
			value = strings.ReplaceAll(value, old, replacement)
		case "query_param":
			parsed, err := url.Parse(strings.TrimSpace(value))
			if err != nil {
				value = ""
				continue
			}
			value = parsed.Query().Get(filter.Arg)
		case "prefix":
			value = filter.Arg + value
		case "suffix":
			value = value + filter.Arg
		case "regex":
			compiled, err := regexp.Compile(filter.Arg)
			if err != nil {
				return "", fmt.Errorf("filter regex: invalid pattern: %w", err)
			}
			match := compiled.FindStringSubmatch(value)
			if len(match) < 2 {
				value = ""
				continue
			}
			value = match[1]
		default:
			return "", fmt.Errorf("filter %q: unknown filter", filter.Name)
		}
	}
	return value, nil
}

// parseSize accepts a byte count or a human size such as "1.5 GB" or
// "1,5 GiB". Unknown values become zero.
func parseSize(raw string) int64 {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\u00a0", " "))
	if raw == "" {
		return 0
	}

	if match := sizePattern.FindStringSubmatch(raw); match != nil {
		number := parseHumanNumber(match[1])
		if number < 0 || math.IsNaN(number) || math.IsInf(number, 0) {
			return 0
		}
		multiplier := map[string]float64{
			"B":   1,
			"KB":  1000,
			"MB":  1000 * 1000,
			"GB":  1000 * 1000 * 1000,
			"TB":  1000 * 1000 * 1000 * 1000,
			"KIB": 1024,
			"MIB": 1024 * 1024,
			"GIB": 1024 * 1024 * 1024,
			"TIB": 1024 * 1024 * 1024 * 1024,
		}[strings.ToUpper(match[2])]
		value := number * multiplier
		if value > float64(math.MaxInt64) {
			return 0
		}
		return int64(value)
	}

	cleaned := strings.NewReplacer(",", "", ".", "", " ", "", "\u00a0", "").Replace(raw)
	if _, err := strconv.ParseInt(cleaned, 10, 64); err != nil {
		return 0
	}
	value, _ := strconv.ParseInt(cleaned, 10, 64)
	return value
}

func parseHumanNumber(raw string) float64 {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), " ", "")
	hasComma := strings.Contains(raw, ",")
	hasDot := strings.Contains(raw, ".")
	switch {
	case hasComma && hasDot:
		raw = strings.ReplaceAll(raw, ",", "")
	case hasComma:
		raw = strings.ReplaceAll(raw, ",", ".")
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return value
}

// parseInt keeps the digits and tolerates thousands separators. Values like
// "N/A" or "—" become zero.
func parseInt(raw string) int64 {
	cleaned := strings.NewReplacer(",", "", ".", "", " ", "", "\u00a0", "").Replace(strings.TrimSpace(raw))
	match := digitsPattern.FindString(cleaned)
	if match == "" {
		return 0
	}
	value, err := strconv.ParseInt(match, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

var commonDateLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
	"02-01-2006 15:04:05",
	"02-01-2006 15:04",
	"02-01-2006",
	"02/01/2006 15:04:05",
	"02/01/2006 15:04",
	"02/01/2006",
}

// parseDate tries the definition layouts first, then the common layouts.
func parseDate(raw string, layouts []string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range append(append([]string(nil), layouts...), commonDateLayouts...) {
		if parsed, err := time.ParseInLocation(layout, raw, time.UTC); err == nil {
			return parsed
		}
	}
	if len(raw) == 10 || len(raw) == 13 {
		if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if len(raw) == 13 {
				unix /= 1000
			}
			return time.Unix(unix, 0).UTC()
		}
	}
	return time.Time{}
}

// mapCategories maps the first recognised site category to its standard id.
// Numeric values in the Torznab range are kept; anything else becomes 8000.
func mapCategories(def *definition.Definition, raw string) []int {
	for _, candidate := range strings.Split(raw, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if def != nil {
			for _, category := range def.Categories {
				if strings.TrimSpace(category.Site) == candidate {
					return []int{category.Standard}
				}
			}
		}
		if number, err := strconv.Atoi(candidate); err == nil && number >= 1000 && number <= 8999 {
			return []int{number}
		}
	}
	return []int{otherCategory}
}

// resolveURL turns a relative href into an absolute URL against the response
// URL, falling back to the definition base URL.
func resolveURL(raw string, bases ...*url.URL) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(raw), "magnet:") {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if parsed.IsAbs() {
		return parsed.String()
	}
	for _, base := range bases {
		if base != nil {
			return base.ResolveReference(parsed).String()
		}
	}
	return ""
}

var (
	hexInfoHashPattern    = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	base32InfoHashPattern = regexp.MustCompile(`^[A-Za-z2-7]{32}$`)
)

// normalizeInfoHash accepts a bare hash, a urn:btih: URI or a magnet link and
// returns the canonical hash, or "" when the value is not a BTIH hash.
func normalizeInfoHash(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(raw), "magnet:") {
		return infoHashFromMagnet(raw)
	}
	lower := strings.ToLower(raw)
	if index := strings.Index(lower, "urn:btih:"); index >= 0 {
		raw = raw[index+len("urn:btih:"):]
	}
	if cut := strings.IndexAny(raw, "&?#"); cut >= 0 {
		raw = raw[:cut]
	}
	raw = strings.TrimSpace(raw)
	switch {
	case hexInfoHashPattern.MatchString(raw):
		return strings.ToLower(raw)
	case base32InfoHashPattern.MatchString(raw):
		return strings.ToUpper(raw)
	default:
		return ""
	}
}

func infoHashFromMagnet(magnet string) string {
	lower := strings.ToLower(magnet)
	index := strings.Index(lower, "urn:btih:")
	if index < 0 {
		return ""
	}
	value := magnet[index+len("urn:btih:"):]
	if cut := strings.IndexAny(value, "&?#"); cut >= 0 {
		value = value[:cut]
	}
	return normalizeInfoHash(value)
}

func buildMagnet(infoHash, title string) string {
	magnet := "magnet:?xt=urn:btih:" + infoHash
	if title != "" {
		magnet += "&dn=" + url.QueryEscape(title)
	}
	return magnet
}

// normalizeRow turns one extracted row into an Item. ok is false when a
// required value is missing or the row has no title and no grab link.
func (c *DefinitionConnector) normalizeRow(raw map[string]string, responseURL *url.URL) (Item, bool, error) {
	fields := c.def.Search.Response.Fields

	for _, name := range c.def.Search.Response.Required {
		value, exists := raw[name]
		if !exists || strings.TrimSpace(value) == "" {
			return Item{}, false, nil
		}
		switch fields[name].Type {
		case "size":
			if parseSize(value) == 0 {
				return Item{}, false, nil
			}
		case "int":
			if parseInt(value) == 0 {
				return Item{}, false, nil
			}
		case "date":
			if parseDate(value, fields[name].Layouts).IsZero() {
				return Item{}, false, nil
			}
		case "url":
			if resolveURL(value, responseURL, c.baseURL) == "" {
				return Item{}, false, nil
			}
		}
	}

	title := strings.Join(strings.Fields(strings.TrimSpace(raw["title"])), " ")
	if title == "" {
		return Item{}, false, nil
	}

	item := Item{
		Protocol:   c.protocol,
		Title:      title,
		GUID:       strings.TrimSpace(raw["guid"]),
		DetailsURL: resolveURL(raw["details"], responseURL, c.baseURL),
		IMDbID:     strings.TrimSpace(raw["imdbid"]),
		TMDbID:     strings.TrimSpace(raw["tmdbid"]),
		TVDbID:     strings.TrimSpace(raw["tvdbid"]),
	}

	download := strings.TrimSpace(raw["download"])
	if strings.HasPrefix(strings.ToLower(download), "magnet:") {
		item.Magnet = download
		download = ""
	} else {
		item.DownloadURL = resolveURL(download, responseURL, c.baseURL)
	}

	if magnet := strings.TrimSpace(raw["magnet"]); magnet != "" && item.Magnet == "" {
		item.Magnet = magnet
	}

	item.InfoHash = normalizeInfoHash(raw["infohash"])
	if item.InfoHash == "" && item.Magnet != "" {
		item.InfoHash = infoHashFromMagnet(item.Magnet)
	}
	if item.InfoHash != "" && item.Magnet == "" && item.DownloadURL == "" {
		item.Magnet = buildMagnet(item.InfoHash, title)
	}

	item.Size = parseSize(raw["size"])
	item.Seeders = parseInt(raw["seeders"])
	item.Leechers = parseInt(raw["leechers"])

	dateRaw := raw["date"]
	layouts := fields["date"].Layouts
	if strings.TrimSpace(dateRaw) == "" {
		dateRaw = raw["published"]
		layouts = fields["published"].Layouts
	}
	item.Published = parseDate(dateRaw, layouts)

	categoryRaw := raw["category"]
	if strings.TrimSpace(categoryRaw) == "" {
		categoryRaw = raw["categories"]
	}
	if strings.TrimSpace(categoryRaw) == "" {
		item.Categories = []int{otherCategory}
	} else {
		item.Categories = mapCategories(c.def, categoryRaw)
	}

	if item.Magnet == "" && item.DownloadURL == "" && item.DetailsURL == "" {
		return Item{}, false, nil
	}
	return item, true, nil
}

// dedupeItems removes duplicates in one Search. Infohash wins, then GUID,
// then the download URL; items with none of them are kept as-is.
func dedupeItems(items []Item) []Item {
	seen := make(map[string]struct{}, len(items))
	out := make([]Item, 0, len(items))
	for _, item := range items {
		key := ""
		switch {
		case item.InfoHash != "":
			key = "hash:" + strings.ToLower(item.InfoHash)
		case item.GUID != "":
			key = "guid:" + item.GUID
		case item.DownloadURL != "":
			key = "url:" + item.DownloadURL
		}
		if key != "" {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
		}
		out = append(out, item)
	}
	return out
}
