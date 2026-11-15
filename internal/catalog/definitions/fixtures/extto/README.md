# ExtTo fixtures

Captured on 2026-10-23 from public pages:

- `listing.html` — https://extto.org/browse/?filter=c=TV
- `detail.html` — https://extto.org/post-detail/95cbfa/yaga-s01e02-il-solstizio-oscuro-ita-eng-1080p-web-dl-ddp5-1-h-264-mem-gp-mkv/

Captured again on 2026-11-15 from the public listing, after the site changed
the row markup:

- `listing-2026-11-15.html` — https://extto.org/browse/?filter=c=TV (three
  newest rows). The title is `a.torrent-title-link` (no more `span.name`), the
  size and age cells carry an `.add-block` label span before the value, and the
  age is relative with the exact day in the `title` attribute.

Cleaning applied before saving (both listings):

- kept the three newest rows of the TV listing and the magnet block of one
  detail page; scripts, styles, forms, comments and tracking attributes
  (`onmouseover`, `onmouseout`, Cloudflare `data-cf-modified`) were removed;
- uploader names and links were replaced with `synthetic-uploader`;
- magnet links and tracker announce URLs are public data and were kept as
  observed; no cookie, session, passkey or account data was present.

The title/row markup, selectors and magnet URIs are unmodified.
