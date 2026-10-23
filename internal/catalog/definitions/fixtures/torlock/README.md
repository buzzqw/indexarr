# Torlock fixtures

Captured on 2026-10-23 from the public Torznab API:

- `search.xml` — `https://www.torlock.com/torznab/api?t=search&q=test`

Cleaning applied before saving:

- kept the first ten `<item>` elements of the `t=search&q=test` response;
- kept `title`, `guid`, `comments`, `pubDate`, `size`, `link`, `category`,
  `enclosure` and the `torznab:attr` values the connector reads
  (`category`, `seeders`, `peers`, `infohash`, `magneturl`);
- removed `downloadvolumefactor`/`uploadvolumefactor` and kept everything else
  unchanged.

The endpoint needs no account and no API key. The HTML site is behind a
Cloudflare challenge for our network at capture time; the Torznab endpoint is
the public interface Torlock publishes for Sonarr, Radarr and Prowlarr.
