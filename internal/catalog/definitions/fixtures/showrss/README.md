# showRSS fixtures

Captured on 2026-10-23 from a public show feed:

- `feed.rss` — `https://showrss.info/show/1967.rss` (NCIS: New York)

Cleaning applied before saving:

- kept the channel header and the first five complete `<item>` elements;
- removed nothing else: the feed contains only public release titles, dates,
  guids and magnets, with no user id or personal feed key.

The personal showRSS feed (`https://showrss.info/user/<id>.rss`) embeds the
user's id and is therefore not stored here. The definition's `url` setting
lets the coordinator paste the personal feed; the fixture and the default
setting use the public per-show feed.
