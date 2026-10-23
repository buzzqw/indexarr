package feed

import "testing"

func TestLooksLikeTorrentURL(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		contentType string
		want        bool
	}{
		{"extension", "https://example.test/release.torrent", "", true},
		{"nya view endpoint", "https://nyaa.si/view/2177602/torrent", "", true},
		{"download path", "https://example.test/download/123", "", true},
		{"magnet", "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567", "", false},
		{"web page", "https://example.test/torrent/123/release", "", false},
		{"content type", "https://example.test/file", "application/x-bittorrent", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LooksLikeTorrentURL(tc.raw, tc.contentType); got != tc.want {
				t.Errorf("LooksLikeTorrentURL(%q, %q) = %v, want %v", tc.raw, tc.contentType, got, tc.want)
			}
		})
	}
}
