# TorrentLeech fixtures

TorrentLeech serves releases only from a personal RSS URL that embeds an
rsskey. No real release feed could be captured without that private
credential, so this directory contains:

- `feed-invalid-key.xml` — real response observed on 2026-10-23 from
  https://rss.torrentleech.org/<synthetic-key>; the key used for the request
  was synthetic and is not in the body. It documents the channel envelope and
  the "Your RSS key is invalid." error item.
- `feed.xml` — **synthetic items** inside the real channel envelope
  (`<channel>`, `<title>`, `<link>`, `<description>`, `<ttl>`, `<atom:link>`).
  The item fields (`title`, `link`, `guid`, `description`, `enclosure`,
  `pubDate`) follow the documented RSS shape but are invented; the enclosure
  URL uses the literal placeholder `SYNTHETICRSSKEY`.

The TorrentLeech definition stays `bozza`: promoting it to `verificata`
requires replacing `feed.xml` with a real sanitized capture and re-running the
equivalence test.
