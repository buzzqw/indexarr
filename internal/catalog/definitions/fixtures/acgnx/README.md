# ACGNX fixture

Captured on 2026-10-23 from the public RSS keyword search:

```
https://share.acgnx.se/rss.xml?keyword=test
```

`search.xml` keeps 10 consecutive `<item>` elements and the channel header.
Each item is unchanged: title, `guid` (release page), magnet in the
`enclosure` URL, category (動畫, 音樂, ...), `pubDate`, and a description that
carries the size in the form `| 923.5MB | 動畫 | <hash>`.

The site root answers a Cloudflare challenge, but this RSS endpoint is public
and supports the `keyword` query. The fixture includes mixed ACGNX categories,
so the definition maps 動畫 to 5070, 音樂 to 3000, 漫畫 to 7030 and so on.
