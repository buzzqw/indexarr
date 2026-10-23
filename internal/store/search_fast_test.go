package store_test

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/perf"
	"github.com/buzzqw/indexarr/internal/store"
)

// TestSearchTextFastPathMatchesReference exercises the intersection path used
// for unselective text queries (more than 2000 FTS matches) and compares it
// with a brute-force scan of the same rows. The seed is small enough to keep
// the test quick but large enough to trigger the fast path.
func TestSearchTextFastPathMatchesReference(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "fast.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := perf.Seed(ctx, st, perf.Options{Releases: 5000, Hidden: 0.1}); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if _, err := st.DB().Exec(`UPDATE releases SET favorite = 1 WHERE id % 7 = 0`); err != nil {
		t.Fatalf("mark favorites: %v", err)
	}

	cases := []struct {
		name   string
		filter store.Filter
	}{
		{name: "first page", filter: store.Filter{Query: "1080p", Limit: 100}},
		{name: "second page", filter: store.Filter{Query: "1080p", Limit: 50, Offset: 50}},
		{name: "near end", filter: store.Filter{Query: "1080p", Limit: 100, Offset: 1700}},
		{name: "past end", filter: store.Filter{Query: "1080p", Limit: 100, Offset: 4000}},
		{name: "multi token", filter: store.Filter{Query: "benchmark 1080p", Limit: 30}},
		{name: "seen order", filter: store.Filter{Query: "1080p", Sort: "seen", Limit: 25, Offset: 10}},
		{name: "include hidden", filter: store.Filter{Query: "1080p", Limit: 100, IncludeHidden: true}},
		{
			name:   "category group",
			filter: store.Filter{Query: "1080p", Categories: []string{"2000"}, Limit: 100},
		},
		{
			name:   "category exact",
			filter: store.Filter{Query: "1080p", Categories: []string{"5040"}, Limit: 20, Offset: 5},
		},
		{
			name:   "exclude term",
			filter: store.Filter{Include: []string{"1080p"}, Exclude: []string{"720p"}, Limit: 40},
		},
		{
			name:   "minimum size",
			filter: store.Filter{Query: "1080p", MinSize: 900_000_000, Limit: 40},
		},
		{
			name:   "indexer",
			filter: store.Filter{Query: "1080p", IndexerID: 1, Limit: 40},
		},
		{
			name:   "favorites only",
			filter: store.Filter{Query: "1080p", FavoritesOnly: true, Limit: 20},
		},
		{
			name: "favorites and category",
			filter: store.Filter{
				Query: "1080p", FavoritesOnly: true, Categories: []string{"2000"}, Limit: 20,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := referenceIDs(t, st, tc.filter)
			releases, total, err := st.SearchReleases(ctx, tc.filter)
			if err != nil {
				t.Fatalf("SearchReleases: %v", err)
			}
			if total != int64(len(want)) {
				t.Fatalf("total = %d, want %d", total, len(want))
			}
			start := tc.filter.Offset
			if start > len(want) {
				start = len(want)
			}
			end := start + tc.filter.Limit
			if end > len(want) {
				end = len(want)
			}
			page := want[start:end]
			if len(releases) != len(page) {
				t.Fatalf("page = %d rows, want %d", len(releases), len(page))
			}
			for i := range page {
				if releases[i].ID != page[i] {
					t.Fatalf("row %d id = %d, want %d", i, releases[i].ID, page[i])
				}
			}
		})
	}
}

// referenceIDs is the straightforward implementation: read every row in the
// requested order and apply the same text and metadata filters.
func referenceIDs(t *testing.T, st *store.Store, f store.Filter) []int64 {
	t.Helper()
	order := "published_at DESC, id DESC"
	if f.Sort == "seen" {
		order = "last_seen_at DESC, id DESC"
	}
	query := `SELECT id, search_title, hidden, favorite, category, size FROM releases`
	if f.IndexerID > 0 {
		query += ` WHERE EXISTS (SELECT 1 FROM release_sources rs
			WHERE rs.release_id = releases.id AND rs.indexer_id = ` + strconv.FormatInt(f.IndexerID, 10) + `)`
	}
	query += ` ORDER BY ` + order
	rows, err := st.DB().Query(query)
	if err != nil {
		t.Fatalf("reference query: %v", err)
	}
	defer rows.Close()
	tokens := make([]string, 0)
	switch {
	case len(f.Include) > 0:
		for _, include := range f.Include {
			tokens = append(tokens, strings.Fields(store.NormalizeSearch(include))...)
		}
	case len(f.Exclude) == 0:
		tokens = strings.Fields(store.NormalizeSearch(f.Query))
	}
	excludes := make([]string, 0, len(f.Exclude))
	for _, token := range f.Exclude {
		excludes = append(excludes, strings.Fields(store.NormalizeSearch(token))...)
	}
	var ids []int64
	for rows.Next() {
		var (
			id       int64
			title    string
			hidden   int
			favorite int
			category string
			size     int64
		)
		if err := rows.Scan(&id, &title, &hidden, &favorite, &category, &size); err != nil {
			t.Fatalf("reference scan: %v", err)
		}
		if hidden != 0 && !f.IncludeHidden {
			continue
		}
		if f.FavoritesOnly && favorite == 0 {
			continue
		}
		if f.MinSize > 0 && size < f.MinSize {
			continue
		}
		if f.MaxSize > 0 && size > f.MaxSize {
			continue
		}
		if len(f.Categories) > 0 && !categoryMatches(category, f.Categories) {
			continue
		}
		matches := true
		for _, token := range tokens {
			if !strings.Contains(title, token) {
				matches = false
				break
			}
		}
		for _, token := range excludes {
			if strings.Contains(title, token) {
				matches = false
				break
			}
		}
		if matches {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reference rows: %v", err)
	}
	return ids
}

func categoryMatches(category string, filters []string) bool {
	for _, raw := range filters {
		c := strings.TrimSpace(raw)
		if c == "" {
			continue
		}
		digits := true
		for _, r := range c {
			if r < '0' || r > '9' {
				digits = false
				break
			}
		}
		if !digits {
			continue
		}
		if len(c) == 4 && strings.HasSuffix(c, "000") {
			if strings.HasPrefix(category, c[:1]) {
				return true
			}
			continue
		}
		if category == c {
			return true
		}
	}
	return false
}
