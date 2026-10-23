package store_test

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/buzzqw/indexarr/internal/perf"
	"github.com/buzzqw/indexarr/internal/store"
)

// benchStore creates a disposable store seeded with n releases.
func benchStore(b *testing.B, n int) *store.Store {
	b.Helper()
	st, err := store.Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })
	if err := perf.Seed(context.Background(), st, perf.Options{Releases: n}); err != nil {
		b.Fatalf("Seed(%d): %v", n, err)
	}
	return st
}

func benchRelease(i int) store.Release {
	title := fmt.Sprintf("Benchmark Movie %d (2024) 1080p WEB-DL x264-GRP", i)
	now := time.Now()
	return store.Release{
		DedupKey:    fmt.Sprintf("bench-new-%d", i),
		InfoHash:    fmt.Sprintf("%040x", 1_000_000+i),
		Title:       title,
		SearchTitle: store.NormalizeSearch(title),
		Size:        1_500_000_000,
		Seeders:     int64(i % 500),
		Leechers:    int64(i % 30),
		Category:    "2040",
		PublishedAt: now.Add(-time.Duration(i) * time.Minute),
		SourceName:  "bench-source",
		GUID:        fmt.Sprintf("guid-%d", i),
		Magnet:      fmt.Sprintf("magnet:?xt=urn:btih:%040x", 1_000_000+i),
		FirstSeenAt: now,
		LastSeenAt:  now,
	}
}

// BenchmarkUpsertRelease measures the ingestion path: a new row (with the
// infohash lookup and the FTS trigger) and the conflict update that refreshes
// an existing row on every run.
func BenchmarkUpsertRelease(b *testing.B) {
	st, err := store.Open(filepath.Join(b.TempDir(), "upsert.db"))
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	b.Run("new", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			release := benchRelease(i)
			if _, err := st.UpsertRelease(ctx, &release); err != nil {
				b.Fatalf("UpsertRelease: %v", err)
			}
		}
	})

	// Pre-populate one row, then update it b.N times. Seeders and LastSeenAt
	// change on every run, which is what the scheduler actually does.
	release := benchRelease(0)
	release.DedupKey = "bench-update"
	if _, err := st.UpsertRelease(ctx, &release); err != nil {
		b.Fatalf("seed update row: %v", err)
	}
	b.Run("update", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			release.Seeders = int64(i % 500)
			release.LastSeenAt = time.Now()
			if _, err := st.UpsertRelease(ctx, &release); err != nil {
				b.Fatalf("UpsertRelease: %v", err)
			}
		}
	})
}

// BenchmarkSearchReleases covers the queries the Torznab endpoint and the web
// UI issue: no query (dashboard/recent), a trigram FTS query, and the common
// source+category+seeders filter combination.
func BenchmarkSearchReleases(b *testing.B) {
	sizes := []int{10_000, 50_000}
	for _, size := range sizes {
		st := benchStore(b, size)
		ctx := context.Background()
		b.Run(fmt.Sprintf("%dk", size/1000), func(b *testing.B) {
			cases := []struct {
				name string
				f    store.Filter
				// bytes is the approximate response payload of one page.
				bytes int64
			}{
				{"empty", store.Filter{Limit: 100}, 100 * 800},
				{"text", store.Filter{Query: "1080p", Limit: 100}, 100 * 800},
				{"filters", store.Filter{
					SourceID:   1,
					Categories: []string{"2000"},
					MinSeeders: 25,
					Limit:      100,
				}, 100 * 800},
				{"text_filters", store.Filter{
					Query:      "1080p",
					Categories: []string{"2000"},
					Limit:      100,
				}, 100 * 800},
				{"text_min_size", store.Filter{
					Query:   "1080p",
					MinSize: 500_000_000,
					Limit:   100,
				}, 100 * 800},
			}
			for _, tc := range cases {
				b.Run(tc.name, func(b *testing.B) {
					if _, _, err := st.SearchReleases(ctx, tc.f); err != nil {
						b.Fatalf("SearchReleases: %v", err)
					}
					b.ReportAllocs()
					b.SetBytes(tc.bytes)
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						releases, _, err := st.SearchReleases(ctx, tc.f)
						if err != nil {
							b.Fatalf("SearchReleases: %v", err)
						}
						if len(releases) == 0 {
							b.Fatal("no releases")
						}
					}
				})
			}
		})
	}
}

// BenchmarkSearchReleasesShortQuery measures the LIKE fallback used for terms
// shorter than the trigram tokenizer can index.
func BenchmarkSearchReleasesShortQuery(b *testing.B) {
	st := benchStore(b, 50_000)
	ctx := context.Background()
	filter := store.Filter{Query: "4k", Limit: 100}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := st.SearchReleases(ctx, filter); err != nil {
			b.Fatalf("SearchReleases: %v", err)
		}
	}
}

// BenchmarkReleaseProvenance measures one result page: 100 ids resolved with
// one query, without N+1 lookups.
func BenchmarkReleaseProvenance(b *testing.B) {
	st := benchStore(b, 10_000)
	ctx := context.Background()
	rows, err := st.DB().QueryContext(ctx, `SELECT id FROM releases ORDER BY id LIMIT 100`)
	if err != nil {
		b.Fatalf("ids: %v", err)
	}
	defer rows.Close()
	ids := make([]int64, 0, 100)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			b.Fatalf("scan id: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		b.Fatalf("ids: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		provenance, err := st.ReleaseProvenance(ctx, ids)
		if err != nil {
			b.Fatalf("ReleaseProvenance: %v", err)
		}
		if len(provenance) == 0 {
			b.Fatal("no provenance")
		}
	}
}
