# 1337x fixture

Captured on 2026-10-23 from the public 1337x.tube mirror:

```
https://1337x.tube/search/?q=test
https://1337x.tube/torrent/3828034/...
```

`listing.html` keeps 10 `table.table-list` rows and drops the page chrome and
scripts. The mirror prints a truncated link label, so the definition rebuilds
the title from the full `/torrent/<id>/<slug>` href (hyphens become spaces).
The absolute mirror URLs were reduced to relative `/torrent/...` paths; this
does not change the selectors.

`detail.html` keeps the release magnet anchor (`a#openPopup`). The mirror's
first magnet is the release itself; an unrelated magnet hidden in an HTML
comment is not parsed. The official 1337x.to and its 1337x.st / x1337x.ws
proxies answer a Cloudflare challenge from this network; 1337x.tube is the
reachable mirror used by the definition.
