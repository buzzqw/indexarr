# Kickass Torrents (KAT) fixture

Captured on 2026-10-23 from the public keyword page of the thekat.info mirror:

```
https://thekat.info/usearch/?q=test
```

`listing.html` keeps 8 result rows of `table.data` and drops the page chrome,
scripts, ad comments and the duplicate proxied download anchor. The magnet
anchor is unchanged: it points at `mylink.cloud/?url=<encoded magnet>`, and
the definition's `query_param` filter extracts the real magnet. The category
link (`/books`, `/tv`, `/xxx`, ...), size and seeders/leechers are unchanged.

The canonical Kickass domains answer HTTP 451 from this network, so the mirror
is the reachable public source.
