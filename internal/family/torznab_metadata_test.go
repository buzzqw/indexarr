package family

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/buzzqw/indexarr/internal/engine"
)

// TorznabMetadataFeed carries the attributes indexarr and Prowlarr emit for a
// release: several language attributes and the download-quota flags.
const TorznabMetadataFeed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
  <channel>
    <title>Metadata</title>
    <item>
      <title>Free.Release.2024.ITA.ENG</title>
      <guid>guid-free</guid>
      <enclosure url="https://tracker.example/1.torrent" length="100" type="application/x-bittorrent"/>
      <torznab:attr name="category" value="2040"/>
      <torznab:attr name="downloadvolumefactor" value="0"/>
      <torznab:attr name="language" value="Italian"/>
      <torznab:attr name="language" value="English"/>
    </item>
    <item>
      <title>Unknown.Quota.Release</title>
      <guid>guid-unknown</guid>
      <enclosure url="https://tracker.example/2.torrent" length="100" type="application/x-bittorrent"/>
      <torznab:attr name="category" value="2040"/>
      <torznab:attr name="freeleechknown" value="0"/>
      <torznab:attr name="freeleech" value="1"/>
    </item>
    <item>
      <title>Flagged.Free.Release</title>
      <guid>guid-flagged</guid>
      <enclosure url="https://tracker.example/3.torrent" length="100" type="application/x-bittorrent"/>
      <torznab:attr name="category" value="2040"/>
      <torznab:attr name="freeleech" value="1"/>
      <torznab:attr name="language" value="ita"/>
    </item>
  </channel>
</rss>
`

// TestTorznabKeepsLanguagesAndFreeleech proves the metadata indexarr emits is
// also read back: a chain indexarr → indexarr, or Prowlarr → indexarr, keeps
// the language list and the download quota instead of losing them.
func TestTorznabKeepsLanguagesAndFreeleech(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(TorznabMetadataFeed))
	}))
	defer server.Close()

	connector, err := NewTorznab(TorznabConfig{
		Indexer:   engine.IndexerRef{Slug: "tz"},
		BaseURL:   server.URL + "/api",
		Transport: newTestTransport(),
	})
	if err != nil {
		t.Fatalf("NewTorznab: %v", err)
	}
	items, err := connector.Search(context.Background(), engine.SearchRequest{Mode: engine.ModeSearch, Query: "x"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	byTitle := map[string]engine.Item{}
	for _, item := range items {
		byTitle[item.Title] = item
	}

	free := byTitle["Free.Release.2024.ITA.ENG"]
	if !free.Freeleech || !free.FreeleechKnown {
		t.Errorf("free release = %+v, want free and known", free)
	}
	if !slices.Equal(free.Languages, []string{"Italian", "English"}) {
		t.Errorf("languages = %v, want both attributes in order", free.Languages)
	}

	unknown := byTitle["Unknown.Quota.Release"]
	if unknown.FreeleechKnown || unknown.Freeleech {
		t.Errorf("unknown quota = %+v, want not stated (freeleechknown=0 wins)", unknown)
	}

	flagged := byTitle["Flagged.Free.Release"]
	if !flagged.Freeleech || !flagged.FreeleechKnown {
		t.Errorf("flagged release = %+v, want free and known", flagged)
	}
	if !slices.Equal(flagged.Languages, []string{"Italian"}) {
		t.Errorf("languages = %v, want the code mapped to its name", flagged.Languages)
	}
}
