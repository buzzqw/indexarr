# YTS fixture

Captured on 2026-10-23 from the public JSON API:

```
https://yts.gg/api/v2/list_movies.json?query_term=test&limit=5
```

`search.json` keeps the API envelope and five movies, reduced to the fields the
definition reads (`url`, `title_long`, `date_uploaded_unix`, `torrents` with
hash, quality, size_bytes, seeds and peers). Long summaries, artwork URLs and
cast lists were removed. The API returns movies, not individual torrents; the
definition takes the first torrent in the array for the hash, size and health.

The `yts.mx` host does not resolve from this network. `yts.gg` serves the same
API and redirects the yts.lt/yts.am/yts.ag aliases to it.
