# BTDigg fixtures

Captured on 2026-10-23 from the public search page:

- `listing.html` — `https://btdig.com/search?q=test&p=0`

Cleaning applied before saving:

- kept the first ten `div.one_result` blocks with the title/details link,
  `span.torrent_size` and the magnet;
- removed the file-tree excerpts (`div.torrent_excerpt`), navigation, language
  switcher and scripts.

BTDigg serves a Korean interface by default; the release titles and magnets are
language-independent. The listing shows relative ages ("8년 전 발견") and no
seeder counts, so the definition does not extract a date, seeders or a
category.
