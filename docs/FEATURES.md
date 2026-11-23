# Features

Everything indexarr does, in detail. The [README](../README.md) has the short
version.

## Acquisition

- Catalog of **native definitions** and family connectors (UNIT3D, NexusPHP,
  XBTIT, Gazelle, TorrentTrader, Newznab, Torznab, RSS).
- **RSS/Atom** feeds, bulk import of RSS and OPML lists, **Quick add** and
  **Discover feeds**.
- Native definitions for the Italian sites: ExtTo, TorrentGalaxy, CorsaroBlu,
  Knaben (ITA feed), **MirCrew** (with the Smartfeed source), **ilDraGoNeRo**
  and **DDUnlimited** (eD2k).
- Any direct **Torznab** or **Newznab** endpoint.
- **eD2k** sources (`protocol: ed2k`) with `ed2k://` links.
- Each source has its own interval, starts immediately on first boot, retries
  with backoff and can be slowed down with **Reduce the frequency** after an
  HTTP 429. A failure may delay a source but never makes it run sooner than
  its own interval.
- **One site, one voice**: the sources that share a definition (the thirteen
  TorrentGalaxy uploaders, the ExtTo channels) take turns inside the site's
  interval, and a periodic pass reads at most one of them at a time. The site
  never receives thirteen crawls in the same minute.
- **Mirrors**: when a site's address is gone, the request moves to the next
  address of the definition and stays there.
- **Per-host rate limit**, **circuit breaker** and **learned pace**: a host
  that keeps failing is paused and retried by itself; after a 429 the periodic
  runs of that host are spaced further apart and the pause grows (10 minutes,
  20, 40, up to six hours) instead of poking the site every ten minutes, while
  a live app search keeps its own window. Forum flood controls (DDUnlimited,
  ilDraGoNeRo) are respected with a queued search lane.

## Cache and search

- Title, size, seeders/leechers, category, date, GUID, infohash, external ids
  (IMDb/TMDB/TVDB), magnet, `.torrent`, `.nzb` and eD2k links.
- **Deduplication** by infohash when available, otherwise by GUID/URL/title.
- **FTS5** with trigram tokenizer: it also finds substrings; short terms use a
  `LIKE` fallback.
- Search by **RE2 regex**, exclusion `-term`, search in torrent file names,
  filters by quality (a value with `+`, such as `1080p+`, keeps that resolution
  and the higher ones), season, source, language, seeders and size. The
  filters are always visible, not hidden behind a panel to open.
- **Saved searches**, favorites, hidden releases and blocklist.
- Automatic **retention** and manual cleanup.

## Torznab and Newznab

- Aggregated `/api` endpoint and per-indexer `/torznab/{slug}/api`.
- `caps`, `search`, `tvsearch`, `movie`, `music`, `book` and `get` functions.
- `.torrent` and `.nzb` download, pagination, standard categories, season,
  episode and external ids.
- **Audio languages** per release: a definition that reads them emits one
  `language` attribute per language, with the English name the *arr clients
  map (Sonarr/Radarr `IsoLanguages.FindByName`), so a custom format can match
  a release whose title does not say it. A source that does not declare them
  omits the attribute instead of guessing from the title. A Torznab/Newznab
  source keeps the languages and the freeleech flag it reads from upstream, so
  a chain indexarr → indexarr (or Prowlarr → indexarr) does not lose them.
- **Live search** on every query (on by default): the enabled sources that can
  search are asked at that moment, what arrives in time is saved and merged
  with the cache; the aggregated endpoint returns one row per release.
  `cache=false` forces a fresh fan-out for one call even when live search is
  off, and `cache=only` returns only the stored answer even when it is on.
  The requested `limit` is capped by the smallest `<limits max>` declared by
  the selected sources, so a client cannot ask a site for more rows than it
  will return. An ID-only request (`imdbid`, `tmdbid`, `tvdbid`) borrows the
  title of a cached release for the live fan-out, so keyword-only sources join
  it instead of being skipped; the stored answer keeps the id filter.
- Jackett-compatible aggregate filters: `tag`, `indexer_type`, `lang`,
  `status=healthy|failing|unknown` (the same health model as the web UI) and
  `test=passed|failed` (the last health check). `status=enabled|disabled`
  keeps working.
- **Per-request override of the saved "freeleech only"**: `freeleech=0` (or
  `ignore_filters=1`) returns everything for that call, `freeleech=1` asks for
  free releases even when the source is not configured for it, and the source
  setting stays untouched. `freeleech=1` has no effect on a definition that
  cannot mark free releases: returning nothing would be a lie, not a filter.
- **Usenet failover headers**: a proxied `.nzb` keeps the upstream
  `X-DNZB-*` headers, so SABnzbd's "use alternative NZB" and NZBGet's
  FailureLink work through indexarr.
- **Stable identity**: the `<guid>` of a release is assigned on its first
  sighting (infohash, eD2k hash or a fallback key) and never changes when a
  later download adds the infohash. After a merge, the GUIDs a client already
  saw stay resolvable for `t=get`.
- Multiple API keys, per-key revocation and per-key rate limit.
- **Remote catalog**: *Settings → Advanced → Definitions* (and
  `POST /api/v2/catalog/import`) reads a catalog directory from an address —
  `index.json` plus `definitions/<id>.yaml` — validates every document and
  stores the ones that changed. A site fix no longer needs a new release; the
  definitions you created or edited are never touched, and the embedded
  catalog never overwrites what the import wrote.
- Administrative JSON API on `/api/v2/`, with the read-only
  Prowlarr-compatible endpoints that dashboards (Homepage, Organizr) read:
  `/api/v1/system/status`, `/api/v1/health`, `/api/v1/indexer`,
  `/api/v1/indexer/{id}` and `/api/v1/indexerstatus` (the sources with a
  failure entry, in Prowlarr's shape: the timestamps and, in the extra
  `message` field, the reason, since Prowlarr keeps it only in its log).

## Web interface

- **Guided first start** that asks for the address the apps must use; it can
  be reopened from Settings.
- **Home** with source status, latest releases and runs.
- **Search** with two separate searches, each with its own heading and button:
  **live on the indexers** (free text, TV, movies, music and books,
  IMDb/TMDb/TVDb ids, selection of multiple indexers, categories) and **the
  stored catalogue** (filters, saved searches and **bulk send** to clients).
  Every result names its source and can be sent to the client from the row; a
  release already sent shows a **Sent** chip with the time, so it is not
  grabbed twice by mistake.
- **Indexers** with a single list, Run/Test/Run all/Test all/Test problems,
  detail, edit, clone, JSON export/import, feed discovery and cookie import.
  The mass actions keep each site's interval and anti-flood limit.
- **App** to connect and synchronize Sonarr, Radarr, Lidarr, Readarr,
  Whisparr, Mylar3 and LazyLibrarian, the generic **Servarr-compatible** type
  for the forks of the family (Chaptarr, Listenarr, Gamarr, Sportarr, ...), and
  to manage the download clients. A source can pin its own download client.
- **Statistics** with app requests, the overview per app and indexer,
  downloads, the read-only live queue of the configured clients, run history,
  new releases and network counters.
- **Logs** with the system (process) log only.
- **Settings** for configuration, with the **Rules** (blocklist),
  **Backup** and **Maintenance** sections.
- The **version** in the bottom-left corner (top right on narrow screens)
  opens the GitHub repository at
  [github.com/buzzqw/indexarr](https://github.com/buzzqw/indexarr).
- Interface translated into Italian, English, German, French, Spanish, Polish
  and Chinese.

### Pages

| Page | Content |
| --- | --- |
| **Home** | Totals, indexer status, latest releases, runs and problems |
| **Search** (`/releases`) | Two searches: live on the indexers (text, TV, movies, music, books, external IDs) and the stored catalogue (`-xxx` exclusion, regex, advanced filters, saved searches, favorites/hidden, bulk grab, bulk curation to add or remove favorites, hide, show again and block many rows at once, **column preferences** stored in the browser to show/hide and reorder the release columns, and **CSV/JSON export** of the current filtered results, capped at 5000 rows); detail with magnet, `.torrent` and `.nzb`, plus TMDB/TVDB search buttons on every row with the title cleaned of technical tags and localized titles, the source indexer and a direct **Send** action |
| **Indexers** | Single list, Run/Test/Run all/Test all/Test problems, detail with Site access, edit, clone, JSON export/import, feed discovery and cookie import; each source can have its own request timeout |
| **App** | Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3, LazyLibrarian and the generic Servarr-compatible type; sync, per-app profile, snapshots and download clients |
| **Statistics** (`/activity`) | Incoming requests, overview per app and indexer, downloads served per source, completion reported by the client, the live **Queue** of the configured clients, update history, new releases, per-host network counters |
| **Logs** (`/logs`) | System log with filters and runtime level |
| **Settings** | Sections General, Network (FlareSolverr, per-host rate limit), Security, Notifications, **Rules** (blocklist by infohash, title, regex, source or category), **Backup**, **Maintenance** (integrity, vacuum, optimize/checkpoint, reindex, deduplication, `.torrent` cleanup, retention, purge per indexer, restore), Advanced (circuit breaker, live search, downloads through indexarr, definitions, **definition debugger** to test a row selector, a field extractor and a regex on a pasted page, proxies) and Updates |

The old `/rules`, `/maintenance` and `/definitions` addresses redirect to the
matching Settings section.

The interface is a **PWA**: manifest, icon and service worker that caches only
static assets; HTML pages always come from the network because they contain
CSRF and private data.

Search syntax, saved searches, rules, sending to clients and notifications:
[Web interface](https://github.com/buzzqw/indexarr/wiki/Web-Interface).

## Per-indexer management

- **Site access** section on every form and overview, with the **access
  method** (username and password, session cookie, API key, passkey, key in
  the personal address, no account) and the fields it needs.
- **Session cookie as an alternative to the login** on every form-login
  definition: either the username and password or a cookie pasted from the
  browser is enough.
- **HTTP/HTTPS/SOCKS4/SOCKS4A/SOCKS5** proxies, defined once on the **Proxies**
  page (Settings → Advanced) and assigned per indexer, with credentials in the
  vault.
- Query/grab limits, minimum seeders, seed ratio and seed time.
- Priority, tags, interval and User-Agent per source.
- **Freeleech only** when the definition supports it; a client can override it
  for a single Torznab request (`freeleech=0|1`, `ignore_filters=1`).
- **Import cookie** from the Firefox profile for sites that bind the session to
  the browser, such as IPTorrents.
- **A problem is what is happening now**: the stored error of a source is
  cleared as soon as a health check or a run succeeds, so a timeout from hours
  ago does not keep the dashboard red, and every cause (rate limit, flood
  control, DNS, page changed) has its own sentence instead of "unexpected
  error". A failure inside indexarr — storage, configuration, result handling
  — is recorded as **internal** and kept apart: it explains itself in the
  history, without degrading the source, feeding the auto-disable count or
  sending a status notification. The health check and the periodic runs keep the sources of one site
  apart: they are checked one after the other, and even when several are due
  only one is read per tick, so a site is never asked for everything at the
  same second.

## \*arr apps

- Sync to Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3 and LazyLibrarian,
  plus the generic **Servarr-compatible** type: a fork of the family works
  without waiting for a new version of indexarr.
- **Resume now** on a source paused after repeated failures: the pause is
  cleared from the page, without editing or recreating the indexer.
- Levels **Disabled**, **Add/remove only** and **Full sync**.
- Per-application profile: RSS, automatic search, interactive search, minimum
  seeders and Torznab categories to send.
- Tag filter, snapshot before changes and restore of the previous state from
  the UI.

## Download clients

Supported:

| Client | Protocol | Notes |
| --- | --- | --- |
| qBittorrent | torrent | Web API v2, category, path, pause (4.x `paused` and 5.x `stopped`), seed ratio/time |
| Gextto / gx-torrent | torrent | qBittorrent-compatible `/api/v2`, password or API key |
| Transmission | torrent | RPC, magnet and `.torrent` |
| Deluge | torrent | JSON-RPC |
| rTorrent | torrent | XML-RPC |
| Aria2 | torrent | JSON-RPC, token, `dir`, pause |
| uTorrent | torrent | Web API, token, magnet and file |
| Flood | torrent | REST, tag and destination |
| Download Station | torrent | Synology `auth.cgi` + `task.cgi` |
| Hadouken | torrent | JSON-RPC, label |
| Torrent Blackhole | torrent | writes `.torrent` or `.magnet` to a folder |
| SABnzbd | Usenet | API, `.nzb`, category, pause |
| NZBGet | Usenet | JSON-RPC, `.nzb`, category, pause |
| NZBVortex | Usenet | API, `.nzb`, group |
| Usenet Blackhole | Usenet | writes `.nzb` to a folder |
| aMule | eD2k | Web API, `ed2k://` links |

The new-client form fills the name with the selected client and updates the
gray examples for the address, username, category and save folder as soon as
the type changes. The **Test client** button checks the address, credentials
and type with the values typed above, before saving; the client list keeps the
last test time and result. The default client receives the grabs; each indexer
can pin its own client, and from the Releases page you can send one release
from its row or several with **Send selected**. The source seed ratio/time
travels to qBittorrent and to the \*arr apps; the other clients keep their own
values.

Statistics → **Queue** reads the live queue of qBittorrent, Gextto /
gx-torrent, Transmission, Deluge, SABnzbd and NZBGet. It is read-only: progress,
speed and ETA are what the client reports. The other client types show a note
instead of pretending the queue is empty. The qBittorrent row accepts the
number-as-string fields some builds return and shows the 5.x `stopped` states
as **paused**, so a client upgrade does not empty the queue or change its
words.

A delivery is not a completed download. A client that knows when the file
arrives can report it to `POST /api/v2/downloads/confirm`, with the same API key
as the Torznab endpoint: the Download tab then shows **downloaded** instead of
**delivered only**. Gextto already does it; the contract is in
[Download confirmation](DOWNLOAD_CONFIRMATION.md).

## Notifications

Independent channels, each with its own encrypted secrets:

- JSON webhook, Telegram, Apprise, Discord, Slack, Gotify, ntfy, Pushover,
  SMTP email and custom script.

Available events: new releases, indexer status changes, failed grab, successful
grab, catalog updated and failed backup. There is an anti-spam interval per
event and a test button that checks all configured channels.

## Operations

- Multiple API keys with revocation and rate limit.
- Auto-disable after N consecutive errors.
- Automatic backups with rotation and restore from the UI.
- Prometheus metrics on `/metrics`.
- In-app logs, rotating log file, `journalctl` with systemd.
- Installable PWA; HTML pages always come from the network.

## Sources

Each source can have **HTTP Basic** credentials independent of the Torznab API
key; **Test** and **Run** work per source, **Test all** and **Run all** on every
enabled source. The catalog, RSS/Atom, direct Torznab, Usenet/Newznab, eD2k,
the native HTML sites, private sources with cookies and forums,
Cloudflare/FlareSolverr and the Jackett/Prowlarr replacement:
[Sources](https://github.com/buzzqw/indexarr/wiki/Sources).

A **Rats Search node** is one more source: the catalog entry `Rats Search
(generic)` takes the node's REST address, no key, and queries the BitTorrent
index the node crawled from the DHT. It is a keyword source like the public DHT
search engines in the catalog — one word filter and a coarse content type, no
`tvdbid`, no category tree — and a release is grabbed as a magnet. The
periodic walk reads the node's newest torrents, so a source added today fills
the cache from the next run; **Test** reports how many torrents the node holds.

## Backup, maintenance and security

- **Backups** run automatically with rotation and can be restored from the UI;
  the vault key is not in the database and must be kept separately.
- **Maintenance**: database report, integrity/quick/foreign-key checks, vacuum,
  optimize/checkpoint, analyze, rebuild indexes, deduplication, `.torrent`
  cleanup, retention and purge per indexer.
- **Diagnostics**: Prometheus metrics on `/metrics`, run history, per-host
  counters and the system log with filters and runtime verbosity.
- **Security**: the Torznab API key is mandatory, the UI can use HTTP Basic,
  secrets live in the vault and all POST forms use CSRF. The UI is open until
  you set a username and password in **Settings**: do that before exposing it
  to the LAN. An optional **Trusted hosts** list in *Settings → Security*
  accepts only the listed names in the Host header, so a forged host cannot
  poison the download and public links; empty keeps every host, and loopback is
  always accepted for local health checks.

Backup and restore, maintenance, statistics, logs and security:
[Operations](https://github.com/buzzqw/indexarr/wiki/Operations).
