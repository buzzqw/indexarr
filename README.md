# indexarr

**Self-hosted torrent and Usenet indexer, Torznab/Newznab compatible.**
indexarr builds a local index of releases, keeps it up to date and exposes it
to Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3, LazyLibrarian, the other
managers of the Servarr family or any Torznab client. It was created to replace Jackett and Prowlarr in the daily
workflow: native sources, SQLite cache, live search, app synchronization and a
complete web interface, with no mandatory external services.

It does not download the content of torrents or NZBs: it builds a searchable
**index**. Downloading remains with the chosen client — qBittorrent,
Transmission, Deluge, rTorrent, SABnzbd, NZBGet, aMule and others.

```
Native sources / RSS / Newznab ──► scheduler ──► normalization ──► SQLite
                                                                      │
  Sonarr / Radarr / Lidarr / Readarr and forks ◄── Torznab / Newznab ─┤
                          Browser ◄── web UI SSR + JSON API ──────────┘
```

**Documentation:** [wiki home](https://github.com/buzzqw/indexarr/wiki) ·
[user manual](https://github.com/buzzqw/indexarr/wiki/User-Manual) ·
[manuale utente](https://github.com/buzzqw/indexarr/wiki/Manuale-Utente) ·
[Torznab API](https://github.com/buzzqw/indexarr/wiki/Torznab-API) ·
[admin JSON API](https://github.com/buzzqw/indexarr/wiki/Admin-API).

## Installation

Four ways to run indexarr. The first three need no Go toolchain; building from
source is for development.

| Path | When to choose it | Command |
| --- | --- | --- |
| **Prebuilt archive** | you only want to run it | download, unpack, `./install.sh` |
| **install.sh** | from a checkout | `./install.sh` |
| **Docker** | container, NAS, everything in one volume | `docker compose up -d --build` |
| **From source** | development, or a host without systemd | `make build && ./bin/indexarr` |

### From a prebuilt archive

Ready-to-run archives of the current code are published manually on the
[continuous release](https://github.com/buzzqw/indexarr/releases/tag/continuous):
`indexarr-linux-amd64.tar.gz` and `indexarr-linux-arm64.tar.gz`. Publishing is
not automatic; a maintainer refreshes the rolling release with
`make publish PUBLISH_ARGS=--yes` (or after every commit, with the opt-in
`scripts/publish-release.sh --install-hook`). There is nothing to compile and
nothing to clone:

```bash
curl -LO https://github.com/buzzqw/indexarr/releases/download/continuous/indexarr-linux-amd64.tar.gz
curl -LO https://github.com/buzzqw/indexarr/releases/download/continuous/SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
tar xzf indexarr-linux-amd64.tar.gz
cd indexarr-*-linux-amd64
./install.sh
```

The archive holds the static binary, `install.sh`, the installer in
`scripts/`, the systemd unit and the example definitions; `INSTALL.md`
repeats the two commands. Right after a refresh GitHub's CDN can still serve the
previous archive for about a minute; `sha256sum -c` tells you which one you got,
download it again if the checksum fails. It is the **current code**, not a
fixed version: for production pin a versioned release when there is one, or
update an existing installation from a newer archive.

### Install with install.sh

Clone the repository, then from its root run the installer. One command
builds the binary (when Go and `make` are available, otherwise it installs an
existing `bin/indexarr` or the file given with `--binary`), installs a
systemd unit, starts it and checks `/healthz`:

```bash
git clone https://github.com/buzzqw/indexarr.git
cd indexarr
./install.sh
./install.sh --listen 127.0.0.1:8788   # local only
./install.sh --binary /tmp/indexarr    # prebuilt binary, no Go needed
./install.sh --system                  # system service in /var/lib/indexarr
./install.sh --status
./install.sh --uninstall               # data is kept
```

By default it is a **user service** started by the account that runs the
installer, without `sudo`; the data directory is `<repo>/data` and is never
deleted by `--uninstall`. `--system` installs a system-wide unit for the
dedicated `indexarr` account with data in `/var/lib/indexarr`, and
`--linger` keeps the user service running after logout.

### Install with Docker

From the same checkout the image is built locally and started in the
background:

```bash
docker compose up -d --build
```

The page is on port **8788**, the container runs as the non-root UID 10001 and
the database, the configuration and the vault key live in the named volume
`indexarr-data`. `INDEXARR_PORT` changes the host port, `INDEXARR_VERSION`
tags the image (Compose defaults to `dev`; `make docker` uses the version from
`scripts/version.sh`). To keep the vault key outside the volume, mount it and
set `INDEXARR_MASTER_KEY_FILE` (the line is already in `docker-compose.yml`).
The prebuilt archives are for `install.sh`, not a container image: Docker
builds from the checkout.

### Upgrading and rollback

`scripts/upgrade.sh` updates a live installation without wiping the database:
dry run by default, `--apply --yes` to execute, `--rollback --yes` to restore
the latest backup. Reinstalling a newer archive with `./install.sh` keeps the
data directory too. With Docker, rebuild the image and recreate the service
(`docker compose up -d --build`); the volume is untouched.

Installer flags, production upgrade and rollback, the system-wide unit and
Docker: [Installation](https://github.com/buzzqw/indexarr/wiki/Installation).

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
- Jackett-compatible aggregate filters: `tag`, `indexer_type`, `lang`,
  `status=healthy|failing|unknown` (the same health model as the web UI) and
  `test=passed|failed` (the last health check). `status=enabled|disabled`
  keeps working.
- **Per-request override of the saved "freeleech only"**: `freeleech=0` (or
  `ignore_filters=1`) returns everything for that call, `freeleech=1` asks for
  free releases even when the source is not configured for it, and the source
  setting stays untouched. `freeleech=1` has no effect on a definition that
  cannot mark free releases: returning nothing would be a lie, not a filter.
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

### Web interface

- **Guided first start** that asks for the address the apps must use; it can
  be reopened from Settings.
- **Home** with source status, latest releases and runs.
- **Search** with two separate searches, each with its own heading and button:
  **live on the indexers** (free text, TV, movies, music and books,
  IMDb/TMDb/TVDb ids, selection of multiple indexers, categories) and **the
  stored catalogue** (filters, saved searches and **bulk send** to clients).
  Every result names its source and can be sent to the client from the row.
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
- **Freeleech only** when the definition supports it; a client can override it
  for a single Torznab request (`freeleech=0|1`, `ignore_filters=1`).
- **Import cookie** from the Firefox profile for sites that bind the session to
  the browser, such as IPTorrents.
- **A problem is what is happening now**: the stored error of a source is
  cleared as soon as a health check or a run succeeds, so a timeout from hours
  ago does not keep the dashboard red, and every cause (rate limit, flood
  control, DNS, page changed) has its own sentence instead of "unexpected
  error". The health check and the periodic runs keep the sources of one site
  apart: they are checked one after the other, and even when several are due
  only one is read per tick, so a site is never asked for everything at the
  same second.

### \*arr apps

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

### Download clients

Supported:

| Client | Protocol | Notes |
| --- | --- | --- |
| qBittorrent | torrent | Web API v2, category, path, pause, seed ratio/time |
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
instead of pretending the queue is empty.

A delivery is not a completed download. A client that knows when the file
arrives can report it to `POST /api/v2/downloads/confirm`, with the same API key
as the Torznab endpoint: the Download tab then shows **downloaded** instead of
**delivered only**. Gextto already does it; the contract is in
[Download confirmation](docs/DOWNLOAD_CONFIRMATION.md).

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
real picture, not a list of intentions.

The catalog ships **more than a hundred site definitions** — most of them
verified, a few still drafts — plus family connectors (UNIT3D, Gazelle,
NexusPHP, TorrentTrader, Xbtit) and generic Torznab/Newznab/RSS endpoints. The complete,
generated list with access method, language and state for every site is
**[docs/SUPPORTED.md](docs/SUPPORTED.md)**; it is regenerated in the same
commit that adds or removes a provider, and a test fails while it is stale.

Among the best known: 1337x, The Pirate Bay, YTS, EZTV, LimeTorrents, Torlock,
TorrentFunk, Nyaa, Sukebei, AnimeTosho, RuTracker and RuTor for Russian
content, TorrentGalaxy, IPTorrents, TorrentLeech (draft), and — Italian —
MirCrew, ExtTo, CorsaroBlu, ilDraGoNeRo and DDUnlimited (eD2k).

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

## Requirements

- **Linux with systemd** to use `install.sh`, or **Docker with Compose** for
  the container image. The binary itself has no runtime dependencies.
- **Go 1.26 or later** only to build from source.
- A writable filesystem for the database.
- Optional: FlareSolverr for Cloudflare, a torrent/Usenet/eD2k client for
  sending releases.

## From source

For development. To run indexarr for real, use `install.sh` or Docker as shown
in [Installation](#installation).

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

`indexarr.json` contains only the startup settings: `listen`, `db_path`,
`vault_key_path`, `memory_limit_mb` and the log options. Everything else —
API keys, intervals, retention, UI credentials, notifications, clients,
backups — is in the database and is changed from the **Settings** page, without
restarting.

The file, the vault key lookup and every setting with its default:
[Configuration](https://github.com/buzzqw/indexarr/wiki/Configuration).

## Sources

Each source can have **HTTP Basic** credentials independent of the Torznab API
key; **Test** and **Run** work per source, **Test all** and **Run all** on every
enabled source. The catalog, RSS/Atom, direct Torznab, Usenet/Newznab, eD2k,
the native HTML sites, private sources with cookies and forums,
Cloudflare/FlareSolverr and the Jackett/Prowlarr replacement:
[Sources](https://github.com/buzzqw/indexarr/wiki/Sources).

## Connecting the apps

In the **Settings** page copy the full endpoint and give it to the client as a
**Torznab** indexer:

```text
http://<host>:8788/api?apikey=<API_KEY>
```

Categories are `5000` TV, `2000` Movies, `3000` Audio and `7000` Books. The
`/torznab/api` and `/newznab/api` aliases and the API key in the `X-Api-Key`
header also work.

App synchronization, per-app profiles, multiple API keys and live search:
[Connecting apps and clients](https://github.com/buzzqw/indexarr/wiki/Connecting-Apps).

## Web interface

| Page | Content |
| --- | --- |
| **Home** | Totals, indexer status, latest releases, runs and problems |
| **Search** (`/releases`) | Two searches: live on the indexers (text, TV, movies, music, books, external IDs) and the stored catalogue (`-xxx` exclusion, regex, advanced filters, saved searches, favorites/hidden, bulk grab); detail with magnet, `.torrent` and `.nzb`, plus TMDB/TVDB search buttons on every row with the title cleaned of technical tags and localized titles, the source indexer and a direct **Send** action |
| **Indexers** | Single list, Run/Test/Run all/Test all/Test problems, detail with Site access, edit, clone, JSON export/import, feed discovery and cookie import; each source can have its own request timeout |
| **App** | Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3, LazyLibrarian and the generic Servarr-compatible type; sync, per-app profile, snapshots and download clients |
| **Statistics** (`/activity`) | Incoming requests, overview per app and indexer, downloads served per source, completion reported by the client, the live **Queue** of the configured clients, update history, new releases, per-host network counters |
| **Logs** (`/logs`) | System log with filters and runtime level |
| **Settings** | Sections General, Network (FlareSolverr, per-host rate limit), Security, Notifications, **Rules** (blocklist by infohash, title, regex, source or category), **Backup**, **Maintenance** (integrity, vacuum, optimize/checkpoint, reindex, deduplication, `.torrent` cleanup, retention, purge per indexer, restore), Advanced (circuit breaker, live search, downloads through indexarr, definitions, proxies) and Updates |

The old `/rules`, `/maintenance` and `/definitions` addresses redirect to the
matching Settings section.

The interface is a **PWA**: manifest, icon and service worker that caches only
static assets; HTML pages always come from the network because they contain
CSRF and private data.

Search syntax, saved searches, rules, sending to clients and notifications:
[Web interface](https://github.com/buzzqw/indexarr/wiki/Web-Interface).

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

## Development

```bash
make test      # go test ./...
make vet       # go vet ./...
make qa        # vet + test + HTTP smoke test on the real binary
make build     # bin/indexarr
make release   # linux/amd64, linux/arm64, windows/amd64, darwin/arm64
make package   # self-contained archives in dist/ (linux/amd64, linux/arm64)
make publish   # dry run; PUBLISH_ARGS=--yes publishes the rolling release manually
make ui-smoke  # UI smoke test
make ci        # local CI, log in ci-logs/<commit>.log
```

The definitions CLI is `indexarr-def` (`validate`, `check-fixtures`,
`convert-cardigann`, `fmt`, `fingerprint`, `schema`). pprof, the complete
command list and the package structure:
[Development](https://github.com/buzzqw/indexarr/wiki/Development).

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

## Documentation

The manuals live in the [GitHub wiki](https://github.com/buzzqw/indexarr/wiki);
the list of supported sources is generated from the catalog and lives in the
repository.

- [Supported sources](docs/SUPPORTED.md) — every site and connector, with access method and state
- [Download confirmation](docs/DOWNLOAD_CONFIRMATION.md) — how a client reports a completed download
- [Wiki home](https://github.com/buzzqw/indexarr/wiki) — index of every page
- [User manual](https://github.com/buzzqw/indexarr/wiki/User-Manual) — step-by-step guide (English)
- [Manuale utente](https://github.com/buzzqw/indexarr/wiki/Manuale-Utente) — guida passo passo (italiano)
- [Configuration](https://github.com/buzzqw/indexarr/wiki/Configuration) — `indexarr.json` and every setting with its default
- [Sources](https://github.com/buzzqw/indexarr/wiki/Sources) — catalog, RSS, Torznab, Usenet, eD2k, cookies and FlareSolverr
- [Connecting apps and clients](https://github.com/buzzqw/indexarr/wiki/Connecting-Apps) — Torznab endpoint, \*arr sync, API keys, live search
- [Web interface](https://github.com/buzzqw/indexarr/wiki/Web-Interface) — pages, search syntax, saved searches, rules and notifications
- [Operations](https://github.com/buzzqw/indexarr/wiki/Operations) — backup, maintenance, statistics, logs and security
- [Installation](https://github.com/buzzqw/indexarr/wiki/Installation) — systemd installer, upgrades and Docker
- [Architecture](https://github.com/buzzqw/indexarr/wiki/Architecture) — packages and design choices
- [Definitions guide](https://github.com/buzzqw/indexarr/wiki/Definitions) — v1 format, fixtures, security
- [MirCrew](https://github.com/buzzqw/indexarr/wiki/MirCrew) — native definition, Smartfeed, FlareSolverr
  session
- [Compatibility](https://github.com/buzzqw/indexarr/wiki/Compatibility) — sources, edge cases, diagnostics
- [Torznab API](https://github.com/buzzqw/indexarr/wiki/Torznab-API) and [Administrative API](https://github.com/buzzqw/indexarr/wiki/Admin-API)
- [Vault](https://github.com/buzzqw/indexarr/wiki/Vault) — key, backup, Docker secrets
- [QA](https://github.com/buzzqw/indexarr/wiki/QA) — tests and smoke tests
- [Development](https://github.com/buzzqw/indexarr/wiki/Development) — build, test, pprof and the definitions CLI
- [Catalog](https://github.com/buzzqw/indexarr/wiki/Catalog) — states, numbers and live evidence per site
- [UI rules](https://github.com/buzzqw/indexarr/wiki/UI-Rules) — rules every page follows
- [Migration from Jackett/Prowlarr](https://github.com/buzzqw/indexarr/wiki/Migration-Managers)
- [Backlog](https://github.com/buzzqw/indexarr/wiki/Backlog) — open work, evidence and how to verify it

## Support the project

If indexarr is useful and you would like to support its development, a PayPal
donation is welcome — thank you. A **Donate** button is also always visible in
the UI, bottom right.

[![Donate with PayPal](https://img.shields.io/badge/Donate-PayPal-0070BA.svg?logo=paypal)](https://www.paypal.com/cgi-bin/webscr?cmd=_donations&business=azanzani@gmail.com&item_name=Support+indexarr+Project)

## Legal and fair use

indexarr is a **self-hosted indexer**. It does not host or distribute any
copyrighted content; it builds a local index of links from the sources you
configure.

- It reads **sources you configure**: native definitions, Torznab/Newznab
  endpoints and public RSS feeds. It ships no index of its own.
- What you search, download or stream is **your responsibility**. Use indexarr
  only for content you have the right to access — public domain, Creative
  Commons, or media you own.
- BitTorrent, Usenet and eD2k are neutral technologies; this project does not
  encourage or facilitate piracy.
- indexarr **replaces** Jackett and Prowlarr: it does not proxy them and does
  not read their configuration.
