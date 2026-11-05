package dedup

import "testing"

func TestSearchNameRemovesReleaseNoise(t *testing.T) {
	cases := map[string]string{
		"Mr-9.Do.or.Die.2023.1080p.ITA-ENG.BluRay.x265.AAC-V3SP4EV3R.mkv":         "mr-9 do or die",
		"The.Show.S01E02.1080p.WEB-DL.x264-GROUP":                                 "the show",
		"Movie.Name.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.7.1.Atmos-GROUP":  "movie name",
		"[Group] Movie Name (2024) [1080p] [BluRay]":                              "movie name",
		"Movie Name 2024 1080p":                                                   "movie name",
		"1917.2019.1080p.BluRay.x264-GROUP":                                       "1917",
		"Blade.Runner.2049.2017.1080p.BluRay.x264-GROUP":                          "blade runner 2049",
		"9-1-1.S01E01.1080p.WEB.x264-GROUP":                                       "9-1-1",
		"Spider-Man.No.Way.Home.2021.1080p.BluRay.x264-GROUP":                     "spider-man no way home",
		"Mission.Impossible.Dead.Reckoning.Part.One.2023.1080p.WEB-DL.x264-GROUP": "mission impossible dead reckoning part one",
		"The.Show.S01.1080p.WEB-DL":                                               "the show",
		"Show.Name.1x01.720p.HDTV.x264-GROUP":                                     "show name",
		"Movie.Name.2024.DDP5.1.2160p.WEB-DL.HEVC-GROUP":                          "movie name",
		"Merv 2025 1080p H265 10Bit iTA EnG AC3 Sub iTA EnG MIRCrew":              "merv",
		"The Running Man 2025 FullHD 1080p H264 Webdl Ita Eng AC3 5 1":            "the running man",
		"The Running Man 2025 mkv 2160p DV HDR WEB DL DDP 5 1 iTA ENG H":          "the running man",
		"Merv 2025 AMZN WEB DL DDP5 1 H 264 FHC_CREW mkv":                         "merv",
		"Movie.Name.2024.2160p.HDR10.Dolby.Vis.HEVC-GROUP":                        "movie name",
		"Show.Name.2024.1080p.8Bit.x265-GROUP":                                    "show name",
		"Goodrich Il padre dell anno 2024 1080p H264 iTA EnG AC3 Sub E":           "goodrich",
		"Life is Beautiful La vita è bella 1997 1080p BluRay x264-GROUP":          "life is beautiful",
		"La La Land 2016 1080p BluRay x264-GROUP":                                 "la la land",
		"Il padre dell anno 2024 1080p H264 iTA EnG AC3 Sub E":                    "il padre dell anno",
		"Sub Zero 1995 1080p BluRay x264-GROUP":                                   "sub zero",
		"No.Technical.Tags.Here":                                                  "no technical tags here",
		"":                                                                        "",
	}
	for input, want := range cases {
		if got := SearchName(input); got != want {
			t.Errorf("SearchName(%q) = %q, want %q", input, got, want)
		}
	}
}
