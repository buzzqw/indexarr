# Bangumi Moe fixture

Captured on 2026-10-23 from the public v2 search API:

```
POST https://bangumi.moe/api/v2/torrent/search
query=test
```

`search.json` keeps the API envelope (`count`, `page_count`, `success`) and the
three returned torrents, reduced to the fields the definition reads: `_id`,
`category_tag_id`, `title`, `publish_time`, `magnet`, `infoHash`, `size`,
`seeders`, `leechers`, `downloads` and `finished`. Large `introduction` HTML,
uploader and team objects were removed.

The two observed `category_tag_id` values belong to anime releases (general
anime and its batch/series companion) and are mapped to 5070.
