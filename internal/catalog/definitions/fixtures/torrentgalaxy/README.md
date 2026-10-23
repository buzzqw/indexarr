# TorrentGalaxy fixtures

Captured on 2026-10-23 from public pages:

- `listing.html` — https://torrentgalaxy.one/get-posts/user:anonymous/
- `details-95ac45.html` — .../post-detail/95ac45/echo-world-the-arks-ascension-2026-1080p-web-dl-h264-playfuldarkness/
- `details-95b0a7.html` — .../post-detail/95b0a7/by-any-means-2026-1080p-web-dl-hevc-x265-5-1-bone/
- `details-957fe9.html` — .../post-detail/957fe9/dracula-eternal-2026-1080p-web-dl-h264-playfuldarkness/

Cleaning applied before saving:

- kept the three newest rows and the magnet cell of their detail pages;
- scripts, styles, forms, comments, tracking attributes and the hover-cover
  markup were removed;
- the `anonymous` uploader identity and links were replaced with
  `synthetic-uploader`;
- magnet links and tracker announce URLs are public data and were kept; no
  cookie, session, passkey or account data was present.

The row markup, selectors and magnet URIs are unmodified.

The listing (and the three detail pages) has no category column or category
label: the native definition therefore extracts size and seeders/leechers but
leaves categories empty. `TestTorrentGalaxyNativeFields` records that absence
instead of inventing a category from the title.
