# LimeTorrents fixtures

Captured on 2026-10-23 from public pages:

- `listing.html` — `https://www.limetorrents.fun/search/all/test/`
- `detail.html` — the first release page linked from that search
  (`...-torrent-19927869.html`)

Cleaning applied before saving:

- kept the result `table.table2` with the first eight release rows;
- removed scripts, styles and tracking images;
- removed session parameters, cookies and the external mirror URL query
  string (the fixture keeps the magnet only).

The listing has no magnet and no numeric category: the category is the word
after `in` in the "Last Month - in Other" cell. The release page carries the
magnet. Relative dates ("19 hours ago") are not extracted.
