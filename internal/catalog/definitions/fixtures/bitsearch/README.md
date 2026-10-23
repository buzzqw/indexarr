# Bitsearch fixtures

Captured on 2026-10-23 from the public search page:

- `listing.html` — `https://bitsearch.eu/search?q=test`

Cleaning applied before saving:

- kept the result container and the first ten `div.torrent-result-card`
  blocks, with the title, details link, category/size/date metadata, swarm
  counters, `.torrent` link and magnet;
- removed the header, navigation, sort controls, ads, scripts and styles.

Bitsearch is the successor front-end of SolidTorrents (`solidtorrents.to`
redirects here). At capture time it returned the same first page as
`solidtorrents.eu`; the two definitions are separate public sites that share an
index. The magnet and `.torrent` hrefs contain no passkey.
