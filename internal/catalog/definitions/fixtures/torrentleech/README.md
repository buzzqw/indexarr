# TorrentLeech fixtures

TorrentLeech serves releases only from a personal RSS URL that embeds an
rsskey. No real release feed could be captured without that private
credential, so the RSS fixtures are:

- `feed-invalid-key.xml` — real response observed on 2026-10-23 from
  `https://rss.torrentleech.org/SYNTHETICRSSKEY`;
- `feed.xml` — synthetic items that preserve the observed RSS 2.0 envelope;
  the rsskey, titles and hashes are placeholders.

Public capture on 2026-10-23:

- `login.html` — `https://www.torrentleech.org/`

Cleaning applied before saving:

- kept only `form[name=login-form]` with the username and password fields;
- removed the signup banner, scripts, cookies and external service links.

Synthetic fixtures, built from the observed login and browse markup:

- `search.html` — three rows of `table#torrenttable` with title, details,
  category, date, size, seeders and leechers;
- `detail.html` — a release page with a magnet and a `.torrent` link;
- `login-ok.html` — a page with a logout link;
- `login-failed.html` — a page with `div.alert-danger`.

The browse path `/torrents/browse/list/query/test` redirects guests to the
login page, which confirms the path but not the result markup. Both the
login+search definition and the RSS definition stay "in prova" until the
coordinator verifies them with an account.
