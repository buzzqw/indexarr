# indexarr

**Self-hosted torrent and Usenet indexer, Torznab/Newznab compatible.**
indexarr builds a local index of releases, keeps it up to date and exposes it
to Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3, LazyLibrarian or any
Torznab client. It was created to replace Jackett and Prowlarr in the daily
workflow: native sources, SQLite cache, live search, app synchronization and a
complete web interface, with no mandatory external services.

It does not download the content of torrents or NZBs: it builds a searchable
**index**. Downloading remains with the chosen client — qBittorrent,
Transmission, Deluge, rTorrent, SABnzbd, NZBGet, aMule and others.

```
Native sources / RSS / Newznab ──► scheduler ──► normalization ──► SQLite
                                                                      │
       Sonarr / Radarr / Lidarr / Readarr ◄── Torznab / Newznab ──────┤
                          Browser ◄── web UI SSR + JSON API ──────────┘
```

## Why indexarr

- **A single Go binary**, SQLite without CGO, no mandatory database or service.
- **Native catalog** of definitions and family connectors; no proxy to Jackett
  or Prowlarr.
- **Real cache**: releases remain available even when a site is slow or
  unreachable, with deduplication, full-text search and retention.
- **Torznab and Newznab** on the same endpoint, for torrents, Usenet and eD2k.
- **Web interface** to search, manage sources, synchronize apps, send to
  clients and perform maintenance.
- **Operational security**: multiple API keys, rate limit, vault for secrets,
  CSRF, backup and restore from the UI.

## Features

### Acquisition

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
  HTTP 429.
- **Mirrors**: when a site's address is gone, the request moves to the next
  address of the definition and stays there.
- **Per-host rate limit** and **circuit breaker**: a host that keeps failing
  is paused and retried by itself; forum flood controls (DDUnlimited,
  ilDraGoNeRo) are respected with a queued search lane.

### Cache and search

- Title, size, seeders/leechers, category, date, GUID, infohash, external ids
  (IMDb/TMDB/TVDB), magnet, `.torrent`, `.nzb` and eD2k links.
- **Deduplication** by infohash when available, otherwise by GUID/URL/title.
- **FTS5** with trigram tokenizer: it also finds substrings; short terms use a
  `LIKE` fallback.
- Search by **RE2 regex**, exclusion `-term`, search in torrent file names,
  filters by quality, source, language, seeders and size.
- **Saved searches**, favorites, hidden releases and blocklist.
- Automatic **retention** and manual cleanup.

### Torznab / Newznab

- Aggregated `/api` endpoint and per-indexer `/torznab/{slug}/api`.
- `caps`, `search`, `tvsearch`, `movie`, `music`, `book` and `get` functions.
- `.torrent` and `.nzb` download, pagination, standard categories, season,
  episode and external ids.
- **Live search** on every query (on by default): the enabled sources that can
  search are asked at that moment, what arrives in time is saved and merged
  with the cache; the aggregated endpoint returns one row per release.
- Multiple API keys, per-key revocation and per-key rate limit.
- Administrative JSON API on `/api/v2/`, with the read-only
  Prowlarr-compatible endpoints that dashboards (Homepage, Organizr) read.

### Web interface

- **Guided first start** that asks for the address the apps must use; it can
  be reopened from Settings.
- **Home** with source status, latest releases and runs.
- **Search** with manual live search: free text, TV, movies, music and books,
  IMDb/TMDb/TVDb ids, selection of multiple indexers, categories, filters,
  saved searches and **bulk send** to clients.
- **Indexers** with a single list, Run/Test/Run all/Test all/Test problems,
  detail, edit, clone, JSON export/import, feed discovery and cookie import.
- **App** to connect and synchronize Sonarr, Radarr, Lidarr, Readarr,
  Whisparr, Mylar3 and LazyLibrarian, and to manage the download clients.
- **Statistics** with app requests, the overview per app and indexer,
  downloads, run history, new releases and network counters.
- **Logs** with the system (process) log only.
- **Settings** for configuration, with the **Rules** (blocklist),
  **Backup** and **Maintenance** sections.
- Interface translated into Italian, English, German, French, Spanish, Polish
  and Chinese.

### Per-indexer management

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
- **Freeleech only** when the definition supports it.
- **Import cookie** from the Firefox profile for sites that bind the session to
  the browser, such as IPTorrents.

### \*arr apps

- Sync to Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3 and LazyLibrarian.
- Levels **Disabled**, **Add/remove only** and **Full sync**.
- Per-application profile: RSS, automatic search, interactive search, minimum
  seeders and Torznab categories to send.
- Tag filter, snapshot before changes and restore of the previous state from
  the UI.

### Download clients

Supported:

| Client | Protocol | Notes |
| --- | --- | --- |
| qBittorrent | torrent | Web API v2, category, path, pause, seed ratio/time |
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

The default client receives the grabs; from the Releases page you can send
multiple releases with **Send selected**. The source seed ratio/time travels to
qBittorrent and to the \*arr apps; the other clients keep their own values.

### Notifications

Independent channels, each with its own encrypted secrets:

- JSON webhook, Telegram, Apprise, Discord, Slack, Gotify, ntfy, Pushover,
  SMTP email and custom script.

Available events: new releases, indexer status changes, failed grab, successful
grab, catalog updated and failed backup. There is an anti-spam interval per
event and a test button that checks all configured channels.

### Operations

- Multiple API keys with revocation and rate limit.
- Auto-disable after N consecutive errors.
- Automatic backups with rotation and restore from the UI.
- Prometheus metrics on `/metrics`.
- In-app logs, rotating log file, `journalctl` with systemd.
- Installable PWA; HTML pages always come from the network.

## Compatibility

indexarr speaks **Torznab** for torrents and **Newznab** for Usenet. This is the
real picture, not a list of intentions:

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
| Cloudflare / challenge | with FlareSolverr | automatic retry for RSS and direct indexers; LAN addresses do not go through the solver |
| Proprietary APIs not Torznab | not automatic | the source stays queued until a native definition exists |

**Jackett and Prowlarr are not sources**: indexarr replaces them. There is no
import that reads them and no proxy is created towards them. Sources are added
from the native catalog; a source with no native path remains "to be written"
and does not appear in the user list.

**Cardigann**: the `indexarr-def convert-cardigann` converter helps study a
definition, but the format is not executed at runtime and the derived
definitions do not enter the catalog.

Details, edge cases and diagnostics: [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md).

## Requirements

- **Go 1.26 or later** to build. The binary has no runtime dependencies.
- A writable filesystem for the database.
- Optional: FlareSolverr for Cloudflare, a torrent/Usenet/eD2k client for
  sending releases.

## Quick start

```bash
make build
./bin/indexarr
```

On first start, in the current directory, the following are created:

- `indexarr.json` — minimal configuration (port, database, logs);
- `indexarr.db` — SQLite database;
- a random Torznab API key, printed in the log and visible in **Settings**.

Open <http://localhost:8788>, add a source and press **Run**. In **Settings**
you find the Torznab URL to give to the apps.

Available flags:

```text
-config string   JSON configuration file (default "indexarr.json")
-listen string   HTTP address, overrides the file
-db string       SQLite path, overrides the file
-version         print the version
```

## Configuration

`indexarr.json` contains only the startup settings:

```json
{
  "listen": ":8788",
  "db_path": "indexarr.db",
  "log_level": "info",
  "vault_key_path": "",
  "memory_limit_mb": 64,
  "log_file": "logs/indexarr.log",
  "log_max_mb": 10,
  "log_max_files": 5
}
```

A relative `db_path` is resolved against the directory of the configuration
file. `vault_key_path` is optional: if empty the vault key is looked up in
`INDEXARR_MASTER_KEY`, `INDEXARR_MASTER_KEY_FILE`, legacy `<db>.key` (if it
exists) and then `os.UserConfigDir()/indexarr/master.key`. A relative path is
resolved against the directory of `indexarr.json`. Details, `vault export-key`
/ `vault move-key` commands, backups and Docker secrets in
[docs/VAULT.md](docs/VAULT.md).

`log_file` is the rotating log file (relative like `db_path`; `"off"` turns it
off): when it exceeds `log_max_mb` it becomes `indexarr.log.1`, older files
shift and `log_max_files` files in total are kept. An old configuration file
without these fields uses the defaults.

Everything else — API keys, intervals, retention, UI credentials, notifications,
clients, backups — is in the database and is changed from the **Settings** page,
without restarting.

### Main settings

| Setting | Default | Meaning |
| --- | --- | --- |
| `default_interval_seconds` | 3600 | Interval of sources without their own |
| `fetch_timeout_seconds` | 45 | Timeout for each HTTP request |
| `workers` | 2 | Sources run in parallel (requires restart) |
| `max_torrents_per_run` | 30 | Limit of `.torrent` files downloaded per run |
| `max_torrent_bytes` | 8 MiB | Maximum size of a `.torrent` |
| `fetch_torrents` | on | Downloads `.torrent` files when the source only offers a link |
| `retention_days` | 0 | Deletes releases no longer seen after N days (0 = never) |
| `public_url` | empty | Public URL for Torznab links behind a reverse proxy |
| `notify_*` | — | Notification channels and triggers |
| `auto_disable_after` | 0 | Disables a source after N consecutive errors (0 = never) |
| `flaresolverr_url` | empty | FlareSolverr endpoint for RSS and direct indexers |
| `torznab_download_factor` | 0 | `downloadvolumefactor` (0 = freeleech) |
| `torznab_upload_factor` | 1 | `uploadvolumefactor` |
| `api_rate_limit_per_minute` | 0 | Requests per API key per minute (0 = unlimited) |
| `flaresolverr_timeout_seconds` | 60 | Time FlareSolverr may spend on one Cloudflare check |
| `torznab_live_search` | on | Queries the indexers live on every search with a query |
| `torznab_live_timeout_seconds` | 20 | Overall deadline of the live search |
| `torznab_live_wait_seconds` | 5 | How long the answer waits for the live results; late indexers finish in the background |
| `torznab_live_cache_seconds` | 300 | How long a live result stays cached (0 = off) |
| `torznab_proxy_magnets` | off | Magnet grabs go through indexarr and are recorded in **Statistics → Downloads** |
| `torznab_proxy_ed2k` | on | eD2k grabs go through indexarr; the direct link stays in the `ed2k` attribute |
| `host_rate` / `host_burst` | 1 / 4 | Requests per second per host, shared by all indexers, and initial burst |
| `breaker_threshold` | 5 | Consecutive failures before a host is paused |
| `breaker_cooldown` | 600 s | Pause after repeated failures or a 429/503 without `Retry-After` |
| `seeders_ttl_hours` | 6 | Age of the seeders before they are refreshed |
| `history_retention_days` | 30 | Retention of run metrics, app searches and samples (0 = forever) |
| `stored_source_limit` | 3 | Soft limit of search-only sites whose results are stored (warning only) |
| `stored_retention_days` | 90 | Retention of releases that came only from those sites (0 = forever) |
| `download_client` | none | Legacy client; modern clients are on the App page |
| `backup_enabled` | on | Automatic database backup |
| `backup_interval_hours` | 24 | Interval of automatic backups (6–168) |
| `backup_keep` | 7 | Number of automatic backups kept (1–60) |
| `log_level` | config | Runtime level (`debug`, `info`, `warn`, `error`) |
| `memory_limit_mb` | config | Soft Go memory limit in MiB (0 disables it) |

## Sources

Each source can have **HTTP Basic** credentials (*HTTP username* and *HTTP
password* fields), independent of the Torznab API key. Next to each source
there are **Test** and **Run**; **Test all** and **Run all** work on all
enabled sources and write the outcomes to **Statistics → Runs** and the logs.

### Catalog and adding

- **Add indexer**: choose a catalog definition (ExtTo, TorrentGalaxy, CorsaroBlu,
  Knaben, TorrentLeech, a generic one) or paste a Torznab endpoint.
- **Quick add** recognizes Torznab, Newznab, RSS and HTML listings.
- **Discover feeds** extracts the feeds declared by a page.
- **Export/import JSON** saves and reloads the public configuration of the
  indexers; secrets are never exported.
- Sources not ready yet stay queued and are not offered in the user list; the
  maintenance view `/indexers/new?queue=1` also shows the composition of the
  queue.

As of **2026-10-29** the catalog contains **627 entries**: 111 native
definitions, 86 verified (85 torrent + 1 eD2k), 92 verified public hosts and
495 entries still to try, mostly private. Numbers by state in
[docs/CATALOG.md](docs/CATALOG.md). Coverage of individual sites grows
with the catalog; the generic Usenet path is already complete.

### RSS / Atom

- **Type**: RSS / Atom feed
- **URL**: the feed
- Title, magnet, `.torrent`/`.nzb` enclosure, size, numeric category, date and
  any `torznab:attr` attributes are read.
- Entries without a magnet or download link are ignored.
- **Default category** assigns a Torznab category to feeds and listings that
  do not declare one.

### Freeleech only

When the definition provides for it, the indexer form shows the **Freeleech
only** checkbox (`setting.freeleech`). If the site accepts a filter in the
search, the definition passes it as a parameter (for example `free=1`); if the
site marks each row, the engine discards non-free rows before saving them and
before answering the apps. The option appears only for definitions that
declare it and also applies to the new-release list and to the live search.

### Direct Torznab

- **URL**: full endpoint, for example `http://indexer.local:8080/api`
- **Manager**: Direct Torznab (or Auto)
- **Categories**: comma-separated Torznab ids (`5000, 2000`)

### Usenet / Newznab

- **Type**: Newznab / Usenet
- **URL** and **API key** of the service
- **Categories**: standard Newznab ids
- The protocol is `usenet`; the payload is an `.nzb` and sending goes to
  SABnzbd or NZBGet.
- Native definitions for individual Usenet sites are not yet in the catalog: a
  source that exposes Newznab works immediately, the others stay queued.

### eD2k / aMule

Definitions with `protocol: ed2k` publish
`ed2k://|file|name|size|hash|/` links. indexarr stores them, expands them per
file and sends them to aMule through the Web API. The link can come from the
page body or from an eD2k field; topics with multiple files produce one release
per file. eD2k remains excluded from synchronization with the \*arr apps.

### HTML sites: ExtTo, TorrentGalaxy, CorsaroBlu

The three sites have native definitions with real fixtures and equivalence
tests. The engine follows detail links to get the magnet, with limited
concurrency and cache. CorsaroBlu is a **listing-only** source for guests: no
username, password or cookie is needed to read its latest list and magnets.
The site disables guest keyword search, so indexarr does not send a query. The
`CorsaroBlu (account)` variant accepts a **pasted session cookie** for keyword
search; it remains a draft until an account can verify it. In the UI, open
**Add indexer**, find the CorsaroBlu card and follow **Add with account login
(optional)** (or open `/indexers/new?definition=corsaro-account`). The username
and password fields do not exist: the site's login is protected by reCAPTCHA, so
paste the browser's `Cookie` request header into **Session cookie**, or leave it
empty and press **Create and import cookie**: indexarr creates the indexer and
shows a one-line `curl` command. Run that command on the machine with Firefox;
it reads the local profile and sends the cookie. Since 29 October 2026 every
request goes through a FlareSolverr session because a plain client receives
429/503. Limits: first page, at most 50 releases per source; if the
layout changes or the site requires login, the test reports the error.

### Private sources, cookies and forums

- **Site access**: every indexer form and overview shows the access method and
  its fields in a fixed order (username, password, cookie, API key, passkey).
  Public sources say they need no account; sources whose key is in the
  personal address (RSS key, Smartfeed) say so.
- **HTTP Basic** for private feeds and indexers.
- **Session cookie instead of the login**: every form-login definition run by
  the engine (MirCrew, ilDraGoNeRo, DDUnlimited, TorrentLeech, …) also accepts
  a cookie pasted from the browser. With a cookie the login form is skipped;
  when the site drops that session indexarr signs in with the username and
  password if they are set, otherwise the test reports that the pasted cookie
  has expired. Family connectors (NexusPHP, TorrentTrader, XBTIT) do their own
  login and do not offer the cookie.
- **Import cookie** for sites with a form login protected by Cloudflare
  Turnstile, such as IPTorrents: it shows a one-line command to run on the
  machine with Firefox; the helper reads the profile, sends the cookie and the
  browser User-Agent, and indexarr stores both encrypted. The indexer can be
  created first and the cookie imported afterwards.
- **MirCrew** and **ilDraGoNeRo** are phpBB forums with native definitions:
  search never thanks; the thanks to the author happens only at download time,
  when the magnet is unlocked. Details in [docs/MIRCREW.md](docs/MIRCREW.md).
- **DDUnlimited** is an eD2k forum: search reads its alphabetical title
  indexes, every file of a topic becomes a release and the forum flood control
  is respected with a single search lane and a pause after a refusal.

### Cloudflare / FlareSolverr

- Endpoint in **Settings → Cloudflare / FlareSolverr**.
- Recognizes `cf-mitigated: challenge` and Cloudflare pages on 403/503/200.
- Repeats the request through FlareSolverr for RSS and direct indexers.
- Persistent sessions are closed after `session.idle_timeout` (30 minutes by
  default); the browsers left by a stopped instance are reconciled 30 seconds
  after start and every two minutes, using only the ownership prefixes of
  indexarr.
- LAN addresses never go through the solver.
- `.torrent` files do not go through the solver: if the site requires clearance
  for the file too, use the magnet.

### Jackett and Prowlarr

indexarr **replaces** them: they are not sources and there is no import that
reads them. A URL that belongs to a manager is rejected with an explicit
message. See [docs/MIGRATION-MANAGERS.md](docs/MIGRATION-MANAGERS.md).

## Connecting the apps

In the **Settings** page copy the full endpoint:

```text
http://<host>:8788/api?apikey=<API_KEY>
```

In the client add a **Torznab** indexer:

| Field | Value |
| --- | --- |
| URL | `http://<host>:8788/api` |
| API Key | the key shown in Settings |
| Categories | `5000` TV, `2000` Movies, `3000` Audio, `7000` Books |

The `/torznab/api` and `/newznab/api` aliases and the API key in the
`X-Api-Key` header are also supported. Full details in [docs/API.md](docs/API.md).

### App sync

In **App** you connect Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3 and
LazyLibrarian. Choose the level:

- **Disabled**: no contact;
- **Add/remove only**: creates and removes the indexers;
- **Full sync**: also updates the existing indexers.

In **Advanced** you set the profile of the individual app: RSS, automatic
search, interactive search, minimum seeders (0 = indexer value) and Torznab
categories to send (empty = all supported ones). An indexer with no categories
in common is not synchronized with that app. The tag filter decides which
sources to send to which app; before every change a snapshot is saved,
restorable from the same page.

indexarr creates in the app only the indexers whose name starts with
`indexarr · `. The rest is not touched.

### Multiple API keys and rate limit

In **Settings → Torznab API** the primary key remains the one shown in the
endpoint. Below, **Additional API keys** lets you create one per client
(Sonarr, Radarr, Lidarr, a script) with name, creation date, last use and
number of requests. Each key can be revoked without touching the others;
`.torrent`/`.nzb` links use the key with which the request was made. **API
requests per key per minute** applies a limit per key (0 = unlimited): above
the threshold the response is HTTP 429 with `Retry-After`.

### Live search

With live search enabled (the default), every Torznab search that carries a
query or an external id asks the enabled sources that can search — native
definitions, families, Torznab and Newznab — at that moment. What arrives
within **Live search response wait** (5 s) is saved and merged with the cache;
the answer returns earlier when every indexer has finished. Indexers still
running continue in the background up to **Live search timeout** (20 s), so
their results are already in the cache for the next page or the next request.
A result stays cached for **Live search cache** (300 s). RSS feeds and
uploader lists do not take part because they do not accept a search term. A
request by id alone does not trigger a live search on sites that do not
support ids. Turn it off if you prefer an answer from the cache only.

## Web interface

| Page | Content |
| --- | --- |
| **Home** | Totals, indexer status, latest releases, runs and problems |
| **Search** (`/releases`) | Manual live search (text, TV, movies, music, books, external IDs), `-xxx` exclusion, regex, advanced filters, saved searches, favorites/hidden, bulk grab; detail with magnet, `.torrent` and `.nzb` |
| **Indexers** | Single list, Run/Test/Run all/Test all/Test problems, detail with Site access, edit, clone, JSON export/import, feed discovery and cookie import |
| **App** | Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3, LazyLibrarian; sync, per-app profile, snapshots and download clients |
| **Statistics** (`/activity`) | Incoming requests, overview per app and indexer, downloads served per source, update history, new releases, per-host network counters |
| **Logs** (`/logs`) | System log with filters and runtime level |
| **Settings** | Sections General, Network (FlareSolverr, per-host rate limit), Security, Notifications, **Rules** (blocklist by infohash, title, regex, source or category), **Backup**, **Maintenance** (integrity, vacuum, optimize/checkpoint, reindex, deduplication, `.torrent` cleanup, retention, purge per indexer, restore), Advanced (circuit breaker, live search, downloads through indexarr, definitions, proxies) and Updates |

The old `/rules`, `/maintenance` and `/definitions` addresses redirect to the
matching Settings section.

The interface is a **PWA**: manifest, icon and service worker that caches only
static assets; HTML pages always come from the network because they contain
CSRF and private data.

## Search in releases

The **Releases** page searches as you type: the request starts after 250 ms,
the previous one is cancelled and only the table is updated, without reloading
the page. The URL remains shareable and the form also works without JavaScript.

- **Exclusion**: `-xxx` removes the results that contain `xxx`. Multiple terms
  combine: `show -1080p -cam`.
- **Full-text**: terms of at least three characters use FTS5 with trigram
  tokenizer; substrings work too. Shorter terms use `LIKE`.
- **Regex**: check **Regex**; each term is an RE2 regular expression,
  case-insensitive by default. Terms are in AND, `-pattern` excludes, quotes
  keep spaces (`"the show"`) and `/pattern/` is accepted.
- An invalid expression shows the error without emptying the page.
- Source, category and sorting combine with the text; pagination remains on
  the full page.

### Manual live search

The **Live search** panel queries the selected sites immediately, saves what
they return and shows it below. Available modes:

- **Free search**: any title.
- **TV**: query, season, episode, year, TVDb/IMDb/TMDb id.
- **Movie**: query, year, IMDb/TMDb id.
- **Music**: artist, album, year.
- **Books**: author, title.

You can choose one or more indexers and a category. Sites that answer in time
show the results immediately; those that time out continue in the background
and may appear after a reload. The live view shows at most 50 rows and does not
use normal pagination.

### Advanced filters and saved searches

Below the search there are the structured filters:

- resolution (`2160p/4K`, `1080p`, `720p`, `480p`);
- source (`WEB-DL`, `BluRay`, `HDTV`);
- language (`Italian`, `English`);
- minimum seeders, minimum and maximum size in MB;
- **Favorites** and **Show hidden**.

They combine with the text and with regex mode. **Save this search as…** saves
the view with a name; saved searches appear above the results as ready links,
with an × to delete them. A ★ marks favorite releases.

### Rules, favorites and hidden releases

**Settings → Rules** manages the blocklist:

- by **infohash** (exact), **title** (substring), **regex** (Go/RE2,
  case-insensitive), **source name** or **category** (`5000` covers `5xxx`);
- matching releases are no longer inserted in subsequent runs;
- on a release detail there are **Favorite**, **Hide** and **Block release**:
  Block adds the rule (infohash if available, otherwise title) and hides it
  immediately.

Hiding a release removes it from the normal views but does not prevent a new
run from reinserting it: that is why the blocklist exists. Blocked releases are
counted in the **Blocked** column of the runs.

### Sending to clients

In **App → Download clients** you choose the
client, enter URL and credentials and, for qBittorrent, category and path. On
the detail of each release there is **Send**: the saved `.torrent`/`.nzb` is
sent, or the magnet if the file is not in the cache. For aMule the `ed2k://`
link is sent to the Web API (`/api/v1/downloads`); the release protocol chooses
the client.

qBittorrent uses the Web API v2; Transmission uses RPC with session negotiation.
When the indexer that provided the release has a **seed ratio** or a **seed
time**, qBittorrent receives them already at addition
(`ratioLimit`/`seedingTimeLimit`), so the torrent stops at the source limit;
the other clients keep their own values. The same `seed_ratio` travels to the
\*arr apps with application synchronization.

## Notifications and auto-disable

In **Settings → Notifications**:

- **Webhook URL**: receives a JSON POST with `event`, `source`, `count`,
  `titles`, `error`, `at`, and the already-rendered `subject` and `message`
  that the other channels show;
- **Telegram bot token** and **chat ID**: readable message; both or neither must
  be set;
- **Apprise**, **Discord**, **Slack**, **Gotify**, **ntfy**, **Pushover**,
  **SMTP email** and **custom script**: independent channels, with their own
  secrets encrypted in the vault;
- **Notify new releases**, **Notify failures**, **Grab failed**, **A grab
  succeeded**, **Catalog updated** and **Backup failed** choose the events;
- **Auto-disable after N consecutive failures**: after N failed runs the source
  is disabled; a successful run resets the counter.

A burst of successful grabs from the same application is grouped: episodes
handed over within a few seconds arrive as one message with every title in the
list, not one message per episode.

**Anti-spam** prevents the same event (type and source) from being sent more
than once in the same interval. The **Send test notification** button checks
all configured channels. Messages are in the language chosen in **Settings →
Notifications → Message language**: "Same as the interface" follows the
selector at the top, a fixed language remains even when changing the interface.

## Backup and restore

The indexarr backup contains the **SQLite database**, not the downloaded files.
Backups are kept in `backups/` next to the database (`data/backups/` with the
user service): automatic ones as `indexarr-<timestamp>.db`, the copies taken
before a restore as `pre-restore-*.db` and those of `scripts/upgrade.sh` as
`pre-upgrade-*.db`.

The **vault key is not in the database**: it must be kept separately
(`INDEXARR_MASTER_KEY`, `vault_key_path` or the user file
`~/.config/indexarr/master.key`). Without that key the stored secrets cannot be
opened. Details in [docs/VAULT.md](docs/VAULT.md).

From **Settings → Backup** you can create a backup, download it, delete it
and restore it. Restore loads a validated `.db`, copies it to
`indexarr.db.restore`, the service restarts and the file is applied before
opening the database. The current database is copied to
`backups/pre-restore-*.db`. The systemd unit uses `Restart=always` to
complete the restart; without systemd, restart manually.

## Maintenance and diagnostics

- **Database report**: sizes, free pages, WAL/SHM, row counts and release
  breakdown.
- **Integrity check**, **quick check**, **foreign key check**.
- **Vacuum**, **optimize/checkpoint**, **analyze**, **rebuild indexes**.
- **Deduplication by infohash**, `.torrent` cleanup, retention, cleanup of
  releases without links or hidden, purge per indexer.
- **Automatic backups** with rotation and restore from the UI.
- **Source health** with errors, failures and Test/Run buttons.
- **Clear source errors**, **reset API usage**, **recount source items**.
- **Prometheus metrics** on `GET /metrics` (`indexarr_releases`,
  `indexarr_indexers`, `indexarr_runs_24h`, `indexarr_errors_24h`,
  `indexarr_blocklist_rules`, `indexarr_db_bytes`, uptime…), without
  authentication like `/healthz`.
- **Runs → detail**: counts, duration and full error of a single run.
- **Cancel** in the row of a running source cancels the run.

### Statistics and log

**Statistics** has six tabs: **Requests** (incoming requests with outcome and
timings), **Overview** (per app and per indexer), **Downloads** (grabs served
per source, with a source filter and a chip that copies the magnet, eD2k link
or `.torrent` link of the release), **Runs**, **New releases** and **Network**
(per-host counters).
**Logs** holds only the system log: the last **2000** messages of the process,
most recent first. Old links (`/logs?tab=requests`, `/activity?tab=logs`, …)
redirect to the new place. The system log offers:

- filter by level (`debug`, `info`, `warning`, `error`), by **origin**
  (`indexarr`, `indexer`/Torznab, RSS/HTML, notifications) and search in the
  message and in the attributes;
- **Problems** button with the count of warnings and errors matching the
  current filters;
- row limit (100/300/1000) and **auto-refresh** every 10 seconds;
- **Download .txt** and **Clear** of the buffer;
- the outcomes of **Test all** and **Test FlareSolverr** also arrive here.

Each source error reports `source`, `kind`, `host` and the original message.
Internal errors use `component=indexarr`, indexer errors `component=indexer` and
feed errors `component=rss`. The same lines go to stdout (journal with systemd)
and to the rotating log file `logs/indexarr.log` next to `indexarr.json`: 5 files
of at most 10 MB by default.

```bash
tail -f data/logs/indexarr.log          # rotating file
journalctl --user -u indexarr -f        # systemd journal
```

Verbosity is changed from **Logs → Runtime verbosity** (`debug`, `info`, `warn`,
`error`): it is applied immediately, saved in the database and kept across
restarts. `log_level` in `indexarr.json` is only the initial value. API keys,
passwords and tokens are redacted in the buffer shown by the UI.

## Security

- The **Torznab API key** is mandatory for search and download. `t=caps` also
  answers without a key, like other Torznab clients. Additional keys can be
  revoked individually and the rate limit is per key.
- The UI accepts **HTTP Basic** if you set a username and password in Settings.
  Without credentials it is open to anyone who can reach the port: use it on a
  trusted network or put it behind a reverse proxy with TLS.
- `GET /healthz`, `GET /metrics` and `/static/*` do not go through Basic auth:
  protect the network or the reverse proxy. `/metrics` does not contain release
  data, only counts.
- All POST forms require a CSRF token tied to the browser; uploading a
  multipart backup uses the same token in the query, without reading the file
  before the check.
- Passwords are stored as `pbkdf2-sha256$iterations$salt$hash`.
- Indexer, proxy, app, client and notification secrets are in the vault, not in
  clear text in the database.
- The server does not execute code uploaded by the indexers: XML and JSON are
  only data.
- FlareSolverr receives the full indexer URL, **including the API key**, and a
  single resolution can take up to ~60 seconds.

## systemd

### User systemd service (recommended)

The script creates and starts the service for the current user, without `sudo`:

```bash
./scripts/install-user-service.sh
```

It creates:

- `~/.config/systemd/user/indexarr.service`, enabled at login;
- `data/indexarr.json` and `data/indexarr.db` with private permissions
  (700/600);
- starts the service and checks `/healthz`.

```bash
./scripts/install-user-service.sh --listen 127.0.0.1:8788   # local only
./scripts/install-user-service.sh --no-build                # use bin/indexarr
./scripts/install-user-service.sh --linger                  # stays active after logout
./scripts/install-user-service.sh --status
./scripts/install-user-service.sh --uninstall               # data is kept
```

The default is `:8788` on all interfaces: on the LAN the address is
`http://<server-IP>:8788/`. The script also prints the detected LAN address and
checks that the port responds there, not only on `127.0.0.1`. The address
passed with `--listen` is written into the unit and takes precedence over
`listen` in the configuration file. If you want to limit it to the local
machine only, use `--listen 127.0.0.1:8788` and run the script again.

The UI has no authentication until you set a username and password in
**Settings**: do that before exposing it to the LAN.

Logs and status:

```bash
systemctl --user status indexarr
journalctl --user -u indexarr -f
```

Without `--linger` the service starts with the user session and stops at
logout; `--linger` keeps it active.

### Production upgrade (data preserved)

`scripts/upgrade.sh` updates the binary **without wiping the database**. By
default it is a dry run: it validates the prerequisites, builds `cmd/indexarr`
in a temporary directory and prints the plan, without stopping the service.

```bash
./scripts/upgrade.sh                       # dry run
./scripts/upgrade.sh --apply --yes         # production upgrade
./scripts/upgrade.sh --rollback --yes      # restore the latest backup
```

With `--apply` the build happens **before** stopping the service. Then it
creates a consistent backup with `VACUUM INTO` in
`data/backups/pre-upgrade-<timestamp>.db`, saves the previous binary as
`bin/indexarr.old-<timestamp>`, stops the service, installs the new binary and
starts again. It checks `/healthz` with the new version, `t=caps` and a Torznab
search with the API key read from the DB (never printed). If a check fails it
automatically restores the DB and the binary and restarts the previous
version.

The rehearsal on a copy uses `--data-dir`, `--port 18510`, `--no-systemd` and
temporary binaries; it does not touch `:8788`, `data/`, `bin/indexarr` or
systemd:

```bash
./scripts/upgrade_test.sh
```

The outcomes of the three tests (successful upgrade, broken build, failed
health with automatic rollback) are in [scripts/README.md](scripts/README.md).

### System-wide systemd service

If you prefer a global service, use the example
[examples/indexarr.service](examples/indexarr.service). Create the user and the
directory, copy the binary and the unit:

```bash
sudo useradd --system --home /var/lib/indexarr --shell /usr/sbin/nologin indexarr
sudo install -d -o indexarr -g indexarr /var/lib/indexarr
sudo install -m 0755 bin/indexarr /usr/local/bin/indexarr
sudo install -m 0644 examples/indexarr.service /etc/systemd/system/indexarr.service
sudo systemctl daemon-reload
sudo systemctl enable --now indexarr
journalctl -u indexarr -f
```

The configuration file and the database are created in `/var/lib/indexarr`.

## Docker

```bash
docker compose up -d
```

The named volume `indexarr-data` contains `indexarr.json` and the database.
Alternatively:

```bash
docker build -t indexarr .
docker run -d --name indexarr -p 8788:8788 \
  -v indexarr-data:/data \
  indexarr
```

The default port is **8788**, chosen to avoid 8484 used by qBittorrent in many
installations. The default applies to new installations: an existing
`indexarr.json` keeps the `listen` value written at first start. If you upgrade
a container created with the old image, update the mapping to `8788:8788` or
add `-listen :8484` to the command to stay on the previous port.

The volume **does not** contain the vault key: in Docker use
`INDEXARR_MASTER_KEY_FILE` with a mounted secret (example in
[docs/VAULT.md](docs/VAULT.md#docker)) or `INDEXARR_MASTER_KEY`. Without a key
the service starts, but indexers with encrypted secrets are not readable.

## Development

```bash
make test    # go test ./...
make vet     # go vet ./...
make qa      # vet + test + HTTP smoke test on the real binary
make build   # bin/indexarr
make release # linux/amd64, linux/arm64, windows/amd64, darwin/arm64 binaries
make ui-smoke  # UI smoke test
make ci      # local CI, log in ci-logs/<commit>.log
```

For memory/goroutine diagnosis, pprof can be exposed only on localhost:

```bash
INDEXARR_PPROF=127.0.0.1:6060 ./bin/indexarr -config data/indexarr.json
go tool pprof -top http://127.0.0.1:6060/debug/pprof/heap
```

The definitions CLI is `indexarr-def`:

```bash
go run ./cmd/indexarr-def validate examples/definitions
go run ./cmd/indexarr-def check-fixtures examples/definitions/html-forum.yaml
go run ./cmd/indexarr-def convert-cardigann -o ./native ./definizioni-da-studiare
go run ./cmd/indexarr-def fmt -w examples/definitions/html-forum.yaml
go run ./cmd/indexarr-def fingerprint examples/definitions/html-forum.yaml
go run ./cmd/indexarr-def schema
make def-validate   # example definitions and catalog, if present
```

Package structure and design choices:
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Usage guide:
[docs/USAGE.md](docs/USAGE.md). Torznab API: [docs/API.md](docs/API.md);
administrative JSON API: [docs/ADMIN-API.md](docs/ADMIN-API.md). Test suite and
smoke test: [docs/QA.md](docs/QA.md).

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
- Cloudflare is handled with FlareSolverr for RSS and direct indexers;
  proprietary APIs stay queued until a native definition exists, and indexarr
  does not include a headless browser.
- The catalog does not yet have native definitions for individual Usenet
  sites: the generic Newznab path is complete, site coverage grows with the
  catalog.
- Download clients do not yet have a per-indexer selection or a
  priority/round-robin among clients of the same protocol.
- Notifications have a single global set of events: there is not yet an
  event × channel matrix.
- SMTP supports STARTTLS, not implicit TLS on port 465.

## Documentation

- [Definitions guide](docs/DEFINITIONS.md) — v1 format, fixtures, security
- [MirCrew](docs/MIRCREW.md) — native definition, Smartfeed, FlareSolverr
  session
- [Compatibility](docs/COMPATIBILITY.md) — sources, edge cases, diagnostics
- [Architecture](docs/ARCHITECTURE.md) — packages and design choices
- [Usage](docs/USAGE.md) — step-by-step guide
- [Torznab API](docs/API.md) and [Administrative API](docs/ADMIN-API.md)
- [Vault](docs/VAULT.md) — key, backup, Docker secrets
- [QA](docs/QA.md) — tests and smoke tests
- [Catalog](docs/CATALOG.md) — states, numbers and live evidence per site
- [UI rules](docs/UI-RULES.md) — rules every page follows
- [Migration from Jackett/Prowlarr](docs/MIGRATION-MANAGERS.md)
