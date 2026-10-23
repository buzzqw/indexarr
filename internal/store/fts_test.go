package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestFTSIndexStaysInSync(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	now := time.Now()
	release := Release{
		DedupKey: "fts-1", Title: "The Alpha Show 1080p",
		SearchTitle: NormalizeSearch("The Alpha Show 1080p"),
		Category:    "5040", FirstSeenAt: now, LastSeenAt: now,
	}
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatal(err)
	}
	short := Release{
		DedupKey: "fts-2", Title: "Movie 4K",
		SearchTitle: NormalizeSearch("Movie 4K"),
		Category:    "2040", FirstSeenAt: now, LastSeenAt: now,
	}
	if _, err := st.UpsertRelease(ctx, &short); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		query string
		total int64
	}{
		{"alpha", 1},
		{"lph", 1}, // trigram substring, not a whole word
		{"show", 1},
		{"4k", 1}, // too short for trigram, LIKE fallback
		{"missing", 0},
	}
	for _, tc := range cases {
		_, total, err := st.SearchReleases(ctx, Filter{Query: tc.query})
		if err != nil {
			t.Fatalf("search %q: %v", tc.query, err)
		}
		if total != tc.total {
			t.Errorf("search %q total = %d, want %d", tc.query, total, tc.total)
		}
	}
	if _, total, err := st.SearchReleases(ctx, Filter{Include: []string{"show"}, Exclude: []string{"alpha"}}); err != nil || total != 0 {
		t.Errorf("FTS exclude total = %d, %v", total, err)
	}

	release.Title = "The Beta Show 1080p"
	release.SearchTitle = NormalizeSearch(release.Title)
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := st.SearchReleases(ctx, Filter{Query: "alpha"}); total != 0 {
		t.Errorf("old FTS term still matches: %d", total)
	}
	if _, total, _ := st.SearchReleases(ctx, Filter{Query: "beta"}); total != 1 {
		t.Errorf("updated FTS term total = %d", total)
	}

	if _, err := st.DeleteRelease(ctx, release.ID); err != nil {
		t.Fatal(err)
	}
	if _, total, _ := st.SearchReleases(ctx, Filter{Query: "beta"}); total != 0 {
		t.Errorf("deleted FTS term still matches: %d", total)
	}
}

func TestFTSMigrationRebuildsExistingRows(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/fts-migrate.db"
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rel := Release{
		DedupKey: "legacy", Title: "Legacy Show S01E01",
		SearchTitle: NormalizeSearch("Legacy Show S01E01"),
		Category:    "5040", FirstSeenAt: now, LastSeenAt: now,
	}
	if _, err := st.UpsertRelease(ctx, &rel); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate a database created before FTS5 support.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TRIGGER IF EXISTS releases_fts_ai`,
		`DROP TRIGGER IF EXISTS releases_fts_ad`,
		`DROP TRIGGER IF EXISTS releases_fts_au`,
		`DROP TABLE IF EXISTS releases_fts`,
	} {
		if _, err := raw.Exec(statement); err != nil {
			_ = raw.Close()
			t.Fatal(err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var ftsCount int
	if err := reopened.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'releases_fts'`).Scan(&ftsCount); err != nil || ftsCount != 1 {
		t.Fatalf("FTS table after migration = %d, %v", ftsCount, err)
	}
	_, total, err := reopened.SearchReleases(ctx, Filter{Query: "legacy"})
	if err != nil || total != 1 {
		t.Fatalf("search after FTS rebuild = %d, %v", total, err)
	}
}

func TestFTSQueryPlanUsesIndex(t *testing.T) {
	st := newTestStore(t)
	plan := ""
	rows, err := st.DB().Query(`EXPLAIN QUERY PLAN
		SELECT id FROM releases
		WHERE id IN (SELECT rowid FROM releases_fts WHERE releases_fts MATCH ?)`,
		`"show"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	if !strings.Contains(plan, "releases_fts") {
		t.Errorf("query plan does not use FTS:\n%s", plan)
	}
}
