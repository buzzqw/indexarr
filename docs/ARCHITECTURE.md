# Architecture

indexarr is a single Go process with a SQLite database. There are no mandatory
external services: the only dependency is the pure-Go SQLite driver
`modernc.org/sqlite`, so the binary is static (`CGO_ENABLED=0`).

## Flow

```
             ┌──────── native scheduler (tick) ────────┐
             │                                          │
   indexers ─┤ RunOnce ─► worker x N ─► indexer.Manager.Run
             │                                          │
             └──────────────────────────────────────────┘
                                    │
                                    ▼
                    connector (definition or family)
                                    │
                                    ▼
                       normalization (engine.Item)
                                    │
                                    ▼
                    .torrent? ─► Download + InfoHash
                                    │
                                    ▼
                 store.UpsertRelease + release_sources ─► SQLite
                                    ▲
                    Torznab /api ◄──┤
                    Web UI SSR ◄────┘
```

1. The **native scheduler** (`internal/native`) runs once at startup and then
   every `default_interval_seconds`, with a minimum of 60 seconds. A positive
   `interval_seconds` on an individual source overrides the global value only for
   that source; the "Reduce frequency" button doubles the interval of the source
   that returned 429. Every enabled indexer goes through
   `indexer.Manager.Run`; the order of outcomes is deterministic by id.
2. **Workers** are limited (`workers` setting, default 2). A disabled indexer is
   skipped and reported as such.
3. `Manager.Run` loads the definition, builds or reuses the cached connector,
   runs `Search` or `Latest`, records `indexer_history` (`kind = run`), updates
   `indexer_state` and writes the releases.
4. **fetch**: native definition (HTML/JSON/XML), Torznab/Newznab/RSS family or
   family connector (Unit3D, Gazelle, NexusPHP, …). Jackett and Prowlarr are
   ordinary Torznab indexers that point to the manager's per-indexer endpoint.
5. **normalization**: title, GUID, magnet, `.torrent` link, infohash, size,
   seeders/leechers, numeric category, date, IMDb/TMDB/TVDB id.
6. **`.torrent` download**: only when the magnet is missing and the indexer
   offers a download link. Limit per run and per size; the file is parsed to
   derive the infohash and stored as a BLOB.
7. **upsert**: the key is the infohash, otherwise `guid:`, `url:` or `title:`
   with a SHA-256 hash. If the infohash is already present under another key,
   that key is reused. The same torrent seen by multiple indexers updates a
   single row and adds a row to `release_sources` for each indexer.
8. **output**: the SSR UI reads the same store; the Torznab server converts the
   rows to XML and serves the stored `.torrent` files. Per-indexer filters use
   `release_sources`, not a denormalized column.

## Packages

| Path | Responsibility |
| --- | --- |
| `cmd/indexarr` | Flags, startup, signals, orderly shutdown |
| `internal/config` | Minimal JSON file, creation on first start, DB path |
| `internal/store` | Schema, numbered migrations, settings, indexers, releases, provenance, history, blocklist, saved searches, maintenance |
| `internal/definition` | Native format v1: strict YAML parsing, validation, SHA-256 `Fingerprint`, upgrader chain, embedded JSON Schema |
| `internal/engine` | Connector/pipeline contracts, transport with guards and proxy, cookie sessions, persistent FlareSolverr session per definition (`session`), templates, HTML/JSON/XML extraction, normalization, `download.before` and magnet selection by season/episode |
| `internal/family` | Family connectors `torznab`, `newznab`, `rss`, `unit3d`, `gazelle`, `nexusphp`, `torrenttrader`, `xbtit` |
| `internal/indexer` | Builds and caches the connector from definition + configuration + secrets, runs a torrent/Usenet indexer, records history and provenance, provides live search |
| `internal/indexersvc` | Indexer page service: validation, create/update/delete, separation of public config and secrets in the vault |
| `internal/native` | Periodic scheduler, run/test of all indexers, automatic backups, retention, notifications and auto-disable |
| `internal/catalog` | Embedded catalog (`catalog.yaml` + `definitions/*.yaml`): validated manifest, parsed definitions, synchronization to the store |
| `internal/catalog/managerdata` | Historical snapshot of public metadata used only by the catalog generators; no runtime link |
| `internal/torznabapi` | Torznab server, multiple API keys, rate limit, live search and `.torrent` download |
| `internal/adminapi` | JSON API `/api/v2` authenticated with an API key: definitions, catalog, indexers, applications, test/search/run |
| `internal/web` | SSR UI, authentication, CSRF, forms, PWA, maintenance and restore |
| `internal/syncjob` | Periodic reconciliation of \*arr applications with `appsync`, persistence of `app_indexers` and management of sync levels |
| `internal/downloadproxy` | Resolves encrypted `download_ref`s and serves `.torrent`/`.nzb` files not yet in the cache |
| `internal/vault` | AES-256-GCM; key from `INDEXARR_MASTER_KEY`, `INDEXARR_MASTER_KEY_FILE`, `vault_key_path`, `<db>.key` file or user directory, never in the database (see [VAULT.md](VAULT.md)) |
| `internal/cardigann` | Local Cardigann → native v1 converter, with family detection and warnings; it is not a runtime |
| `internal/feed` | RSS/Atom/Torznab parser and normalization utilities; the historical HTML parsers remain only as the oracle of the equivalence tests |
| `internal/flaresolverr` | Minimal client to solve Cloudflare challenges |
| `internal/logbuffer` | In-memory ring buffer and slog handler with secret redaction |
| `internal/notify` | JSON webhook, Telegram, Apprise, Discord, Slack, Gotify, ntfy, Pushover, SMTP email and script notifications, with secret redaction |
| `internal/backup` | Consistent backups, listing/rotation, validation and restore at restart |
| `internal/downloadclient` | Sending magnets/.torrent/.nzb/eD2k to qBittorrent, Transmission, Deluge, rTorrent, SABnzbd, NZBGet, aMule, Aria2, uTorrent, Flood, Download Station, NZBVortex, Hadouken and blackhole |
| `internal/simmock`, `cmd/simmock`, `sim/` | Deterministic end-to-end simulation: fake Torznab/Newznab, Prowlarr, Sonarr, download clients and HTML trackers |
| `internal/i18n` | Embedded UI catalogs (en/it/fr/de/es/zh/pl), language negotiation, English fallback |
| `internal/perf` | Dataset generator and leak tests for benchmarks; not linked by the production binary |
| `internal/appsync` | Sonarr/Radarr/Lidarr/Readarr/Whisparr client (Servarr API) and Mylar3/LazyLibrarian, with sync levels and per-application profile (RSS, automatic/interactive search, minimum seeders, categories) |
| `internal/deftool` | Testable CLI logic: validation, formatting, fixture checking |
| `cmd/indexarr-def` | `validate`, `schema`, `fmt`, `fingerprint`, `check-fixtures` CLI |

Dependencies: `definition` imports nothing from indexarr; `engine` does not import
`store`, `web` or `torznabapi`; `indexer` is the layer that connects definitions,
store, vault and connectors. Every indexer goes through a native connector: there
is no second scheduler or second HTTP client.

## Database

```
definitions ──1:N── indexers ──1:N── indexer_secrets
                         │──1:N── indexer_state
                         │──1:N── indexer_history
                         └──1:N── release_sources ──N:1── releases
proxies ──1:N── indexers
applications ──N:M── indexers (app_indexers)
blocklist (rules)
saved_searches (named queries)
api_keys (additional Torznab keys)
download_clients (torrent/usenet, one default per protocol rule)
settings (key/value)
```

- `definitions`: native YAML, family, state (`bozza`/`verificata`/`stabile`/
  `degradata`), origin (`builtin`/`catalog`/`local`), author, hash.
- `indexers`: configured instance with `definition_id`, protocol, unique slug,
  name, priority, non-secret `config_json`, proxy, tags and query/grab limits.
- `indexer_secrets`: secrets encrypted in the vault per indexer.
- `indexer_state`: encrypted cookies, last login, circuit breaker, `disabled_until`
  and last error.
- `indexer_history`: history of `run`, `search`, `latest`, `test`, `grab`,
  `download` and sync, with latency, bytes and redacted error.
- `release_sources`: provenance (which indexer saw a release, first/last
  observation, seeders/leechers, `download_ref`).
- `releases`: identity (unique `dedup_key`), infohash, external ids, title and
  normalized `search_title`, metadata, magnet, `.torrent` BLOB or Usenet
  payload, `first_seen_at`/`last_seen_at`/`times_seen`, `favorite` and `hidden`.
- `releases_fts`: FTS5 external-content virtual table on `releases`, with
  trigram tokenizer and synchronization triggers; it does not duplicate the
  titles.
- `blocklist`: `infohash`, `title`, `regex`, `source`, `category` rules; the
  matcher compiles the regexes once per run and discards releases before the upsert.
- `saved_searches`, `api_keys`, `settings`: saved searches, additional revocable
  keys and key/value options.
- `download_clients`: torrent and Usenet clients with secrets sealed in the
  vault, protocol, category, path, default flag. Migration 6 creates the table;
  the old `download_client*` settings remain as a fallback and are imported on
  first start when the vault is available.

Indexes on infohash, publication date, category, last observation, external ids
and provenance. The database uses WAL, `busy_timeout`, foreign keys and a pool
limited to four connections.

### Identity and deduplication

| Priority | Key | When |
| --- | --- | --- |
| 1 | infohash (40 hex) | magnet, Torznab attribute, downloaded `.torrent` |
| 2 | `guid:<sha256>` | stable indexer GUID |
| 3 | `url:<sha256>` | download link |
| 4 | `title:<sha256>` | title + size |

The infohash is also the merge key across different indexers. The Maintenance
page offers an explicit deduplication that keeps the oldest row and the best data
(magnet, torrent, seeders, last observation, `times_seen`).

## Scheduler and reliability

- Full run at startup and then every `default_interval_seconds`; minimum 60 seconds.
  Each source can have its own `interval_seconds`, shown in the list and in the
  detail tab; "Reduce frequency" doubles it only for the source that returned
  429. Live searches are not subject to this interval.
- Limited workers (`workers`); a manual run and a periodic run of the same indexer
  do not overlap thanks to the manager and the history.
- Per-request timeout from the transport; an error is recorded in
  `indexer_history` and in `indexer_state.last_error`.
- Webhook/Telegram/Apprise/Discord/Slack/Gotify/ntfy/Pushover/email/script notifications for new releases, grabs, indexer status, catalog and backups. If
  `auto_disable_after` is set and the latest runs have all failed, the indexer
  is disabled and the event is notified.
- Torznab live search (`torznab_live_search`) uses `indexer.SearchLive` on every
  request with a query: fan-out to all capable indexers, global concurrency 16,
  deadline `torznab_live_timeout_seconds`; those running late continue in the
  background and cache the result. Indexers with `meta.latest: false` do not have
  periodic runs and only take part in live search.
- Automatic backups (`backup_enabled`) are created every
  `backup_interval_hours` with `VACUUM INTO`, rotated at `backup_keep` and do not
  block the workers. The restore is prepared as `<db>.restore` and applied by
  `main` before opening SQLite; the current file is copied to
  `backups/pre-restore-*.db`.
- Retention runs every six hours and from the Maintenance page; run history is
  cleaned up after 90 days and `PRAGMA optimize` + `wal_checkpoint(TRUNCATE)` is
  executed.

## Migrations

`internal/store` creates the final schema idempotently and applies the numbered
migrations recorded in `schema_migrations`:

1. `indexarr2 foundation`: `definitions`, `indexers`, `indexer_secrets`,
   `indexer_state`, `release_sources`, `indexer_history`, `proxies`,
   `applications`, `app_indexers`.
2. `usenet payload`: `releases.protocol`, `payload`, `payload_type`.
3. `drop legacy sources`: for a previous database it drops the old `sources`
   table, the `indexers.legacy_source_id` column, the `runs` table and the
   `releases.source_id`/`source_name` denormalization, then recreates `releases`
   and `release_sources` on the provenance model. On a new database it is a no-op.

`catalog.Sync` writes the embedded definitions only as `builtin` and skips those
with `local` origin; it rewrites an entry only when the hash changes.

## HTTP server

- UI: SSR with `html/template`, embedded CSS/JS (`go:embed`), no Node build.
- Torznab: XML built with explicit escaping (`xml.EscapeText`), no dependency on
  Torznab libraries.
- Authentication: primary key with constant-time comparison, additional keys in
  the database, rate limit per key; Basic with PBKDF2.
- CSRF: `indexarr_csrf` cookie, HttpOnly + hidden field, double submit; the
  multipart backup upload uses the same token in the query string.
- Download: `/dl/{id}.torrent` requires the same API key as the search and the
  generated links use the request's key.
- XML errors for Torznab clients, HTML pages with the correct status for the UI.
- `/sources*` and `/indexers?tab=legacy` respond 302 to `/indexers`;
  `/definitions`, `/rules` and `/maintenance` to the Settings section.
- `GET /metrics` and `GET /sw.js` are on the outer mux; `/metrics` does not
  require authentication, like `/healthz`, and must not be exposed without a
  reverse proxy.
- PWA: manifest and icon in the assets, service worker that caches only
  `/static/*`; the HTML stays network-only because it contains CSRF and private
  data.

## Design choices

- **SQLite with FTS5 instead of an external search engine**: the typical volume
  (tens of thousands of releases) does not justify a separate service. The
  `releases_fts` table is external-content on `releases`, uses the **trigram**
  tokenizer to keep substring search and is synchronized by triggers on
  insert/update/delete. Terms shorter than three characters fall back to `LIKE`.
- **A single indexer model**: a public site, an RSS feed or a Jackett/Prowlarr
  Torznab endpoint are all rows in `indexers`. Provenance lives in
  `release_sources`; there is no denormalized column to keep in sync.
- **No content download**: indexarr is an index; downloading files is the
  torrent client's responsibility.
- **One process**: no distributed locks; the database is the source of truth.
- **Magnet before file**: downloading the `.torrent` for every result would be
  pointless and heavy; it is downloaded only when it is the only way to get the
  release.
- **Tolerant XML**: scraped feeds contain raw `&` and HTML entities; a
  sanitization before the decoder prevents a single character from making the
  whole response unreadable.
- **FlareSolverr on challenge**: the one-request solver is called only when the
  response is recognizable as a Cloudflare challenge, never for an authentication
  error or for a LAN address. It is stateless: one request solved at a time,
  without persistent sessions. An indexer that protects **all** pages (for
  example MirCrew) declares `session` instead and uses a persistent browser
  session, with login, 12-hour recycling and cleanup by prefix; see
  [DEFINITIONS.md](DEFINITIONS.md#session) and [MIRCREW.md](MIRCREW.md).
- **Live search active by default**: it queries Torznab/Newznab indexers when the
  cache has no results, then saves what arrives. It can be turned off from
  Settings because it adds seconds to the client response; a search with only
  external ids does not enable it on sites that do not support ids.
- **Backup and restore**: the backup is a standalone SQLite file; the restore
  does not open the uploaded file in the live process but applies it at restart,
  after preserving the current database.

## Changes in 2.0.0-rc1

- Release merging happens on the full set before `limit`/`offset`; the short snapshot keeps `total` constant across pages.
- Live fan-out shares one worker per `(indexer, effective query)`, bounds the queue and retires workers without requests.
- `LiveExcluded` excludes TGx/ExtTo uploaders from live; the lists still feed the cache.
- Stored sources have a measured warning, counter and explicit retention.
- Migrations 10 (dedup) and 11 (TGx hashless cleanup).

## Memory

`indexarr` sets a soft Go limit of 64 MiB (`memory_limit_mb` in
`indexarr.json`, 0 to disable it). The `scripts/budget.sh` budget remains a
warning at 64 MiB and an error at 256 MiB, measured on `RssAnon`; total RSS also
includes the binary and is not the budget value.
