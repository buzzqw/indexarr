# indexarr administrative JSON API

indexarr exposes two different surfaces:

| Surface | Path | Format | Use |
| --- | --- | --- | --- |
| **Torznab/Newznab** | `/api`, `/torznab/api`, `/newznab/api`, `/torznab/{slug}/api` | XML | Sonarr, Radarr, gextto, Torznab clients |
| **Administrative API** | `/api/v2/...` | JSON | scripts, automations, integrations |

This page documents the **administrative JSON API**. For the Torznab protocol
see [API.md](API.md).

---

## 1. Basics

- **Base URL**: `http://<host>:8788`
- **Prefix**: `/api/v2/`
- **Content-Type**: `application/json` for every body sent
- **No cookies and no CSRF**: meant for scripts and services
- **Maximum body**: 1 MiB per request
- **Unknown fields**: rejected with `400` (`DisallowUnknownFields`)
- **Secrets**: passwords, API keys, cookies and tokens are **write-only**.
  They are never returned in clear text; responses only report
  `has_secrets`, `has_api_key` or `has_auth`.

### Authentication

The same API key as Torznab, in one of three ways:

```http
GET /api/v2/health?apikey=KEY
GET /api/v2/health
X-Api-Key: KEY
Authorization: Bearer KEY
```

Additional keys (Settings → Additional API keys) work like the primary one. A
revoked key returns `401`.

### Errors

All errors are JSON:

```json
{"error": "indexer not found"}
```

| HTTP | Meaning |
| --- | --- |
| `400` | invalid body or parameter |
| `401` | missing, wrong or revoked API key |
| `404` | nonexistent resource |
| `409` | conflict (slug or definition already exists) |
| `413`/`400` | body over 1 MiB |
| `422` | invalid definition (with `errors[]`) |
| `503` | engine or vault unavailable |
| `500` | internal error (the detail stays in the server log) |

---

## 2. Quick start

```bash
export INDEXARR=http://127.0.0.1:8788
export KEY='your-api-key'

curl -s "$INDEXARR/api/v2/health?apikey=$KEY" | jq
curl -s "$INDEXARR/api/v2/indexers?apikey=$KEY" | jq
curl -s "$INDEXARR/api/v2/catalog?apikey=$KEY" | jq '.catalog[:3]'
```

---

## 3. Health

### `GET /api/v2/health`

```json
{"status":"ok","version":"v2.1.0-rc1-33-g59a082b"}
```

It does not require the engine: it is used to verify the process, the key and
the version.

---

## 4. Definitions

A definition is the YAML file that describes a site (connector, login, search,
download). The format is described in [DEFINITIONS.md](DEFINITIONS.md).

### `GET /api/v2/definitions`

```json
{
  "definitions": [
    {
      "id": "extto",
      "schema_version": 1,
      "family": "html",
      "state": "verificata",
      "origin": "builtin",
      "author": "indexarr",
      "hash": "…",
      "updated_at": "2026-10-26T08:00:00Z"
    }
  ]
}
```

### `GET /api/v2/definitions/{id}`

As above, plus `"body"` with the full YAML.

### `POST /api/v2/definitions`

```json
{"body": "schema: 1\nmeta:\n  id: my-site\n…"}
```

- `201` with the metadata of the created definition;
- `422` with `{"error":"invalid definition","errors":["…"]}` if the YAML is not
  valid;
- `409` if the id already exists, unless `?overwrite=1` (or `true`/`yes`).

Definitions created through the API have `origin: "local"`: the catalog does not
overwrite them.

### `PUT /api/v2/definitions/{id}`

Same body as creation. The id in the body must match the path. The definition
becomes `origin: "local"` even if it was builtin.

### `DELETE /api/v2/definitions/{id}`

`204` if deleted. `409` if at least one indexer uses it.

### `POST /api/v2/definitions/validate`

Saves nothing:

```json
{"valid": true, "id": "my-site", "name": "My Site"}
```

On error: `422` with `{"valid":false,"errors":[…]}`.

---

## 5. Catalog

### `GET /api/v2/catalog`

Lists the definitions included in the binary:

```json
{
  "catalog": [
    {"id":"1337x","state":"stabile","author":"indexarr","hash":"…","installed":true}
  ]
}
```

`installed` indicates whether the definition is already in the database.

### `POST /api/v2/catalog/sync`

Aligns the database with the catalog included in the binary. Definitions with
`origin: "local"` are not touched.

```json
{"written": 12}
```

---

## 6. Indexers

### Model

```json
{
  "id": 15,
  "definition_id": "mircrew",
  "protocol": "torrent",
  "slug": "mircrew",
  "name": "MirCrew",
  "enabled": true,
  "priority": 25,
  "tags": [],
  "query_limit": 0,
  "grab_limit": 0,
  "proxy_id": null,
  "min_seeders": 0,
  "seed_ratio": 0,
  "seed_time_minutes": 0,
  "season_pack_seed_time_minutes": 0,
  "interval_seconds": 3600,
  "has_secrets": true,
  "created_at": "2026-10-24T00:38:16Z",
  "updated_at": "2026-10-26T10:56:53Z"
}
```

### `GET /api/v2/indexers`

```json
{"indexers": [ … ]}
```

### `GET /api/v2/indexers/{id}`

A single indexer object. `404` if it does not exist.

### `POST /api/v2/indexers`

```json
{
  "definition_id": "torrentleech",
  "name": "TorrentLeech",
  "slug": "torrentleech",
  "protocol": "torrent",
  "enabled": true,
  "priority": 25,
  "tags": ["tv"],
  "interval_seconds": 1800,
  "settings": {
    "username": "user",
    "password": "secret",
    "categories": "2000,5000"
  }
}
```

- `definition_id` and `name` are mandatory;
- `slug` is normalized from the name if missing;
- `settings` accepts strings, numbers and booleans; secret keys
  (`password`, `apikey`, `token`, `cookie`, …) go into the vault;
- the mandatory settings of the definition must be present, otherwise
  `400 missing required setting: <name>`;
- `201` with the created model.

### `PUT /api/v2/indexers/{id}`

Same fields. Omitted fields stay unchanged; a missing or empty secret key keeps
the saved value. To delete a secret use `"settings": {"password": ""}`.

### `DELETE /api/v2/indexers/{id}`

`204`. The engine invalidates the cached connection.

### `POST /api/v2/indexers/{id}/test`

Runs the test phases (reachability, capabilities, login, search):

```json
{
  "ok": true,
  "phases": [
    {"name":"reach","ok":true,"message":"","duration_ms":120},
    {"name":"caps","ok":true,"message":"","duration_ms":80},
    {"name":"search","ok":true,"message":"","duration_ms":900}
  ]
}
```

A successful test also records a health check and clears the last error from
the state.

### `POST /api/v2/indexers/{id}/search`

```json
{
  "mode": "search",
  "query": "Reacher",
  "season": 0,
  "episode": 0,
  "imdbid": "",
  "tmdbid": "",
  "tvdbid": "",
  "limit": 50,
  "offset": 0,
  "categories": [5000]
}
```

`mode`: `search`, `tvsearch`, `movie`, `music`, `book`.

```json
{
  "items": [
    {
      "title": "Reacher S03E01 1080p",
      "guid": "…",
      "size": 1234567890,
      "seeders": 12,
      "leechers": 2,
      "categories": [5040],
      "published": "2026-10-25T10:00:00Z",
      "infohash": "…",
      "magnet": "magnet:?xt=urn:btih:…"
    }
  ]
}
```

### `POST /api/v2/indexers/{id}/run`

Runs one refresh (latest releases list or fixed search):

```json
{"seen":500,"new":494,"updated":0,"blocked":0,"skipped":0,"torrents":12}
```

---

## 7. Proxies

### Model

```json
{"id": 1, "kind": "http", "url": "http://proxy.example:8080", "has_auth": true}
```

`kind`: `http`, `https`, `socks5`, `socks5h`.

### Endpoints

| Method | Path | Notes |
| --- | --- | --- |
| `GET` | `/api/v2/proxies` | list |
| `POST` | `/api/v2/proxies` | create (`201`) |
| `GET` | `/api/v2/proxies/{id}` | detail |
| `PUT` | `/api/v2/proxies/{id}` | update |
| `DELETE` | `/api/v2/proxies/{id}` | `204` |

Creation/update body:

```json
{
  "kind": "http",
  "url": "http://proxy.example:8080",
  "username": "user",
  "password": "secret",
  "clear_auth": false
}
```

An empty `password` keeps the saved one; `clear_auth: true` removes it.

---

## 8. Applications

The applications are Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3,
LazyLibrarian.

### Model

```json
{
  "id": 1,
  "kind": "sonarr",
  "name": "Sonarr",
  "url": "http://192.168.1.10:8989",
  "sync_level": "full",
  "enable_rss": true,
  "enable_automatic_search": true,
  "enable_interactive_search": true,
  "min_seeders": 0,
  "categories": [5000, 2000],
  "last_sync_at": "2026-10-26T10:00:00Z",
  "last_error": "",
  "has_api_key": true
}
```

Allowed `kind` values: `sonarr`, `radarr`, `lidarr`, `readarr`, `whisparr`,
`mylar3`, `lazylibrarian`.

`sync_level`: `disabled`, `add-remove`, `full`.

`enable_rss`, `enable_automatic_search` and `enable_interactive_search` are the
flags with which the indexer is created in the app. `min_seeders` (0 = the
indexer value) and `categories` (standard Torznab id, array or CSV string; empty
= all the categories supported by the app) complete the profile of the single
application. On update, omitted fields keep the current value.

### Endpoints

| Method | Path | Notes |
| --- | --- | --- |
| `GET` | `/api/v2/applications` | `{"applications":[…]}` |
| `POST` | `/api/v2/applications` | create (`201`) |
| `GET` | `/api/v2/applications/{id}` | detail |
| `PUT` | `/api/v2/applications/{id}` | update |
| `DELETE` | `/api/v2/applications/{id}` | `204` |
| `POST` | `/api/v2/applications/{id}/sync` | sync one app |
| `POST` | `/api/v2/applications/sync` | sync all |

Creation:

```json
{
  "kind": "sonarr",
  "name": "Sonarr",
  "url": "http://192.168.1.10:8989",
  "api_key": "sonarr-api-key",
  "sync_level": "full",
  "enable_rss": true,
  "enable_automatic_search": true,
  "enable_interactive_search": true,
  "min_seeders": 0,
  "categories": [5000]
}
```

`api_key` is mandatory on creation; on update an empty string keeps the saved
one.

Sync response:

```json
{"added": 12, "updated": 3, "deleted": 0, "skipped": 1}
```

`POST /api/v2/applications/sync` returns an array with the result of each app,
including `error` when one fails.

---

## 9. Prowlarr compatibility (read-only)

For dashboards (Homepage, Organizr, …) indexarr exposes the same read-only
surface as Prowlarr:

| Method | Path | Response |
| --- | --- | --- |
| `GET` | `/api/v1/system/status` | `appName`, `version`, `branch`, `authentication`, `startTime` |
| `GET` | `/api/v1/health` | empty array (no problems) |
| `GET` | `/api/v1/indexer` | list in Prowlarr format |
| `GET` | `/api/v1/indexer/{id}` | a single indexer |

These routes do not accept writes and do not forward searches. Each indexer
reports `baseUrl` = `<base>/torznab/<slug>` and `apiPath` = `/api`, so a client
that appends `/api` gets the per-indexer Torznab endpoint.

---

## 10. Useful examples

### Create an ExtTo indexer and test it

```bash
KEY='…'
BASE='http://127.0.0.1:8788'

curl -s -X POST "$BASE/api/v2/indexers?apikey=$KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "definition_id":"extto",
    "name":"ExtTo – MIRCrew",
    "protocol":"torrent",
    "enabled":true,
    "settings":{"filter":"c=TV","query":"MIRCrew"}
  }' | jq

curl -s -X POST "$BASE/api/v2/indexers/48/test?apikey=$KEY" | jq
```

### Search for an episode

```bash
curl -s -X POST "$BASE/api/v2/indexers/15/search?apikey=$KEY" \
  -H 'Content-Type: application/json' \
  -d '{"mode":"tvsearch","query":"Reacher","season":3,"episode":1,"limit":10}' | jq
```

### Sync all apps

```bash
curl -s -X POST "$BASE/api/v2/applications/sync?apikey=$KEY" | jq
```

### Validate a definition before saving it

```bash
jq -Rs '{body: .}' my-site.yaml > body.json
curl -s -X POST "$BASE/api/v2/definitions/validate?apikey=$KEY" \
  -H 'Content-Type: application/json' \
  --data @body.json | jq
```

---

## 11. Security notes

- Do not put the API key in shared URLs or in logs: prefer
  `X-Api-Key` or `Authorization: Bearer`.
- The API never returns a secret. To rotate a password, send the new value; to
  remove it, send an empty string.
- Test, search and refresh calls can contact the real sites: they respect the
  per-host limits and the configured proxies, but a test against a site with a
  CAPTCHA can fail.
- Definitions created through the API are `local` and are not overwritten by
  the catalog.
