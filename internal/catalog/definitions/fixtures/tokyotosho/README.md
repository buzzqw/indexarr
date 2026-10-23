# TokyoTosho fixture

Captured on 2026-10-23 from the public RSS listing:

```
https://www.tokyotosho.info/rss.php?filter=1
```

`feed.xml` keeps 10 consecutive `<item>` elements and the channel header; the
page chrome is not part of RSS. The site's `search.php` answers a Cloudflare
challenge (HTTP 403), so the definition uses this public listing and filters
titles client-side.

Items link either a `.torrent` file or Nyaa's `/view/<id>/torrent` endpoint.
The feed parser treats both as torrent downloads. A magnet also appears in the
`<description>` CDATA; the RSS parser does not read it, so the download link
is the primary path. The feed's only category is `Anime`, mapped to 5070.
