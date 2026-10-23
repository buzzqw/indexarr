# Corsaro fixtures

Captured on 2026-10-23 from public pages on https://ilcorsaroblu.org/:

- `listing.html` — https://ilcorsaroblu.org/index.php?page=torrents&category=0&active=0
- `detail.html` — https://ilcorsaroblu.org/Ho_ucciso_Napoleone_-979378.html

Cleaning applied before saving:

- kept three movie rows (the ones whose title matches the magnet `dn`) and the
  magnet cell of one detail page;
- scripts, styles, forms, comments, tracking attributes and tracking images
  were removed;
- the uploader identity and its user-id link were replaced with
  `synthetic-uploader`;
- magnet links and tracker announce URLs are public data and were kept; no
  cookie, session, passkey or account data was present.

The row markup, selectors and magnet URIs are unmodified.
