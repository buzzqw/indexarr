# Academic Torrents fixtures

Captured on 2026-10-23 from the public RSS feed:

- `feed.rss` — `https://academictorrents.com/rss.xml`

Cleaning applied before saving:

- kept the channel header and the first ten `<item>` elements;
- kept `title`, `category`, `infohash`, `guid`, `link` and `size`;
- removed the long `<description>` abstracts, which the definition does not
  read.

Academic Torrents publishes the latest entries as RSS and asks robots not to
scrape `browse.php`. The `/apiv2/` search endpoint answered with a Cloudflare
challenge for our network. No account is required for the feed.
