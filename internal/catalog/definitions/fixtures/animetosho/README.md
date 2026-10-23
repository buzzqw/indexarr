# AnimeTosho fixture

Captured on 2026-10-23 from the public search page:

```
https://animetosho.org/search?q=test
```

`listing.html` keeps 12 `div.home_list_entry` rows and drops the page chrome,
scripts and the "older entries" pagination. Each row still carries its
`div.link` (release page), the magnet, the human-readable size, the
`Seeders: N / Leechers: N` title and the `Date/time submitted` title.

AnimeTosho is an anime-only archive, so the definition maps the release URL
namespace (`/view/`, `/series/`, `/episodes/`) to standard category 5070.
