# Nyaa fixture

Captured on 2026-10-23 from the public search page:

```
https://nyaa.si/?f=0&c=0_0&q=test&s=seeders&o=desc
```

`listing.html` keeps 12 result rows of `table.torrent-list` and drops the
navigation, scripts and footer. Every row is unchanged: category link, title,
magnet and `.torrent` link, size, `data-timestamp`, seeders and leechers.
The magnets are public tracker links and contain no credentials.

The first row is an Anime/English-translated release (`c=1_2`), which the
definition maps to standard category 5070.
