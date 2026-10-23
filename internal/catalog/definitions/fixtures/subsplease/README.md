# SubsPlease fixture

Captured on 2026-10-23 from the public 1080p feed:

```
https://subsplease.org/rss/?r=1080
```

`feed.xml` keeps 12 consecutive `<item>` elements and the channel header.
Every item carries the magnet in `<link>` and the infohash in `<guid>`; the
`<category>` element is a human-readable show name, so the definition uses the
5070 anime default. The feed is a "latest releases" listing: there is no
search endpoint, and the RSS family applies the query to titles client-side.
