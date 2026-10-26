package dedup

import (
	"testing"
	"time"
)

func meta(title string, size int64, hash string) Meta {
	return Meta{Title: title, Size: size, InfoHash: hash}
}

func TestMatchMustMerge(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		a, b Meta
	}{
		{
			name: "severance hash and hashless copy",
			a:    meta("Severance.S02E03.Who.Is.Alive.2160p.ATVP.WEB-DL.DDP5.1.DV.HDR.H265-NTb", 9_320_000_000, "52ad082d9b694eefa921d20f8468a1428fc5649a"),
			b:    meta("Severance S02E03 Who Is Alive 2160p ATVP WEB DL DDP5 1 DV HDR H265 NTb", 9_320_000_000, ""),
		},
		{
			name: "two hashless TGx copies",
			a:    meta("The.Movie.2024.1080p.WEB-DL.DDP5.1.H.264-GROUP", 4_300_000_000, ""),
			b:    meta("The Movie 2024 1080p WEB DL DDP5 1 H.264 GROUP", 4_300_000_000, ""),
		},
		{
			name: "size rounding inside one percent",
			a:    meta("Show.S01E02.1080p.WEB-DL.x264-GRP", 9_320_000_000, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			b:    meta("Show.S01E02.1080p.WEB-DL.x264-GRP", 9_300_000_000, ""),
		},
		{
			name: "same hash with different display title",
			a:    meta("Movie 2024 1080p BluRay x264-GRP", 8_000_000_000, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
			b:    meta("Movie.2024.1080p.BluRay.x264-GRP.mkv", 8_000_000_000, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.a.FirstSeenAt, tc.a.LastSeenAt = now, now
			tc.b.FirstSeenAt, tc.b.LastSeenAt = now, now
			if !Match(tc.a, tc.b, Options{}) {
				t.Fatalf("Match(%q, %q) = false, want true", tc.a.Title, tc.b.Title)
			}
		})
	}
}

func TestMatchMustNotMerge(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	realDate := now.Add(-365 * 24 * time.Hour)
	cases := []struct {
		name string
		a, b Meta
	}{
		{
			name: "different group",
			a:    meta("Movie.2024.1080p.WEB-DL.x264-GROUP1", 4_000_000_000, ""),
			b:    meta("Movie.2024.1080p.WEB-DL.x264-GROUP2", 4_000_000_000, ""),
		},
		{
			name: "1080p against 2160p",
			a:    meta("Movie.2024.1080p.WEB-DL.x264-GRP", 4_000_000_000, ""),
			b:    meta("Movie.2024.2160p.WEB-DL.x264-GRP", 4_000_000_000, ""),
		},
		{
			name: "proper against original",
			a:    meta("Show.S01E01.1080p.WEB-DL.x264-GRP", 1_500_000_000, ""),
			b:    meta("Show.S01E01.PROPER.1080p.WEB-DL.x264-GRP", 1_500_000_000, ""),
		},
		{
			name: "repack against original",
			a:    meta("Show.S01E01.1080p.WEB-DL.x264-GRP", 1_500_000_000, ""),
			b:    meta("Show.S01E01.REPACK.1080p.WEB-DL.x264-GRP", 1_500_000_000, ""),
		},
		{
			name: "ita against eng",
			a:    meta("Movie.2024.1080p.BluRay.ITA.x264-GRP", 4_000_000_000, ""),
			b:    meta("Movie.2024.1080p.BluRay.ENG.x264-GRP", 4_000_000_000, ""),
		},
		{
			name: "episode against season pack",
			a:    meta("Show.S02E03.1080p.WEB-DL.x264-GRP", 2_000_000_000, ""),
			b:    meta("Show.S02.1080p.WEB-DL.x264-GRP", 2_000_000_000, ""),
		},
		{
			name: "size beyond tolerance",
			a:    meta("Movie.2024.1080p.WEB-DL.x264-GRP", 9_300_000_000, ""),
			b:    meta("Movie.2024.1080p.WEB-DL.x264-GRP", 9_410_000_000, ""),
		},
		{
			name: "different torrent hashes",
			a:    meta("Movie.2024.1080p.WEB-DL.x264-GRP", 4_000_000_000, "1111111111111111111111111111111111111111"),
			b:    meta("Movie.2024.1080p.WEB-DL.x264-GRP", 4_000_000_000, "2222222222222222222222222222222222222222"),
		},
		{
			name: "real dates two years apart",
			a:    meta("Movie.2024.1080p.WEB-DL.x264-GRP", 4_000_000_000, ""),
			b:    meta("Movie.2024.1080p.WEB-DL.x264-GRP", 4_000_000_000, ""),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Real publication dates must be distinguishable from the crawl
			// clock, so keep first/last seen far enough from published_at.
			tc.a.FirstSeenAt = realDate.Add(-100 * 24 * time.Hour)
			tc.a.LastSeenAt = realDate.Add(-99 * 24 * time.Hour)
			tc.b.FirstSeenAt = realDate.Add(-100 * 24 * time.Hour)
			tc.b.LastSeenAt = realDate.Add(-99 * 24 * time.Hour)
			tc.a.PublishedAt = realDate
			tc.b.PublishedAt = realDate
			if tc.name == "real dates two years apart" {
				tc.b.PublishedAt = realDate.Add(2 * 365 * 24 * time.Hour)
				tc.b.FirstSeenAt = tc.b.PublishedAt.Add(-100 * 24 * time.Hour)
				tc.b.LastSeenAt = tc.b.PublishedAt.Add(-99 * 24 * time.Hour)
			}
			if Match(tc.a, tc.b, Options{}) {
				t.Fatalf("Match(%q, %q) = true, want false", tc.a.Title, tc.b.Title)
			}
		})
	}
}

func TestMatchIgnoresSyntheticDates(t *testing.T) {
	seen := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	a := meta("Show.S01E01.1080p.WEB-DL.x264-GRP", 1_500_000_000, "")
	a.FirstSeenAt, a.LastSeenAt = seen, seen
	a.PublishedAt = seen // site did not provide a date: store used the crawl clock
	b := meta("Show S01E01 1080p WEB DL x264 GRP", 1_500_000_000, "cccccccccccccccccccccccccccccccccccccccc")
	b.FirstSeenAt, b.LastSeenAt = seen.Add(-200*24*time.Hour), seen.Add(-199*24*time.Hour)
	b.PublishedAt = seen.Add(-200 * 24 * time.Hour) // real date, one year earlier
	if !Match(a, b, Options{}) {
		t.Fatal("a synthetic crawl date must not block a merge")
	}
}

func TestExtractFeatures(t *testing.T) {
	got := Extract("Severance.S02E03.PROPER.2160p.ATVP.WEB-DL.ITA-ENG.DDP5.1.H.265-G66.mkv")
	if got.Resolution != "2160p" {
		t.Errorf("resolution = %q", got.Resolution)
	}
	if got.Source != "webdl" {
		t.Errorf("source = %q", got.Source)
	}
	if got.Codec != "h265" {
		t.Errorf("codec = %q", got.Codec)
	}
	if got.Group != "g66" {
		t.Errorf("group = %q", got.Group)
	}
	if got.Flags != "proper" {
		t.Errorf("flags = %q", got.Flags)
	}
	if got.Languages != "eng,ita" {
		t.Errorf("languages = %q", got.Languages)
	}
	if got.Season != 2 || got.Episode != 3 || got.SeasonPack {
		t.Errorf("season/episode = %d/%d pack=%v", got.Season, got.Episode, got.SeasonPack)
	}
	if got.Year != 0 {
		t.Errorf("year = %d", got.Year)
	}
}

func TestExtractCacheIsBoundedAndConsistent(t *testing.T) {
	parsedTitles.Lock()
	parsedTitles.entries = make(map[string]Features, featureCacheMax)
	parsedTitles.order = nil
	parsedTitles.Unlock()

	title := "Show.S01E02.1080p.WEB-DL.x264-GRP"
	want := extractUncached(title)
	if got := Extract(title); got != want {
		t.Fatalf("cached Extract = %+v, want %+v", got, want)
	}
	for i := 0; i < featureCacheMax+10; i++ {
		Extract("Filler " + time.Unix(int64(i), 0).Format(time.RFC3339Nano))
	}
	parsedTitles.RLock()
	size := len(parsedTitles.entries)
	order := len(parsedTitles.order)
	parsedTitles.RUnlock()
	if size > featureCacheMax || order > featureCacheMax {
		t.Fatalf("cache size/order = %d/%d, want <= %d", size, order, featureCacheMax)
	}
}

func TestNormalizedKeyMatchesExtract(t *testing.T) {
	for _, title := range []string{
		"The.Show.S01E02.1080p.WEB-DL.x264-GRP",
		"  Movie Title (2024) [1080p] [ITA ENG] [...",
		"Album - Artist.torrent",
		"Some.Release.2160p.UHD.mkv",
		"",
		"Ünïcödé Tïtlé 720p…",
	} {
		if got, want := NormalizedKey(title), extractUncached(title).Normalized; got != want {
			t.Errorf("NormalizedKey(%q) = %q, want %q", title, got, want)
		}
	}
}
