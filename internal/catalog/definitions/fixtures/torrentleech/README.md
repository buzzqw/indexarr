# TorrentLeech fixtures

TorrentLeech serves releases only to an authenticated account.

Public capture on 2026-10-23:

- `login.html` — `https://www.torrentleech.org/`

Capture on 2026-10-26 with the coordinator's account (values then replaced with
placeholders, keeping the observed shape):

- the browse endpoint answers **JSON** and not the HTML table used by the
  first draft: `/torrents/browse/list/query/<q>` returns
  `{numFound, torrentList:[...]}`, where each row has `fid`, `name`,
  `filename`, `categoryID`, `addedTimestamp`, `size` (float in scientific
  notation), `seeders`, `leechers`, `completed` and `download_multiplier`.
  `search.json` keeps three synthetic rows in exactly that shape;
- the release page `https://www.torrentleech.org/torrent/<fid>` exposes the
  button `href="/download/<fid>/<filename>.torrent"`. `detail.html` keeps that
  markup with a placeholder fid and filename.

Cleaning applied before saving: no cookie, session id, rsskey, username,
password or account-specific URL is kept in any fixture.

The RSS fixtures are unchanged:

- `feed-invalid-key.xml` — real response observed on 2026-10-23 from
  `https://rss.torrentleech.org/SYNTHETICRSSKEY`;
- `feed.xml` — synthetic items that preserve the observed RSS 2.0 envelope;
  the rsskey, titles and hashes are placeholders.

`login-ok.html` and `login-failed.html` are synthetic pages used for the auth
phase: a logout link and a `div.alert-danger` respectively.

Both the login+search definition and the RSS definition stay "in prova" until
the coordinator verifies login, search and a grab with an account.
