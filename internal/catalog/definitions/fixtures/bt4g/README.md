# BT4G fixtures

Captured on 2026-10-23 from the public RSS/XML search endpoint:

- `search.rss` —
  `https://bt4gprx.com/search?q=test&orderby=relevance&category=all&p=1&page=rss`

Cleaning applied before saving:

- kept the channel header and the first ten complete `<item>` elements;
- kept `title`, `link` (the magnet), `guid`, `pubDate` and the `<description>`
  block that carries `<br>`-separated size and type;
- left the public magnet tracker list untouched.

The HTML search page answers with a Cloudflare challenge for our network; the
`page=rss` variant is the stable XML interface and returns the same results
with magnets. No account is required.
