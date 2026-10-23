# SolidTorrents fixtures

Captured on 2026-10-23 from the public search page:

- `listing.html` — `https://solidtorrents.eu/search?q=test`

Cleaning applied before saving:

- kept the result container and the first ten `div.torrent-result-card`
  blocks, with the title, details link, category/size/date metadata, swarm
  counters, `.torrent` link and magnet;
- removed the header, navigation, sort controls, ads, scripts and styles.

The page is server-rendered. The magnet and `.torrent` hrefs are public and
contain no passkey. `solidtorrents.to` now redirects to Bitsearch; the `.eu`
domain kept serving the original SolidTorrents layout at capture time.
