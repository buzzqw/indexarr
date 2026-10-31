package torznabapi

import (
	"bytes"
	"strings"
	"testing"

	"github.com/buzzqw/indexarr/internal/store"
)

func TestWriteItemEmitsPerReleaseFreeleech(t *testing.T) {
	settings := &store.Settings{TorznabDownloadFactor: 7, TorznabUploadFactor: 1}
	base := store.Release{
		ID:       1,
		Title:    "Show.S01E01.1080p",
		Magnet:   "magnet:?xt=urn:btih:1111111111111111111111111111111111111111",
		InfoHash: "1111111111111111111111111111111111111111",
		Size:     1_000,
		Seeders:  3,
		Leechers: 1,
	}
	cases := []struct {
		name       string
		known      bool
		free       bool
		wantFactor string
		wantKnown  string
		wantFree   string
	}{
		{"unknown falls back to the setting", false, false, "7", "0", ""},
		{"free overrides the setting", true, true, "0", "1", "1"},
		{"paid overrides the setting", true, false, "1", "1", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			release := base
			release.Freeleech = tc.free
			release.FreeleechKnown = tc.known
			var b bytes.Buffer
			(&Server{}).writeItem(&b, "http://example", settings, "key", release, store.ReleaseProvenance{})
			body := b.String()
			if got := attrValue(body, "downloadvolumefactor"); got != tc.wantFactor {
				t.Errorf("downloadvolumefactor = %q, want %q", got, tc.wantFactor)
			}
			if got := attrValue(body, "freeleechknown"); got != tc.wantKnown {
				t.Errorf("freeleechknown = %q, want %q", got, tc.wantKnown)
			}
			if got := attrValue(body, "freeleech"); got != tc.wantFree {
				t.Errorf("freeleech = %q, want %q", got, tc.wantFree)
			}
		})
	}
}

func attrValue(body, name string) string {
	marker := `<torznab:attr name="` + name + `" value="`
	index := strings.Index(body, marker)
	if index < 0 {
		return ""
	}
	rest := body[index+len(marker):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return ""
	}
	return rest[:end]
}
