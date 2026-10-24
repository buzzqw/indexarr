# TorrentGalaxy fixtures

Captured from public pages on 2026-10-24:

- `listing-user.html` — the 50 newest rows of
  https://torrentgalaxy.one/get-posts/user:anonymous/
- `details-uploader.html` — one post-detail page, reduced to its public magnet
  and torrent links.

- `listing.html`, `details-95ac45.html`, `details-95b0a7.html` and
  `details-957fe9.html` are the 2026-10-23 captures kept for the keyword
  fixture; they are not the current uploader layout.

Cleaning applied before saving:

- scripts, styles, forms, comments, tracking attributes and hover-cover
  markup were removed;
- event handlers and inline styles were removed;
- the `anonymous` uploader identity and links were replaced with
  `synthetic-uploader`;
- magnet links and tracker announce URLs are public data and were kept; no
  cookie, session, passkey or account data was present.

The current uploader listing has no magnet and no `small.added-date`. It has a
category link (`/get-posts/category:<name>:time:10D`), the title, the
post-detail link, size and seeders/leechers. The native definition reads the
category from the link and resolves the magnet from the detail page while the
latest list is read. `TestTorrentGalaxyNativeFields` checks the 50 rows and
`TestTorrentGalaxyUploaderResolvesMagnet` checks the resolved magnets.
