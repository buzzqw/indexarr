package simmock

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

// release is one deterministic synthetic release served by the fake indexers.
type release struct {
	ID           string
	Title        string
	GUID         string
	Category     string
	Size         int64
	Seeders      int64
	Leechers     int64
	Published    time.Time
	Magnet       string
	InfoHash     string
	Torrent      []byte
	NZB          []byte
	Usenet       bool
	EnclosureURL string
}

func (r release) enclosureType() string {
	if r.Usenet {
		return "application/x-nzb"
	}
	return "application/x-bittorrent"
}

// buildFixtureReleases returns the three fixtures used by both the Torznab and
// the Newznab endpoints: a magnet release, a .torrent enclosure release and a
// Usenet .nzb release.
func buildFixtureReleases() []release {
	torrent1, hash1 := buildTorrent("Simpsons.S01E01.1080p.WEB-DL", 1_500_000_000, "release-one")
	torrent2, _ := buildTorrent("Sim.Demo.Movie.2026.1080p.BluRay.x264", 8_000_000_000, "release-two")
	nzb := buildNZB("Sim.Demo.Album.2026.FLAC")

	return []release{
		{
			ID:        "1",
			Title:     "The.Simpsons.S01E01.1080p.WEB-DL",
			GUID:      "demo-magnet-1",
			Category:  "5040",
			Size:      1_500_000_000,
			Seeders:   42,
			Leechers:  5,
			Published: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC),
			Magnet:    "magnet:?xt=urn:btih:" + hash1 + "&dn=The.Simpsons.S01E01.1080p.WEB-DL",
			InfoHash:  hash1,
			Torrent:   torrent1,
		},
		{
			ID:        "2",
			Title:     "Sim.Demo.Movie.2026.1080p.BluRay.x264",
			GUID:      "demo-torrent-2",
			Category:  "2040",
			Size:      8_000_000_000,
			Seeders:   17,
			Leechers:  2,
			Published: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
			Torrent:   torrent2,
		},
		{
			ID:        "3",
			Title:     "Sim.Demo.Album.2026.FLAC",
			GUID:      "demo-nzb-3",
			Category:  "3040",
			Size:      700_000_000,
			Published: time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC),
			NZB:       nzb,
			Usenet:    true,
		},
	}
}

// buildTorrent returns a minimal but valid bencoded .torrent and its SHA-1
// infohash. The info dictionary is deterministic for a given salt.
func buildTorrent(name string, length int64, salt string) ([]byte, string) {
	pieces := sha1.Sum([]byte("pieces:" + salt))

	var info bytes.Buffer
	info.WriteString("d6:lengthi")
	info.WriteString(strconv.FormatInt(length, 10))
	info.WriteString("e4:name")
	fmt.Fprintf(&info, "%d:%s", len(name), name)
	info.WriteString("12:piece lengthi16384e6:pieces20:")
	info.Write(pieces[:])
	info.WriteString("e")

	hash := sha1.Sum(info.Bytes())

	announce := "http://127.0.0.1:19080/tracker/announce"
	var body bytes.Buffer
	body.WriteString("d8:announce")
	fmt.Fprintf(&body, "%d:%s", len(announce), announce)
	body.WriteString("4:info")
	body.Write(info.Bytes())
	body.WriteString("e")
	return body.Bytes(), hex.EncodeToString(hash[:])
}

// buildNZB returns a tiny but structurally valid NZB document.
func buildNZB(title string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <head><meta type="title">` + title + `</meta></head>
  <file poster="sim &lt;sim@example.invalid&gt;" date="1792401600" subject="` + title + ` [1/1]">
    <groups><group>alt.binaries.sim</group></groups>
    <segments><segment bytes="1024" number="1">sim-segment-1@example.invalid</segment></segments>
  </file>
</nzb>
`)
}
