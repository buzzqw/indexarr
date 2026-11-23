package dedup

import "testing"

// BenchmarkSearchName mirrors the per-row work of the TMDB/TVDB links on the
// releases page: two cleaned titles are derived for every visible release.
func BenchmarkSearchName(b *testing.B) {
	titles := []string{
		"Mr-9.Do.or.Die.2023.1080p.ITA-ENG.BluRay.x265.AAC-V3SP4EV3R.mkv",
		"Series A S06E13 720p HDTV x264-GROUP [MIRCrew]",
		"Another.Title.2024.2160p.WEB-DL.DDP5.1.H.265-Example",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SearchName(titles[i%len(titles)])
	}
}

func BenchmarkSearchGroup(b *testing.B) {
	titles := []string{
		"Mr-9.Do.or.Die.2023.1080p.ITA-ENG.BluRay.x265.AAC-V3SP4EV3R.mkv",
		"Series A S06E13 720p HDTV x264-GROUP [MIRCrew]",
		"Another.Title.2024.2160p.WEB-DL.DDP5.1.H.265-Example",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = searchGroup(titles[i%len(titles)])
	}
}
