package catalog_test

// Offline checks for the ExtTo definition. They run the real engine against the
// cleaned fixtures, so a selector change that no longer matches the captured
// pages fails here instead of in production.
//
// The reason this file exists: on 2026-11-13 ExtTo moved the row title from
// `span.name` to `a.torrent-title-link` and added a label span inside the size
// and age cells. Every row was read and every row was discarded, so the source
// reported "no results" for two days while the site answered 50 rows per page.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/engine"
)

func TestExtToFixturesReadEveryField(t *testing.T) {
	cases := []struct {
		name     string
		fixture  string
		rows     int
		firstDay time.Time
	}{
		{
			// Layout until 2026-11-13: title in `span.name`, the day as text in
			// the age cell and no `title` attribute to read it from.
			name:    "2026-10-23 layout",
			fixture: "extto/listing.html",
			rows:    3,
		},
		{
			// Layout since 2026-11-15: title in `a.torrent-title-link`, labels
			// inside the size and age cells, relative age plus the exact day in
			// the `title` attribute.
			name:     "2026-11-15 layout",
			fixture:  "extto/listing-2026-11-15.html",
			rows:     3,
			firstDay: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/browse/" {
					serveFile(t, w, tc.fixture)
					return
				}
				http.NotFound(w, r)
			}))
			t.Cleanup(server.Close)

			connector := nativeConnector(t, "extto", server.URL)
			items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "test"})
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			if len(items) != tc.rows {
				t.Fatalf("items = %d, want %d usable rows", len(items), tc.rows)
			}
			for _, item := range items {
				if item.Title == "" || item.DetailsURL == "" || item.Magnet == "" {
					t.Errorf("incomplete row: %+v", item)
				}
				if item.Size <= 0 {
					t.Errorf("%q: size was not extracted", item.Title)
				}
				if item.Seeders <= 0 {
					t.Errorf("%q: seeders were not extracted", item.Title)
				}
				if len(item.Categories) != 1 || item.Categories[0] != 5000 {
					t.Errorf("%q: categories = %v, want 5000 (TV)", item.Title, item.Categories)
				}
			}
			if !tc.firstDay.IsZero() {
				if got := items[0].Published; !got.Equal(tc.firstDay) {
					t.Errorf("first row day = %s, want %s (read from the title attribute)", got, tc.firstDay)
				}
			}
		})
	}
}
