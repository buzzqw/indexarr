# indexarr Torznab API

indexarr exposes a Torznab server (newznab-compatible). The same handler
answers on several paths, so clients that append `/api` and those that use the
full URL both work.

| Path | Meaning |
| --- | --- |
| `/api` | aggregate (all indexers) — endpoint recommended for manual clients |
| `/torznab/api` | alias of the aggregate |
| `/newznab/api` | alias of the aggregate |
| `/api/v2.0/indexers/all/results/torznab/api` | Jackett-compatible alias (aggregate) |
| `/torznab/{slug}` | per-indexer base: the client appends `/api` |
| `/torznab/{slug}/api` | full per-indexer endpoint |
| `/api/v2.0/indexers/{slug}/results/torznab/api` | Jackett-compatible per-indexer alias |

- Base: `http://<host>:8788`
- Aggregate search endpoint: `http://<host>:8788/api`
- Authentication: `apikey` parameter or `X-Api-Key` header
- Response: `rss` XML with the `torznab` and `newznab` namespaces
- Administrative JSON API: see [ADMIN-API.md](ADMIN-API.md)

`{slug}` is the source slug (for example `mircrew` or
`mircrew-smartfeed`), shown on the source page. The per-indexer endpoint
queries only that source; the aggregate applies live search to all the enabled
ones.

The primary key is in **Settings → Torznab API** and can be regenerated (every
client that uses it must be updated). In **Additional API keys** you create
separate keys per client, each one revocable individually. The `.torrent` links
in the response use the key that came with the request, so an additional key
also works for downloads.

`api_rate_limit_per_minute` enforces a per-key limit: once the threshold is
exceeded the response is `429` with `Retry-After: 60`.

## Supported functions

| `t` | Parameters | Notes |
| --- | --- | --- |
| `caps` | — | Capabilities, categories and limits; does not require the key |
| `search` | `q`, `cat`, `limit`, `offset` | Generic search |
| `tvsearch` | `q`, `season`, `ep`, `cat`, `imdbid`, `tvdbid`, `tmdbid` | Season/episode filter, also on season packs |
| `movie` | `q`, `year`, `cat`, `imdbid`, `tmdbid` | Movies |
| `get` | `id` | Downloads the saved `.torrent` (alternative to `/dl/{id}`) |

`music` and `book` respond like `search`: the cache does not yet distinguish
those types with their own filters, but categories 3000 and 7000 are available.

### Parameters

| Name | Default | Description |
| --- | --- | --- |
| `apikey` | — | Required for search and download |
| `q` | empty | Text searched in the normalized title (all terms in AND) |
| `cat` | all | One or more comma-separated Torznab ids; `5000` selects the whole TV root |
| `limit` | 50 | Maximum 100 |
| `offset` | 0 | Pagination |
| `season` | 0 | `tvsearch` only |
| `ep` | 0 | `tvsearch` only |
| `imdbid` | empty | With or without the `tt` prefix |
| `tmdbid` | empty | |
| `tvdbid` | empty | |
| `year` | 0 | `movie` only, also used in the textual fallback |
| `live_wait` | empty | Seconds to wait for the live search for this request, 1–60; extension used by gextto to give eD2k sources time. Does not change the Sonarr/Radarr default |

`q` and the external ids are combined in OR: a release that matches the id is
returned even if the title is different.

The `-term` syntax and regex mode belong to the **Releases** web page; the
Torznab API uses only the standard `q` parameter.

### Live search

With `torznab_live_search` enabled, **every** request with a query (`search`,
`tvsearch`, `movie`, `music`, `book`) queries live all the enabled indexers
that declare the mode: not only when the cache is empty. The fan-out has a
global concurrency of 8 and respects the per-host limit; the overall deadline
is `torznab_live_timeout_seconds` (default 20). The results that arrive in
time are merged into the cache with the existing provenance; those that are
late keep going in the background (up to 3 minutes) and their result enters the
cache for the next request.

With `live_wait` a client can extend only the wait of its own request (up to
60 s); gextto uses it for eD2k indexers, which must open the topic pages. The
other clients do not send the parameter and stay at 5 s.

An eD2k item carries the link in `<link>`, in the enclosure (`application/x-ed2k`)
and in the `ed2k` and `ed2khash` attributes. With `torznab_proxy_ed2k` enabled
(default) `<link>` and enclosure are the indexarr download link, which redirects
to the `ed2k://` and records the grab; the `ed2k` attribute always carries the
direct link. It does not carry `seeders`, `leechers` or `peers`: an indexer
cannot count eD2k sources, and a 0/0 would make the client believe that nobody
is sharing the file.

Fallback: `tvsearch` without support becomes `search` with `SxxEyy` (or `Sxx`);
`movie` adds the year; a request with only unsupported ids and no `q` skips that
indexer. RSS requests (`t=search`/`tvsearch` without `q`) answer only from the
cache of indexers with `meta.latest` and do not generate synthetic queries. The
per-indexer endpoint queries only that one.

## Examples

```bash
# Capabilities
curl 'http://localhost:8788/api?t=caps'

# TV search
curl 'http://localhost:8788/api?t=tvsearch&q=Show&season=1&ep=2&cat=5000&apikey=KEY'

# Movie by IMDb id
curl 'http://localhost:8788/api?t=movie&imdbid=0133093&apikey=KEY'

# Download the .torrent
curl -OJ 'http://localhost:8788/dl/42.torrent?apikey=KEY'
```

## Search response

```xml
<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"
     xmlns:torznab="http://torznab.com/schemas/2015/feed"
     xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <title>indexarr</title>
    <link>http://localhost:8788</link>
    <description>indexarr Torznab index</description>
    <newznab:response offset="0" total="1"/>
    <item>
      <title>Show.S01E02.1080p</title>
      <guid isPermaLink="false">0123456789abcdef0123456789abcdef01234567</guid>
      <link>http://localhost:8788/dl/1.torrent?apikey=KEY</link>
      <comments>http://localhost:8788/releases/1</comments>
      <pubDate>Mon, 05 Oct 2026 10:17:44 +0000</pubDate>
      <size>1234567890</size>
      <category>5040</category>
      <enclosure url="http://localhost:8788/dl/1.torrent?apikey=KEY"
                 length="1234567890" type="application/x-bittorrent"/>
      <torznab:attr name="category" value="5040"/>
      <torznab:attr name="size" value="1234567890"/>
      <torznab:attr name="seeders" value="12"/>
      <torznab:attr name="leechers" value="2"/>
      <torznab:attr name="peers" value="14"/>
      <torznab:attr name="infohash" value="0123456789abcdef0123456789abcdef01234567"/>
      <torznab:attr name="magneturl" value="magnet:?xt=urn:btih:..."/>
      <torznab:attr name="publishdate" value="2026-10-05T10:17:44Z"/>
      <torznab:attr name="downloadvolumefactor" value="0"/>
      <torznab:attr name="uploadvolumefactor" value="1"/>
    </item>
  </channel>
</rss>
```

If the `.torrent` has not been saved, `<link>` and `<enclosure>` contain the
magnet: clients that support magnets (Sonarr, Radarr, qBittorrent) use it
directly. `downloadvolumefactor` and `uploadvolumefactor` are configurable in
Settings. If the request uses an additional key, the `.torrent` links carry that
key, not the primary one.

If an indexer does not report seeders (for example MirCrew), the `seeders`,
`leechers` and `peers` attributes are **omitted** instead of being `0`: a
missing value means "unknown", a zero would mean "dead torrent".

### Link base

Links are built with `public_url` if set, otherwise from the request host.
Behind a reverse proxy set `public_url` (for example
`https://indexarr.example.org`) and forward `X-Forwarded-Proto`/`X-Forwarded-Host`.

## Download

| Path | Authentication | Response |
| --- | --- | --- |
| `GET /dl/{id}.torrent?apikey=KEY` | yes | `.torrent` file with `Content-Type: application/x-bittorrent` |
| `GET /api?t=get&id={id}&apikey=KEY` | yes | As above |

If only the magnet exists for the release, the response is `404` with an XML
description: the client must use the magnet link received in the search.

## Configuring clients

| Client | URL to enter | Notes |
| --- | --- | --- |
| Sonarr/Radarr created by indexarr | `http://<host>:8788/torznab/<slug>` | the app appends `/api` |
| Sonarr/Radarr manual (Torznab) | `http://<host>:8788/torznab/<slug>` | same behavior |
| gextto (indexarr source) | `http://<host>:8788` | gextto appends `/api` |
| gextto (Other/Torznab) | `http://<host>:8788/api` | full endpoint |
| curl / script | `http://<host>:8788/api` | add `?apikey=…` or `X-Api-Key` |
| Prowlarr-compatible dashboard | `http://<host>:8788/api/v1/indexer` | read-only |

Per-indexer examples:

```bash
# capabilities of MirCrew only
curl 'http://localhost:8788/torznab/mircrew/api?t=caps'

# TV search on MirCrew only
curl 'http://localhost:8788/torznab/mircrew/api?t=tvsearch&q=Reacher&season=3&ep=1&apikey=KEY'

# same indexer with the Jackett-compatible path
curl 'http://localhost:8788/api/v2.0/indexers/mircrew/results/torznab/api?t=caps'
```

## Errors

The error response is XML:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<error code="401" description="invalid API key"/>
```

| HTTP | Cause |
| --- | --- |
| 400 | unknown `t` or invalid id |
| 401 | missing or wrong API key |
| 404 | nonexistent release or `.torrent` |
| 429 | per-key rate limit exceeded (`Retry-After: 60`) |
| 500 | internal/database error |
| 503 | API key not configured |

## Health check

`GET /healthz` requires no authentication and returns JSON:

```json
{"status":"ok","version":"dev","uptime_seconds":42,"releases":1200,"indexers":3,"db_bytes":1048576}
```

## Metrics

`GET /metrics` requires no authentication (like `/healthz`) and returns the
Prometheus text format. It is meant for a local scraper or a trusted network:
do not expose the instance on the Internet without an authenticated reverse
proxy.

```text
# TYPE indexarr_releases gauge
indexarr_releases 1200
# TYPE indexarr_indexers gauge
indexarr_indexers 3
# TYPE indexarr_enabled_indexers gauge
indexarr_enabled_indexers 2
# TYPE indexarr_runs_24h gauge
indexarr_runs_24h 14
# TYPE indexarr_errors_24h gauge
indexarr_errors_24h 1
# TYPE indexarr_blocklist_rules gauge
indexarr_blocklist_rules 4
# TYPE indexarr_db_bytes gauge
indexarr_db_bytes 1048576
```

`indexarr_up`, `indexarr_uptime_seconds`, `indexarr_torrents` and
`indexarr_torrent_bytes` are also present.

## Categories

These are the standard newznab/Torznab ones. The roots emitted by `caps`:

| Id | Category |
| --- | --- |
| 1000 | Console |
| 2000 | Movies |
| 3000 | Audio |
| 4000 | PC |
| 5000 | TV |
| 6000 | XXX |
| 7000 | Books |
| 8000 | Other |

The full subcategories are in `internal/categories/categories.go` and in the
`t=caps` response.

## Administrative API `/api/v2`

`indexarr 2` exposes a versioned JSON API for definitions, native indexers and
\*arr applications. It is meant for scripts and automation: it uses the same API
keys as the Torznab server, without cookies and without CSRF.

- Authentication: `?apikey=...`, `X-Api-Key` header or `Authorization: Bearer ...`
- Errors: JSON `{"error":"..."}` with a consistent HTTP status
- Bodies: maximum 1 MiB; unknown fields rejected
- No response contains API keys, passwords, cookies, tracker URLs or
  `download_ref`

| Method | Path | Use |
|---|---|---|
| GET | `/api/v2/health` | status and version |
| GET/POST | `/api/v2/definitions` | list / create YAML definition |
| POST | `/api/v2/definitions/validate` | validate without saving |
| GET/PUT/DELETE | `/api/v2/definitions/{id}` | read (with body) / update / delete |
| GET | `/api/v2/catalog` | built-in catalog and installation status |
| POST | `/api/v2/catalog/sync` | sync the catalog into the database |
| GET/POST | `/api/v2/indexers` | list / create native indexer |
| GET/PUT/DELETE | `/api/v2/indexers/{id}` | detail / update / delete |
| POST | `/api/v2/indexers/{id}/test` | test by phase (reach/auth/search) |
| POST | `/api/v2/indexers/{id}/search` | live search, without the tracker URL |
| POST | `/api/v2/indexers/{id}/run` | one ingestion run |
| GET/POST | `/api/v2/proxies` | list / create an HTTP, HTTPS or SOCKS5 proxy |
| GET/PUT/DELETE | `/api/v2/proxies/{id}` | detail / update / delete a proxy |
| GET/POST | `/api/v2/applications` | list / connect an \*arr |
| GET/PUT/DELETE | `/api/v2/applications/{id}` | detail / update / disconnect |
| POST | `/api/v2/applications/{id}/sync` | reconcile an application |
| POST | `/api/v2/applications/sync` | reconcile all |

Example:

```sh
curl -X POST "http://localhost:8788/api/v2/indexers?apikey=$KEY" \
  -H 'Content-Type: application/json' \
  -d '{
        "definition_id": "newznab-generic",
        "name": "My Usenet",
        "proxy_id": 1,
        "query_limit": 100,
        "grab_limit": 50,
        "min_seeders": 2,
        "seed_ratio": 1.5,
        "seed_time_minutes": 60,
        "season_pack_seed_time_minutes": 90,
        "settings": {"url": "https://indexer.example/api", "apikey": "..."}
      }'
```

`proxy_id` points to a proxy created with `/api/v2/proxies`; `0` removes it.
`query_limit` and `grab_limit` are per day; `min_seeders`, `seed_ratio`,
`seed_time_minutes` and `season_pack_seed_time_minutes` are synced to the
applications. Zero means no limit or the default value. The same `seed_ratio`
and `seed_time_minutes` are passed to qBittorrent when the release is sent from
the **Send** button (`ratioLimit`/`seedingTimeLimit`).

Values whose name contains `api_key`, `apikey`, `token`, `password`, `passkey`,
`secret` or `cookie` are encrypted in the vault; the others end up in
`config_json`. The built-in definitions are currently in the `bozza` (draft)
state: they have not been verified against a live site.

### Importing the cookie from the browser

Some private sites (IPTorrents, for example) have a login protected by a CAPTCHA
and are configured with the **Cookie** of a session open in the browser. The
`GET /indexers/{id}/cookie` page (UI, CSRF) explains the procedure;
`POST /indexers/{id}/cookie` generates a command valid **only once and for 10
minutes**:

```
curl -fsSL <indexarr>/static/import-browser-cookie.py -o /tmp/import-browser-cookie.py && \
python3 /tmp/import-browser-cookie.py --indexarr <indexarr> --token <token> --domain <domain>
```

The script runs on the computer with the browser, reads the user's Firefox
profile (`cookies.sqlite`), derives the User-Agent from the running version and
sends everything to:

| Method | Path | Authentication | Body |
|---|---|---|---|
| POST | `/pair` | `X-Indexarr-Pairing: <token>` header | `{"cookie":"...","user_agent":"..."}` |

`/pair` does not use the API key or the UI password: the token is random, tied
to a single indexer, single-use and lives only in memory (a restart invalidates
it). The cookie is sealed in the vault and the User-Agent ends up in
`config_json`. Responses: `200 {"ok":true}`, `401` missing/expired token, `400`
invalid body, `503` indexer service unavailable. The script never prints the
cookie or the token.

## Prowlarr v1 compatibility (read-only)

For dashboards that read the Prowlarr API (Homepage, Organizr, …) three
equivalent routes are available, authenticated with the same API key
(`apikey`, `X-Api-Key` or `Authorization: Bearer`):

| Method | Route | Response |
|---|---|---|
| GET | `/api/v1/system/status` | application name, version, authentication |
| GET | `/api/v1/health` | list of problems (empty when everything is ok) |
| GET | `/api/v1/indexer` | list of indexers in Prowlarr format |
| GET | `/api/v1/indexer/{id}` | a single indexer |

They are **read-only** and do not create any link towards Prowlarr: they only
serve those who have a dashboard that speaks that dialect.

## Changes in 2.0.0-rc1

- `total` is the number of releases after merging and is stable across all pages; every page is full except the last.
- Live searches use shared workers and a 5 s window; late results stay in the DB.
- ExtTo follows the request category (Movies/TV) in live searches; uploader lists are not queried.
