// Package dedup implements the conservative identity rules used to merge the
// same release seen through several indexers. It never compares titles alone:
// every identifying feature of a release must agree, the size must match
// within a rounding tolerance, and dates must be compatible when both are real
// publication dates. When in doubt the caller keeps both rows.
package dedup

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/textutil"
)

// DefaultSizeTolerance is the relative rounding tolerance between the sizes of
// two copies of the same release. The acceptance example 9.32 GB vs
// 9,320,000,000 bytes passes; 9.3 GB vs 9.41 GB (1.18%) does not.
const DefaultSizeTolerance = 0.01

// DefaultDateWindow is the maximum distance between two real publication
// dates. A missing or synthetic date (the crawl time stored when the site did
// not provide one) never blocks a merge.
const DefaultDateWindow = 14 * 24 * time.Hour

// Options tunes Match. Zero values fall back to the defaults.
type Options struct {
	SizeTolerance float64
	DateWindow    time.Duration
}

func (o Options) tolerance() float64 {
	if o.SizeTolerance <= 0 {
		return DefaultSizeTolerance
	}
	return o.SizeTolerance
}

func (o Options) dateWindow() time.Duration {
	if o.DateWindow <= 0 {
		return DefaultDateWindow
	}
	return o.DateWindow
}

// Meta is the subset of a release identity the rules need.
type Meta struct {
	ID          int64
	Title       string
	Size        int64
	InfoHash    string
	PublishedAt time.Time
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

// Features are the identifying parts of a release title. Normalized keeps the
// full title; the other fields make an accidental "same title" match visible
// and are compared individually.
type Features struct {
	Normalized string
	Resolution string
	Source     string
	Codec      string
	Group      string
	Flags      string
	Languages  string
	Season     int
	Episode    int
	SeasonPack bool
	Year       int
}

var (
	resolutionPattern = regexp.MustCompile(`(?i)\b(2160p|1080p|720p|480p|4k|uhd)\b`)
	sourcePattern     = regexp.MustCompile(`(?i)\b(web[\s._-]?dl|webdl|webrip|web[\s._-]?rip|bluray|blu[\s._-]?ray|bdrip|brrip|remux|hdtv|dvdrip|web)\b`)
	codecPattern      = regexp.MustCompile(`(?i)\b(x264|h[\s._-]?264|avc|x265|h[\s._-]?265|hevc|av1|vp9|mpeg2)\b`)
	seasonEpisode     = regexp.MustCompile(`(?i)\bs(\d{1,2})(?:e(\d{1,3}))?\b`)
	crossEpisode      = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2,3})\b`)
	yearPattern       = regexp.MustCompile(`\b(19|20)\d{2}\b`)
	sceneGroup        = regexp.MustCompile(`(?i).*[-–]([a-z0-9][a-z0-9._-]{0,30})\s*$`)
	bracketGroup      = regexp.MustCompile(`(?i).*\[([a-z0-9][a-z0-9._ -]{0,30})\]\s*$`)
	trailingGroup     = regexp.MustCompile(`(?i)(?:^|\s)([a-z0-9][a-z0-9._-]{1,30})\s*$`)
	languagePattern   = regexp.MustCompile(`(?i)\b(ita|italian|eng|english|multi|sub[\s._-]?ita|ita[\s._-]?eng|eng[\s._-]?ita)\b`)
)

// The normalizers run on every title in every result page. Building a
// strings.Replacer is far more expensive than using it, so keep the three
// fixed tables at package level. normalizeCodec deliberately does not remove
// the hyphen: that matches the historical output for "h-264" and friends.
var (
	languageCleaner = strings.NewReplacer(" ", "", "_", "", ".", "", "-", "")
	sourceCleaner   = strings.NewReplacer(" ", "", "_", "", ".", "", "-", "")
	codecCleaner    = strings.NewReplacer(" ", "", "_", "", ".", "")
)

var nonGroups = map[string]bool{
	"x264": true, "x265": true, "h264": true, "h265": true, "h.264": true,
	"h.265": true, "hevc": true, "avc": true, "av1": true, "vp9": true,
	"web": true, "webdl": true, "webrip": true, "web-dl": true,
	"bluray": true, "bdrip": true, "brrip": true, "remux": true, "hdtv": true,
	"480p": true, "720p": true, "1080p": true, "2160p": true, "4k": true,
	"proper": true, "repack": true, "rerip": true, "internal": true,
	"mkv": true, "mp4": true, "avi": true, "torrent": true,
}

// Extract parses the identifying features of a release title.
func Extract(title string) Features {
	lower := strings.ToLower(strings.TrimSpace(title))
	trimmed := trimMediaExtension(lower)
	out := Features{Normalized: textutil.NormalizeTitle(trimmed)}

	if match := resolutionPattern.FindString(trimmed); match != "" {
		switch strings.ToLower(match) {
		case "4k", "uhd":
			out.Resolution = "2160p"
		default:
			out.Resolution = strings.ToLower(match)
		}
	}
	if match := sourcePattern.FindString(trimmed); match != "" {
		out.Source = normalizeSource(match)
	}
	if match := codecPattern.FindString(trimmed); match != "" {
		out.Codec = normalizeCodec(match)
	}
	out.Group = extractGroup(trimmed)
	out.Flags = extractFlags(trimmed)
	out.Languages = extractLanguages(trimmed)
	if match := seasonEpisode.FindStringSubmatch(trimmed); match != nil {
		out.Season = atoi(match[1])
		out.Episode = atoi(match[2])
		out.SeasonPack = out.Season > 0 && out.Episode == 0
	} else if match := crossEpisode.FindStringSubmatch(trimmed); match != nil {
		out.Season = atoi(match[1])
		out.Episode = atoi(match[2])
	}
	if match := yearPattern.FindString(trimmed); match != "" {
		out.Year = atoi(match)
	}
	return out
}

// Match reports whether two release copies are the same release under the
// conservative rules.
func Match(a, b Meta, opts Options) bool {
	if a.InfoHash != "" && b.InfoHash != "" &&
		!strings.EqualFold(a.InfoHash, b.InfoHash) {
		// Two different torrents are never the same release, even when a
		// private tracker reused the title and size. This is the rule that
		// protects PROPER/REPACK and different group uploads that happen to
		// share a display title.
		return false
	}
	if a.Size <= 0 || b.Size <= 0 {
		return false
	}
	fa, fb := Extract(a.Title), Extract(b.Title)
	if fa.Normalized == "" || fa.Normalized != fb.Normalized {
		return false
	}
	if !featuresEqual(fa, fb) {
		return false
	}
	largest := math.Max(math.Abs(float64(a.Size)), math.Abs(float64(b.Size)))
	if largest <= 0 || math.Abs(float64(a.Size-b.Size))/largest > opts.tolerance() {
		return false
	}
	if !datesCompatible(a, b, opts.dateWindow()) {
		return false
	}
	return true
}

func featuresEqual(a, b Features) bool {
	return a.Resolution == b.Resolution &&
		a.Source == b.Source &&
		a.Codec == b.Codec &&
		a.Group == b.Group &&
		a.Flags == b.Flags &&
		a.Languages == b.Languages &&
		a.Season == b.Season &&
		a.Episode == b.Episode &&
		a.SeasonPack == b.SeasonPack &&
		a.Year == b.Year
}

// datesCompatible compares real publication dates. When either side has no
// date, or the date is just the moment indexarr first saw the row, the field
// carries no identity information and does not block the merge.
func datesCompatible(a, b Meta, window time.Duration) bool {
	if a.PublishedAt.IsZero() || b.PublishedAt.IsZero() {
		return true
	}
	if syntheticDate(a) || syntheticDate(b) {
		return true
	}
	return math.Abs(a.PublishedAt.Sub(b.PublishedAt).Hours()) <= window.Hours()
}

// syntheticDate reports a published_at that is effectively the crawl clock:
// UpsertRelease starts from FirstSeenAt and refreshes the value on every
// sighting when the site does not provide a publication date.
func syntheticDate(m Meta) bool {
	published := m.PublishedAt
	if published.IsZero() {
		return true
	}
	if !m.FirstSeenAt.IsZero() && absDuration(published.Sub(m.FirstSeenAt)) <= 48*time.Hour {
		return true
	}
	if !m.LastSeenAt.IsZero() && absDuration(published.Sub(m.LastSeenAt)) <= 48*time.Hour {
		return true
	}
	return false
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func extractGroup(raw string) string {
	if match := sceneGroup.FindStringSubmatch(raw); match != nil {
		candidate := strings.Trim(match[1], "._- ")
		if candidate != "" && !strings.Contains(candidate, " ") &&
			!nonGroups[candidate] && !isResolution(candidate) {
			return strings.ToLower(candidate)
		}
	}
	if match := bracketGroup.FindStringSubmatch(raw); match != nil {
		candidate := strings.Trim(match[1], "._- ")
		if candidate != "" && !nonGroups[candidate] && !isResolution(candidate) {
			return strings.ToLower(candidate)
		}
	}
	// Sites that do not use a hyphen or brackets still end the title with the
	// group name ("... H265 NTb"). The last token is accepted only when it is
	// not a known technical tag.
	if match := trailingGroup.FindStringSubmatch(raw); match != nil {
		candidate := strings.Trim(match[1], "._- ")
		if candidate != "" && !nonGroups[candidate] && !isResolution(candidate) &&
			!strings.Contains(candidate, "ddp") && !strings.Contains(candidate, "atmos") {
			return strings.ToLower(candidate)
		}
	}
	return ""
}

func extractFlags(raw string) string {
	var flags []string
	if strings.Contains(raw, "proper") {
		flags = append(flags, "proper")
	}
	if strings.Contains(raw, "repack") {
		flags = append(flags, "repack")
	}
	if strings.Contains(raw, "rerip") {
		flags = append(flags, "rerip")
	}
	sort.Strings(flags)
	return strings.Join(flags, ",")
}

func extractLanguages(raw string) string {
	matches := languagePattern.FindAllString(raw, -1)
	if len(matches) == 0 {
		return ""
	}
	set := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		set[normalizeLanguage(match)] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func normalizeLanguage(value string) string {
	value = strings.ToLower(value)
	value = languageCleaner.Replace(value)
	switch value {
	case "italian":
		return "ita"
	case "english":
		return "eng"
	}
	return value
}

func normalizeSource(value string) string {
	value = strings.ToLower(value)
	value = sourceCleaner.Replace(value)
	switch value {
	case "webdl":
		return "webdl"
	case "webrip":
		return "webrip"
	case "bluray":
		return "bluray"
	}
	return value
}

func normalizeCodec(value string) string {
	value = strings.ToLower(value)
	value = codecCleaner.Replace(value)
	switch value {
	case "x264", "h264", "avc":
		return "h264"
	case "x265", "h265", "hevc":
		return "h265"
	}
	return value
}

func trimMediaExtension(raw string) string {
	for _, extension := range []string{".torrent", ".mkv", ".mp4", ".avi", ".m4v"} {
		if strings.HasSuffix(raw, extension) {
			raw = strings.TrimSpace(strings.TrimSuffix(raw, extension))
			break
		}
	}
	// Sites truncate long titles with "[..." or an ellipsis. The marker is not
	// part of the group name.
	if index := strings.LastIndex(raw, "["); index >= 0 && !strings.Contains(raw[index:], "]") {
		raw = strings.TrimSpace(raw[:index])
	}
	return strings.TrimRight(raw, ".… ")
}

func isResolution(value string) bool {
	switch strings.ToLower(value) {
	case "480p", "720p", "1080p", "2160p", "4k", "uhd":
		return true
	}
	return false
}

func atoi(raw string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(raw))
	return value
}
