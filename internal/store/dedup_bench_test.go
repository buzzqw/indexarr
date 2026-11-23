package store

import (
	"fmt"
	"testing"
	"time"
)

// BenchmarkFuseReleases measures the per-request duplicate pass on a result
// set shaped like a wide catalogue search: many distinct titles, each with a
// handful of copies from different indexers.
func BenchmarkFuseReleases(b *testing.B) {
	const groups, copies = 1000, 5
	now := time.Now()
	rows := make([]Release, 0, groups*copies)
	for i := 0; i < groups; i++ {
		title := fmt.Sprintf("Series %04d S01E01 1080p WEB-DL x264-GROUP", i)
		for j := 0; j < copies; j++ {
			rows = append(rows, Release{
				DedupKey:    fmt.Sprintf("%d-%d", i, j),
				Title:       title,
				SearchTitle: NormalizeSearch(title),
				Size:        1 << 20,
				Seeders:     int64(j),
				PublishedAt: now.Add(-time.Hour),
				FirstSeenAt: now.Add(-time.Hour),
				LastSeenAt:  now,
			})
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := FuseReleases(rows)
		if len(out) != groups {
			b.Fatalf("fused rows = %d, want %d", len(out), groups)
		}
	}
}
