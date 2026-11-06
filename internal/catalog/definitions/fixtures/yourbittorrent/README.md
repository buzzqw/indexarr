# YourBittorrent fixtures

- `search.xml` — `https://yourbittorrent.com/torznab/api?t=search&q=test&limit=50`,
  captured on 2026-11-06. 50 items, every row with a magnet, size and seeders
  (the feed is sorted by seeders by default).

The JSON endpoints (`/api/search.json`, `/api/latest.json`) are documented on
the site but are not used: the official Torznab feed already gives magnets,
infohash, size, seeders and standard categories, and supports tv-search with
season and episode. The latest feed marks brand-new rows with size and seeders
0 until the backend processes them, so it is not a reliable periodic source.
