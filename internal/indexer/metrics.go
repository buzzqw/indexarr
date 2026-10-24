package indexer

import (
	"strconv"
	"strings"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
	"github.com/buzzqw/indexarr/internal/feed"
	"github.com/buzzqw/indexarr/internal/store"
)

// maxSampleItems is the number of normalized items kept in the last sample.
const maxSampleItems = 50

// observeItem updates completeness and the item sample in one pass over the
// connector result. It runs before any run-level filter, so completeness
// describes what the site supplied, not what indexarr accepted.
func (s *RunStats) observeItem(item engine.Item, secrets []string) {
	s.FieldTotal++
	if item.Size > 0 {
		s.FieldSize++
	}
	if item.Seeders > 0 {
		s.FieldSeeders++
	}
	if hasRealCategory(item.Categories) {
		s.FieldCategory++
	}
	if !item.Published.IsZero() {
		s.FieldDate++
	}
	if item.InfoHash != "" || item.Magnet != "" {
		s.FieldHash++
	}
	if s.maxSampleItems <= 0 {
		s.maxSampleItems = maxSampleItems
	}
	if len(s.SampleItems) < s.maxSampleItems {
		s.SampleItems = append(s.SampleItems, sampleItem(item, secrets))
	}
}

func hasRealCategory(categories []int) bool {
	for _, category := range categories {
		if category > 0 && category != 8000 {
			return true
		}
	}
	return false
}

// sampleItem turns one item into the UI-safe representation: URLs are
// redacted, and Fields tells the UI which values the site actually provided.
func sampleItem(item engine.Item, secrets []string) store.SampleItem {
	redact := func(value string) string {
		return feed.RedactText(strings.TrimSpace(value), secrets)
	}
	published := ""
	if !item.Published.IsZero() {
		published = item.Published.UTC().Format(time.RFC3339)
	}
	out := store.SampleItem{
		Title:       strings.TrimSpace(item.Title),
		GUID:        redact(item.GUID),
		DetailsURL:  redact(item.DetailsURL),
		DownloadURL: redact(item.DownloadURL),
		Magnet:      redact(item.Magnet),
		InfoHash:    strings.TrimSpace(item.InfoHash),
		Size:        item.Size,
		Seeders:     item.Seeders,
		Leechers:    item.Leechers,
		Categories:  append([]int(nil), item.Categories...),
		Published:   published,
		IMDbID:      strings.TrimSpace(item.IMDbID),
		TMDbID:      strings.TrimSpace(item.TMDbID),
		TVDbID:      strings.TrimSpace(item.TVDbID),
		Fields: map[string]bool{
			"size":     item.Size > 0,
			"seeders":  item.Seeders > 0,
			"category": hasRealCategory(item.Categories),
			"date":     !item.Published.IsZero(),
			"infohash": item.InfoHash != "" || item.Magnet != "",
		},
	}
	return out
}

func (s *RunStats) duplicate() {
	s.Duplicates++
	s.Skipped++
}

func (s *RunStats) discardNoLink() {
	s.DiscardNoLink++
	s.Skipped++
}

func (s *RunStats) discardTooOld() {
	s.DiscardTooOld++
	s.Skipped++
}

func (s *RunStats) discardCategory() {
	s.DiscardCategory++
	s.Skipped++
}

func (s *RunStats) discardParse() {
	s.DiscardParse++
}

func (s *RunStats) discardOther() {
	s.DiscardOther++
	s.Skipped++
}

// Metrics returns the row written with indexer_history.
func (s *RunStats) Metrics() *store.RunMetrics {
	if s == nil {
		return nil
	}
	discarded := s.DiscardNoLink + s.Blocked + s.DiscardTooOld +
		s.DiscardCategory + s.DiscardParse + s.DiscardOther
	s.Discarded = discarded
	return &store.RunMetrics{
		Seen:            int64(s.Seen),
		ItemsNew:        int64(s.New),
		Updated:         int64(s.Updated),
		Duplicates:      int64(s.Duplicates),
		Discarded:       int64(discarded),
		DiscardNoLink:   int64(s.DiscardNoLink),
		DiscardBlocked:  int64(s.Blocked),
		DiscardTooOld:   int64(s.DiscardTooOld),
		DiscardCategory: int64(s.DiscardCategory),
		DiscardParse:    int64(s.DiscardParse),
		DiscardOther:    int64(s.DiscardOther),
		FieldTotal:      int64(s.FieldTotal),
		FieldSize:       int64(s.FieldSize),
		FieldSeeders:    int64(s.FieldSeeders),
		FieldCategory:   int64(s.FieldCategory),
		FieldDate:       int64(s.FieldDate),
		FieldHash:       int64(s.FieldHash),
	}
}

// runFilter applies the optional age and category rules of one indexer. The
// rules are configuration, not hard-coded policy: a missing value keeps every
// item, which is the behaviour of an installation that never set them.
type runFilter struct {
	allowed  []int
	excluded []int
	maxAge   time.Duration
	minSize  int64
	now      time.Time
}

func newRunFilter(config map[string]any, now time.Time) runFilter {
	filter := runFilter{
		allowed:  categoriesFromValue(config["categories"]),
		excluded: categoriesFromValue(config["exclude_categories"]),
		now:      now,
	}
	if days := intFromConfig(config["max_age_days"]); days > 0 {
		filter.maxAge = time.Duration(days) * 24 * time.Hour
	}
	if mb := intFromConfig(config["min_size_mb"]); mb > 0 {
		filter.minSize = int64(mb) << 20
	}
	return filter
}

// reject reports why item is not kept, or "" when it passes. "too old" wins
// over "category", so a run reports the more specific reason first.
func (f runFilter) reject(item engine.Item) (tooOld, category, tooSmall bool) {
	if f.maxAge > 0 && !item.Published.IsZero() && item.Published.Before(f.now.Add(-f.maxAge)) {
		tooOld = true
	}
	if f.minSize > 0 && item.Size > 0 && item.Size < f.minSize {
		tooSmall = true
	}
	category = false
	itemCategory := firstCategory(item.Categories)
	if itemCategory > 0 {
		if len(f.excluded) > 0 && categoryMatches(f.excluded, itemCategory) {
			category = true
		}
		if len(f.allowed) > 0 && !categoryMatches(f.allowed, itemCategory) {
			category = true
		}
	}
	return tooOld, category, tooSmall
}

func firstCategory(categories []int) int {
	for _, category := range categories {
		if category > 0 {
			return category
		}
	}
	return 0
}

// categoryMatches accepts both an exact leaf and its top-level group: 5000
// selects every 5xxx category, while 5040 selects only itself.
func categoryMatches(allowed []int, category int) bool {
	for _, candidate := range allowed {
		if candidate == category {
			return true
		}
		if candidate > 0 && candidate%1000 == 0 && category/1000 == candidate/1000 {
			return true
		}
	}
	return false
}

func intFromConfig(value any) int {
	switch typed := value.(type) {
	case nil:
		return 0
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case bool:
		if typed {
			return 1
		}
		return 0
	}
	text := strings.TrimSpace(settingString(value))
	if text == "" {
		return 0
	}
	number, err := strconv.Atoi(text)
	if err != nil {
		return 0
	}
	return number
}
