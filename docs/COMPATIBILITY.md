# Compatibility and limits

indexarr speaks **Torznab** for torrents and **Newznab** for Usenet. This is the
real picture, not a list of intentions.

The catalog ships **more than a hundred site definitions** — most of them
verified, a few still drafts — plus family connectors (UNIT3D, Gazelle,
NexusPHP, TorrentTrader, Xbtit) and generic Torznab/Newznab/RSS endpoints. The
complete, generated list with access method, language and state for every site
is **[SUPPORTED.md](SUPPORTED.md)**; it is regenerated in the same commit that
adds or removes a provider, and a test fails while it is stale.

Among the best known: 1337x, The Pirate Bay, YTS, EZTV, LimeTorrents, Torlock,
TorrentFunk, YourBittorrent, Nyaa, Sukebei, AnimeTosho, RuTracker and RuTor for
Russian content, TorrentGalaxy, IPTorrents, TorrentLeech (draft), and — Italian
— MirCrew, ExtTo, CorsaroBlu, ilDraGoNeRo and DDUnlimited (eD2k).

| Source | Support | How |
| --- | --- | --- |
| MirCrew | complete | native definition, login, search, magnet at grab with thanks; Smartfeed for new topics |
| Direct Torznab | complete (torrent) | full or base endpoint; `/api` is added if missing |
| ExtTo | complete | dedicated HTML parser: title and magnet from the same row |
| TorrentGalaxy | complete | parser of `post-detail` pages, with magnet cache |
| CorsaroBlu | listing only | parser of `/torrent/` titles and direct magnets; the site disables guest keyword search and currently requires a FlareSolverr session. `CorsaroBlu (account)` with a pasted cookie is still a draft |
| ilDraGoNeRo | complete (account) | phpBB login or session cookie, search with the forum flood control, magnet at grab after the thanks |
| DDUnlimited | complete (eD2k, account) | phpBB login or session cookie, search from the title indexes, `ed2k://` links per file, flood control respected |
| IPTorrents | complete (account) | session cookie imported from the browser (the form login has Cloudflare Turnstile) |
| TorrentLeech | draft | login or RSS key; the session drops under load and is renewed |
| Newznab / Usenet | complete (generic) | `newznab-generic`: caps, `search`/`tv`/`movie`/`music`/`book` search, `.nzb` and sending to SABnzbd/NZBGet. Native definitions for individual Usenet sites not yet in the catalog |
| eD2k / aMule | complete | `protocol: ed2k` definitions, stored `ed2k://` links, expanded per file, sent to aMule |
| RSS / Atom | complete | normal feeds, `.torrent`, `.nzb` or magnet enclosure |
| Private indexers with Basic | complete | HTTP username/password fields per source |
| API key in query, header or URL | complete | `apikey` as a parameter and as `X-Api-Key` |
| Cloudflare / challenge | with FlareSolverr | automatic retry for RSS and direct indexers; LAN addresses do not go through the solver. Solves are queued: at most two browsers are asked at once, so a cycle over many protected sites cannot make the service unresponsive |
| Proprietary APIs not Torznab | not automatic | the source stays queued until a native definition exists |

**Jackett and Prowlarr are not sources**: indexarr replaces them. There is no
import that reads them and no proxy is created towards them. Sources are added
from the native catalog; a source with no native path remains "to be written"
and does not appear in the user list.

**Cardigann**: the `indexarr-def convert-cardigann` converter helps study a
definition, but the format is not executed at runtime and the derived
definitions do not enter the catalog.

**"No results" is not a failure**: a definition can list the HTTP statuses its
site uses for a valid search with no matches (`search.empty_status: [404]`).
The search then returns zero rows instead of an error, so a source is not
marked as broken and its circuit breaker is not touched. Without the
declaration the same answer stays an error: a moved endpoint must be visible.

Details, edge cases and diagnostics: [Compatibility](https://github.com/buzzqw/indexarr/wiki/Compatibility).

## Limits

- One process per database (SQLite with a serialized connection).
- It is not a BitTorrent, Usenet or eD2k client: it does not download or seed
  the content.
- The filter by external ids works on releases that expose those ids; in their
  absence it falls back to the title.
- RSS categories are numeric only: the textual categories of feeds do not
  produce a valid Torznab id and are ignored; **Default category** covers feeds
  and listings that do not declare one.
- Live search queries only sources that can search (native definitions,
  families, Torznab/Newznab); RSS feeds and uploader lists feed the cache but
  do not answer a query. Manual search accepts external ids and music/book fields, but does not
  translate an id into a title by itself for sites that do not support it.
- The manual live view shows at most 50 rows and does not use normal
  pagination; indexers that time out continue in the background and require a
  reload to show the results.
- Restoring a backup requires restarting the process; with systemd
  `Restart=always` it is automatic, otherwise it must be restarted manually.
- Seeders not reported by the indexer (for example MirCrew) remain unknown: the
  `seeders` attribute is omitted in the Torznab response instead of being 0.
- Languages come from the definition's optional `languages` row field, which
  may carry codes or names ("ita, eng"); the stored list is the English names,
  and the Torznab output is one `language` attribute each. The web filter by
  language looks at the declared languages first and falls back to the title,
  because most catalog definitions do not expose the field yet.
- Freeleech is per release when the source states it: `freeleechknown=1` and
  `freeleech` are emitted, and `downloadvolumefactor` is 0/1. When the source
  says nothing the configured download factor is the fallback. A release a
  client grabbed is kept by the retention pass even when older than the window,
  so it is not indexed as new and grabbed again when it reappears in a feed.
- Cloudflare is handled with FlareSolverr for RSS and direct indexers;
  proprietary APIs stay queued until a native definition exists, and indexarr
  does not include a headless browser.
- The catalog does not yet have native definitions for individual Usenet
  sites: the generic Newznab path is complete, site coverage grows with the
  catalog.
- Download clients can be pinned per indexer, but there is no
  priority/round-robin among clients of the same protocol: the default, then
  the oldest enabled client, is chosen.
- Notifications have a single global set of events: there is not yet an
  event × channel matrix.
- SMTP supports STARTTLS, not implicit TLS on port 465.
