package feed

import (
	"os"
	"strings"
	"testing"
	"time"
)

const torznabResponse = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
<channel>
<title>test</title>
<item>
<title>FBI.Most.Wanted-1x02-1080p</title>
<guid isPermaLink="false">abc123</guid>
<link>magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&amp;dn=FBI</link>
<pubDate>Mon, 05 Oct 2026 10:17:44 +0000</pubDate>
<category>5000</category>
<enclosure url="magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&amp;dn=FBI" length="0" type="application/x-bittorrent"/>
<torznab:attr name="size" value="123456"/>
<torznab:attr name="seeders" value="12"/>
<torznab:attr name="peers" value="20"/>
<torznab:attr name="infohash" value="0123456789abcdef0123456789abcdef01234567"/>
<torznab:attr name="category" value="5040"/>
<torznab:attr name="imdbid" value="tt0133093"/>
</item>
<item>
<title>Movie.2024.1080p.BluRay</title>
<guid>guid-2</guid>
<link>http://indexer.test/download/2</link>
<enclosure url="http://indexer.test/download/2.torrent" length="999" type="application/x-bittorrent"/>
<torznab:attr name="seeders" value="3"/>
</item>
<item>
<title>Web.Page.Release</title>
<link>https://tracker.test/details/3</link>
<category>TV &gt; HD</category>
</item>
</channel>
</rss>`

func TestParseTorznabResponse(t *testing.T) {
	items, err := ParseRSS([]byte(torznabResponse))
	if err != nil {
		t.Fatalf("ParseRSS: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	first := items[0]
	if first.Title != "FBI.Most.Wanted-1x02-1080p" {
		t.Errorf("title = %q", first.Title)
	}
	if first.InfoHash != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("infohash = %q", first.InfoHash)
	}
	if !strings.HasPrefix(first.Magnet, "magnet:?xt=urn:btih:") {
		t.Errorf("magnet = %q", first.Magnet)
	}
	if first.Size != 123456 {
		t.Errorf("size = %d", first.Size)
	}
	if first.Seeders != 12 || first.Leechers != 20 {
		t.Errorf("seeders/leechers = %d/%d", first.Seeders, first.Leechers)
	}
	if first.Category != "5040" {
		t.Errorf("category = %q", first.Category)
	}
	if first.IMDbID != "0133093" {
		t.Errorf("imdb = %q", first.IMDbID)
	}
	if first.Published.IsZero() || !first.Published.Equal(time.Date(2026, 10, 5, 10, 17, 44, 0, time.UTC)) {
		t.Errorf("published = %v", first.Published)
	}

	second := items[1]
	if second.TorrentURL != "http://indexer.test/download/2.torrent" {
		t.Errorf("torrent URL = %q", second.TorrentURL)
	}
	if second.Magnet != "" {
		t.Errorf("magnet = %q, want empty", second.Magnet)
	}
	if second.Size != 999 {
		t.Errorf("size = %d", second.Size)
	}

	third := items[2]
	if third.TorrentURL != "" {
		t.Errorf("torrent URL = %q, want empty", third.TorrentURL)
	}
	if third.Category != "" {
		t.Errorf("text-only category should be ignored, got %q", third.Category)
	}
}

func TestParseAtomFeed(t *testing.T) {
	const atom = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Test</title>
  <entry>
    <title>Atom.Show.S02E03.720p</title>
    <id>urn:uuid:1234</id>
    <published>2026-01-02T03:04:05Z</published>
    <link rel="alternate" href="https://feed.test/entry/1"/>
    <link rel="enclosure" href="https://feed.test/file.torrent" type="application/x-bittorrent" length="4242"/>
    <category term="5000"/>
  </entry>
</feed>`
	items, err := ParseRSS([]byte(atom))
	if err != nil {
		t.Fatalf("ParseRSS(atom): %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	item := items[0]
	if item.Title != "Atom.Show.S02E03.720p" {
		t.Errorf("title = %q", item.Title)
	}
	if item.TorrentURL != "https://feed.test/file.torrent" {
		t.Errorf("torrent = %q", item.TorrentURL)
	}
	if item.Size != 4242 {
		t.Errorf("size = %d", item.Size)
	}
	if item.Category != "5000" {
		t.Errorf("category = %q", item.Category)
	}
	if item.Published.IsZero() {
		t.Error("published is zero")
	}
}

func TestParseEmptyFeed(t *testing.T) {
	if _, err := ParseRSS(nil); err == nil {
		t.Fatal("expected an error for an empty feed")
	}
}

func TestParseTruncatedRSSKeepsCompleteItems(t *testing.T) {
	body := `<rss version="2.0"><channel><title>Knaben</title>
<item><title>First.Release.1080p</title><link>magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa</link></item>
<item><title>Second.Release.1080p</title><link>magnet:?xt=urn:btih:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb</link></item>
<item><title>Cut`
	items, err := ParseRSS([]byte(body))
	if err != nil {
		t.Fatalf("ParseRSS truncated: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want the two complete items", len(items))
	}
	if items[0].Title != "First.Release.1080p" {
		t.Fatalf("first title = %q", items[0].Title)
	}
}

func TestParseTorrentEnclosureDoesNotInventMagnet(t *testing.T) {
	body, err := os.ReadFile("testdata/torrent_enclosure.xml")
	if err != nil {
		t.Fatal(err)
	}
	items, err := ParseRSS(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
	if items[0].Magnet != "" {
		t.Fatalf("magnet was reconstructed despite a .torrent: %q", items[0].Magnet)
	}
	if items[0].TorrentURL == "" {
		t.Fatal("torrent URL was lost")
	}
}

func TestRSSProtocolInferenceFixtures(t *testing.T) {
	for _, tc := range []struct {
		file   string
		usenet bool
	}{
		{"../catalog/definitions/fixtures/rss/torrent.xml", false},
		{"../catalog/definitions/fixtures/rss/usenet.xml", true},
	} {
		body, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		items, err := ParseRSS(body)
		if err != nil || len(items) != 1 {
			t.Fatalf("%s: %d items, %v", tc.file, len(items), err)
		}
		if items[0].Usenet != tc.usenet {
			t.Errorf("%s: Usenet = %v, want %v", tc.file, items[0].Usenet, tc.usenet)
		}
		if items[0].TorrentURL == "" {
			t.Errorf("%s: download URL missing", tc.file)
		}
	}
}

func TestParseRSSNamespacedTorrentURLAndFileSize(t *testing.T) {
	body := []byte(`<?xml version="1.0"?>
<rss version="2.0" xmlns:anibt="https://anibt.net/xmlns/rss/1.0/"><channel><title>AniBT</title>
<item>
  <title>Release 04</title>
  <link>https://anibt.net/release/rel_x</link>
  <guid isPermaLink="false">rel_x</guid>
  <anibt:torrentUrl>https://anibt.net/api/torrent/rel_x.torrent</anibt:torrentUrl>
  <anibt:fileSize>1454440210</anibt:fileSize>
</item>
</channel></rss>`)
	items, err := ParseRSS(body)
	if err != nil {
		t.Fatalf("ParseRSS: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	if items[0].TorrentURL != "https://anibt.net/api/torrent/rel_x.torrent" {
		t.Errorf("torrent = %q", items[0].TorrentURL)
	}
	if items[0].Size != 1454440210 {
		t.Errorf("size = %d", items[0].Size)
	}
}
