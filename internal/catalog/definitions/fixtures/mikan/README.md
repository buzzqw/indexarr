# Mikan Project fixture

Captured on 2026-10-23 from the public search page:

```
https://mikanani.me/Home/Search?searchstr=test
```

`listing.html` keeps 12 `tr.js-search-results-row` rows of the search table
and drops the page chrome, scripts and the 425 other results. Every row keeps
the title link, the magnet in `data-clipboard-text`, the size, the update time
and the `.torrent` link. The magnets are public tracker links and contain no
credentials.

The site is anime-only. The `/Home/` release namespace is mapped to standard
category 5070; there is no per-row category cell.
