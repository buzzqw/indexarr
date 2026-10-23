# TorrentDownloads fixture

Captured on 2026-10-23 from the public RSS search endpoint:

```
https://www.torrentdownloads.pro/rss.xml?type=search&search=test
```

`search.xml` keeps 12 consecutive `<item>` elements and the channel header.
The document is unchanged, including its `encoding='iso-8859-1'` declaration:
the engine's XML reader widens Latin-1/Windows-1252 to UTF-8. Each item has
`info_hash`, `size`, `seeders`, `leechers` and `pubDate`, so the definition
builds the magnet from the hash and never opens a detail page. The search feed
has no category element; rows fall back to 8000.

The HTML search page only returns ad click-throughs to `/td/`, so the RSS
search document is the usable public search surface.
