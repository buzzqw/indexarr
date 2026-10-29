# indexarr definitions (format v1)

A definition is a YAML document that describes **one indexer**: metadata,
user settings, category map, authentication, search and offline tests. The
engine interprets it as data: there is no executable code inside a definition.

This document describes the `schema: 1` format accepted by
`internal/definition`, the `internal/engine` connector that executes it and the
`indexarr-def` CLI. For context and product decisions:
[ARCHITECTURE.md](ARCHITECTURE.md).

Alongside full definitions the catalog has a layer of **lightweight entries**
generated from a historical snapshot of public metadata (name, URL, type,
language, categories, family). They are not definitions, they do not import
Cardigann and they do not contact Jackett or Prowlarr: see
[`CATALOG.md`](CATALOG.md) for states, families, the Italian list and
regeneration commands.

## Structure of a file

```yaml
schema: 1
connector: ""          # optional: name of a native connector
meta: {}               # required
settings: []
categories: []
capabilities: {}
auth: {}               # optional
session: {}            # optional: persistent browser session (FlareSolverr)
search: {}             # required when connector is absent
download: {}           # optional: magnet/torrent from the details page
health: {}             # optional
tests: []              # optional, but required for the catalog
```

Unknown keys are **rejected**: a typo is not ignored. The file must contain a
single YAML document.

## `meta`

| Field | Required | Rules |
|---|---|---|
| `id` | yes | slug `^[a-z0-9][a-z0-9-]*$`, stable and unique |
| `name` | yes | readable name, not empty |
| `type` | yes | `public`, `semi-private`, `private` |
| `protocol` | yes | `torrent`, `usenet` or `ed2k` |
| `urls` | yes | at least one absolute `http`/`https` URL; the first is the primary one, the others are mirrors. When an address is no longer there (the name does not resolve, nothing answers on the port, host unreachable, certificate of another site) or Cloudflare answers that the site behind is down (`502`, `504`, `520`-`526`, `530`; not `503`, which is also the anti-bot check), the connector retries the same request on the other addresses, in rotation, and stays on the one that answers: if the mirror dies too it goes back to the first. A slow site or one that refuses does not cause an address change. Only requests to the definition's hosts change address, not those to trackers or CDNs. The switch also applies to FlareSolverr sessions: the browser reports a dead domain with `net::ERR_NAME_NOT_RESOLVED` (or `ERR_CONNECTION_REFUSED`, `ERR_ADDRESS_UNREACHABLE`, `ERR_CERT_COMMON_NAME_INVALID`) and the session is not restarted because of this. A mirror belongs in the list only if it serves the same pages at the same path: a domain that has moved to something else (e.g. a betting site) must be removed |
| `language` | no | BCP 47 tag, for example `it-IT` |
| `author` | no | author or maintainer |
| `description` | no | short description |
| `updated` | no | date of the last revision, for example `"2026-10-23"` |
| `derived_from` | no | slug of the tool that produced a draft, for example `cardigann`; a derived draft stays local and the catalog rejects it |
| `mirror` | no | `official` or `unofficial`. `unofficial` signals that the definition only works through an unofficial mirror: the UI always shows the "Unofficial mirror" badge and a warning in the form, even when the state is `verificata`. Additive: definitions without the field remain valid |
| `interval` | no | Go duration, for example `15m`; it is a suggestion for the indexer's refresh interval. Empty = no suggestion, the global value applies. The minimum is 60s |
| `latest` | no | `true` (default) if the site publishes a list or feed of new items; `false` if it can only answer a search |
| `insecure_tls` | no | `true` only for a `public` definition: the site serves an incomplete TLS chain, so the engine skips certificate verification **for this indexer**. The UI shows a "TLS not verified" badge and a warning in the form. It is rejected on `semi-private` and `private` |

### TLS exception per definition

A public site can serve an incomplete TLS chain (the signer's intermediate
certificate is missing) and become unreachable for Go, which refuses the
connection with `x509: certificate signed by unknown authority`. It is not a
selector to fix: it is the server.

`meta.insecure_tls: true` authorizes the engine to skip certificate
verification **only for that indexer's requests**, and only when
`meta.type` is `public`. The shared transport is not modified: the
connector derives one from it with the same politeness, the same SSRF guard and
the same configuration, changing only the TLS check. A `private` or
`semi-private` definition that asks for the exception is rejected when the
connector is opened, because credentials must never travel to an unverified
certificate.

The UI shows the «TLS not verified» badge on the indexer and a warning in the form
(translated into the 7 languages), so the user knows that for that source the
server's identity is not proven and that it is advisable to check the magnet
hash. In the catalog the definition stays `verificata` if search and grab were
tested; the proof of the broken chain lives in the YAML file's comment.

### Derived drafts

`derived_from` marks a definition produced by a conversion tool,
today only the Cardigann converter (`derived_from: cardigann`). It is not a
migration path nor an import: the draft stays **local**, the UI and
the API have no route to import it, and the built-in catalog rejects it.

The `cardigann-import` author is a marker equivalent to `derived_from`,
compared case-insensitively. To enter the catalog a draft
must be rewritten by observing the site: selectors verified on real fixtures, our
own author and date, `derived_from` removed. `make def-validate` runs this policy
on the catalog directory with `validate -catalog`.

### Suggested interval

`meta.interval` is additive and optional. A "last 24 h" feed needs
more frequent refreshes than a site with search, so the definition can
suggest a Go duration (`15m`, `2h`). When the indexer is created the field
"Refresh interval" is pre-filled with this value, outside
Advanced; the user can change it with the quick choices (15 minutes–24 hours), with
a value in seconds, or set it to `0` to use the global interval. A
value below 60s is rejected. The effective value appears in the list and
in the card; «Reduce the frequency» doubles the one of only the source that
answered 429. The interval concerns automatic refreshes: live searches
from the apps are not delayed. The format stays `schema: 1`.

### List of new items

`meta.latest` is additive and optional. Absent it means `true`: the definition has
a list or feed of new items and the scheduler can run a periodic run.
A site that can **only search** (for example The Pirate Bay, TorrentsCSV,
LimeTorrents) declares `latest: false`: it has no scheduled runs, does not appear in the
RSS feeds of the *arr apps and is queried by live search when an app
searches. The `torznab`, `newznab` connectors and feeds leave the value absent,
so they stay `true`.

```yaml
meta:
  id: thepiratebay
  # ...
  latest: false
```

The field does not change search capabilities: a `latest: false` definition
remains queryable for `search`, `tv-search`, `movie-search`, etc. according to
`capabilities`.

## `settings`

Each setting becomes a field in the indexer form.

| Field | Required | Notes |
|---|---|---|
| `name` | yes | slug; it is the key used in templates |
| `type` | no | form type: `text`, `password`, `select`, `checkbox`, `number`… |
| `label`, `help` | no | text shown in the UI |
| `default` | no | value applied when the user does not enter one |
| `required` | no | if true the connector rejects the indexer without a value |
| `options` | no | allowed values for `select` |

Names must be slugs: lowercase letters, digits and hyphens. That is why the
valid spelling of an API key is `apikey` (not `api_key`); the engine synchronizes
`apikey` and `api_key` when needed.

Settings whose name or type suggests a secret (`password`, `token`,
`apikey`, `secret`, `cookie`, `passkey`) are treated as sensitive and
redacted in error messages.

### `freeleech`: free releases only

A definition that can distinguish freeleech declares the setting
`freeleech` (a checkbox, `type: checkbox`). The engine and the UI use the name
`freeleech` as a convention, so the form shows the checkbox with the translated
label without dedicated code:

```yaml
settings:
  - { name: freeleech, type: checkbox, label: "Freeleech only", default: false }
```

The filter can work in two ways:

- **on the site**: the search parameter uses `{{ settings.freeleech | onoff }}`
  (a `1`/`0`) or `{{ settings.freeleech | ifset("on") }}` (the value
  the site expects, for example `free=on`), and the site already returns only
  free releases;
- **row by row**: `search.response.fields.freeleech` reads a marker
  (`FREE`, `1`, `0`, …) and the engine discards non-free rows before
  storing them, answering the apps and reading the list of new items. The filter also applies
  to live search and to the periodic run.

A value of the field other than `0`, `false`, `no` and `off` means freeleech.
If the definition does not declare the setting, the checkbox does not appear.


## `categories`

Maps the site's categories to the standard Torznab/Newznab categories.

```yaml
categories:
  - { site: "5",  standard: 5040, desc: "TV HD" }
  - { site: "12", standard: 2040, desc: "Film HD" }
```

- `site` is the id or string used by the site;
- `standard` must be between `1000` and `8999`;
- `desc` is optional.

When a search requests standard categories, the engine translates them into the
site's ids through this table. An unrecognized category becomes `8000`
(Other).

## `capabilities`

They declare which modes and parameters the indexer supports. The allowed values are
`q`, `season`, `ep`, `imdbid`, `tvdbid`, `tmdbid`, `tvmazeid`, `rid`, `artist`,
`album`, `author`, `title`, `year`, `genre`.

```yaml
capabilities:
  search: [q]
  tv-search: [q, season, ep, imdbid]
  movie-search: [q, imdbid]
```

Modes without parameters are not published in the connector's capabilities.
The published categories are the `standard` ids declared in
`categories`.

## `auth`

```yaml
auth:
  strategy: form
  steps:
    - get: /login.php
      extract:
        csrf: { selector: 'input[name="csrf_token"]', attr: value }
    - post: /login.php
      form:
        username: "{{ settings.username }}"
        password: "{{ settings.password }}"
        csrf_token: "{{ csrf }}"
  success: { selector: 'a[href="/logout.php"]' }
  failure:
    - { selector: "div.error", message: text }
  refresh_after: 6h
```

### Strategies

| Strategy | Behavior |
|---|---|
| `none` | no authentication |
| `api_key` | adds `apikey` to the query if the definition does not already use `settings.apikey`/`settings.api_key`; otherwise the value must be inserted explicitly in the templates |
| `basic` | HTTP Basic with `settings.username` and `settings.password` |
| `token` | header `Authorization: Bearer <settings.token>` |
| `form` | runs `steps` on first use and keeps cookies and extracted values |
| `cookie` | like `form`; useful when the flow is already described by `steps` |

`refresh_after` is a Go duration (`30m`, `6h`, `1h30m`). If absent, the engine
reuses the session for 6 hours. With `form`/`cookie` the login happens lazily and
is repeated on `401`/`403` once per search.

`logged_in` (optional, requires a `selector`) is a sign present on every page
of an authenticated user, usually the logout link. It is needed by forums that keep
a single session per account: if the same account logs in from another browser,
indexarr's session drops and the details page answers `200` as a guest,
without the link. When a details page does not contain the sign, the engine
logs in again once and re-reads the page; two pages read in parallel that find
the session dropped cost only one login.

```yaml
auth:
  success: { selector: 'a[href*="mode=logout"]' }
  logged_in: { selector: 'a[href*="mode=logout"]' }
```

### `steps`

Each step has **`get` or `post`** (never both) and can have:

- `form`: body fields, only for `post`;
- `headers`: extra headers;
- `extract`: values to extract from the response and make available to later
  steps and to the search.

Extractors are evaluated in this order:

```yaml
extract:
  csrf: { selector: 'input[name="csrf"]', attr: value }  # empty attr = text
  token: { json: "$.token" }
  nonce: { regex: 'nonce=([a-f0-9]+)' }                  # group 1 is required
```

`Set-Cookie` cookies are captured automatically; they must not be declared.

### Conditions

`success` and each `failure` entry can have `selector` and/or `status`:

- with both, both must match;
- `message: text` uses the text of the selected node as the error message;
- without `success`, the login succeeds with a `2xx`/`3xx` status;
- `failure` entries are evaluated before `success`.

Extracted values and settings are available in the templates; passwords
never appear in returned messages.

## `session`

The section is **optional** and additive. It is for sites that protect **all**
pages with Cloudflare and keep state in a browser: MirCrew is the reference
case. With a persistent session every indexer request —
login, search, details page and download — goes through FlareSolverr with
`sessions.create` + `request.get`/`request.post`, not through the ordinary
HTTP transport. The single-request solver from cycle 4 remains for sites that only have
an occasional check: it keeps no cookies or session and cannot repeat a
POST.

```yaml
session:
  mode: flaresolverr
  endpoint: "{{ settings.flaresolverr }}"
  name_prefix: mircrew-idx
  max_age: 12h
  idle_timeout: 30m
  timeout: 60s
```

| Field | Required | Rules |
|---|---|---|
| `mode` | no | empty or `flaresolverr` (empty means `flaresolverr`) |
| `endpoint` | no | FlareSolverr `http`/`https` URL or template; empty uses the global URL from network settings |
| `name_prefix` | no | slug used for session ownership; empty = `indexarr-<id>` |
| `max_age` | no | Go duration; beyond this time the session is closed and recreated. Default 12h |
| `idle_timeout` | no | Go duration; after this time without requests the browser is closed and recreated on the next request. Default 30m |
| `timeout` | no | Go duration for a FlareSolverr command. Default 60s |

Behavior:

- **All** connector requests use the session, including the login POST.
  A FlareSolverr session serves one request at a time, so the
  connector serializes them.
- If a command fails (crashed tab, closed browser, unsolved check)
  the session is destroyed, a new one is created and the request is
  repeated **once**. The new browser has no cookies: the next lazy
  login recreates them.
- After `max_age` the session is recycled in the same way.
- After `idle_timeout` without requests the browser is closed: an indexer that
  nobody queries does not keep a whole Chromium alive. On the next
  request the connector recreates the session and logs in again. The same
  check runs in the background every two minutes and at process shutdown,
  so stopping indexarr does not leave browsers hanging.
- When a connector is discarded (indexer saved, settings changed,
  definition updated) its session is closed immediately, not on the next
  request.
- On the first request, orphan sessions starting with
  `name_prefix + "-"` are closed; those of other programs and those still alive from
  another connector of the same process are not touched. Cleanup is
  therefore limited to the declared prefix.
- Requests use `disableMedia: true`, like the Python service: forum
  pages contain images and slow media that would make the timeout expire.

The session mode does not change the format of the other fields: `auth.steps`,
`search` and `download` stay the same. A definition without `session` behaves
exactly as before and `schema` stays `1`.

### Cloudflare without a session

A definition must not declare `session` just because the site is behind
Cloudflare. With FlareSolverr configured, the transport repeats the GET that
received the challenge and the search works; the persistent session is
only needed when the site keeps state in the browser (login, cookies) or when
a POST is needed that the single-request solver cannot repeat.

Known limit: the solver returns text, not binaries. A `.torrent` protected
by Cloudflare cannot be downloaded: the definition is only valid if the row exposes a
magnet or if the file is on an unprotected path. BT.etree is the measured case:
the search is behind a challenge, `download.php` answers `200
application/x-bittorrent` even without the solver. A definition that searches but does not
download must not be written half-way.

## Templates

Templates are deliberately minimal:

```yaml
"{{ query.keywords }}"
"{{ settings.apikey }}"
"{{ csrf }}"
"{{ categories | join(',') }}"
"{{ page }}"
```

Available variables:

- `settings.<name>` — declared settings only;
- `query.keywords`, `query.season`, `query.ep`, `query.imdbid`, `query.tvdbid`,
  `query.tmdbid`, `query.tvmazeid`, `query.rid`, `query.artist`,
  `query.album`, `query.author`, `query.title`, `query.year`;
- the values extracted during login or `search.preflight` (`{{ csrf }}`, `{{ token }}`, …);
- `categories` — site ids matching the requested categories;
- `page`, `offset`, `limit` — scalar pagination values;
- `item.title`, `item.guid`, `item.details`, `item.download`, `item.season`,
  `item.episode` — only in `download.before`: the release chosen by the client. The
  season and episode come from the search (or from the link's parameters).

The only filter is `join(<separator>)`, applicable to `categories`, and
`onoff`/`ifset(<value>)`, applicable to a setting with a checkbox
(`true`/`false`): `onoff` renders `1`/`0`, `ifset("on")` renders `on` when the
checkbox is active and nothing when it is off. An unknown name or an unknown
filter is an error, not an empty string.

### TV keywords format

For a TV search, `query.keywords` contains the query with the
season/episode marker, following the Jackett/Prowlarr model:

- `q + " S04E01"` when both season and episode are present;
- `q + " S04"` when only the season is present;
- the original query if the marker is already present or if the mode is not TV.

`query.keywords_raw` always contains the original text. `query.keywords_title`
is the original text without the season/episode markers («The Murdoch
Mysteries S13E01» → «The Murdoch Mysteries»), for a site that searches every word
in topic titles, where the marker never appears; the season and episode
filters still read the full query. The format is chosen
with an additive field of `search`, default `sxxeyy`:

```yaml
search:
  keywords_format: sxxeyy   # default: S04E01
  # keywords_format: 1x05   # S04E01 -> 4x01
  # keywords_format: separate  # keywords stays the query; use query.season/query.ep
```

With `separate` the definition must pass season and episode with dedicated
parameters, for example:

```yaml
params:
  q: "{{ query.keywords }}"
  season: "{{ query.season }}"
  ep: "{{ query.ep }}"
```

The field is additive: existing definitions without `keywords_format`
keep working and receive the `sxxeyy` format.

## `search`

Required when `connector` is absent. If `connector` is present and `search`
is omitted, the native connector handles the search; if `search` is present it is
validated anyway.

### `preflight`

Some sites generate a CSRF token on the page and require it in the search.
`preflight` makes a request once before the search, extracts the values
and makes them available as `{{ name }}` in the search template.

```yaml
search:
  preflight:
    get: /                      # or post, never both
    extract:
      csrf: { selector: 'input[name="_csrf"]', attr: value }
  request:
    post: /topic/index
    form:
      _csrf: "{{ csrf }}"
      keywords: "{{ query.keywords }}"
```

| Field | Required | Rules |
|---|---|---|
| `get` / `post` | one of the two | relative path or http/https URL |
| `params` | no | preflight query string |
| `form` | no | body, only with `post` |
| `headers` | no | preflight headers |
| `extract` | yes | at least one name; each extractor uses `selector`/`attr`, `json` or `regex` |

The preflight uses the same cookies, proxy and solver as the connector. An
extracted empty value is an error: the search does not start with a missing token. The
values stay in the connector's session and are renewed on every
search.

### `request`

```yaml
search:
  request:
    get: /torrents.php          # or post, never both
    params:                     # query string
      search: "{{ query.keywords }}"
      cat: "{{ categories | join(',') }}"
      page: "{{ page }}"
    form:                       # body, only with post
      sort: "{{ settings.sort }}"
    headers:
      Accept: text/html
```

`params` are added to the query even for `POST` requests. Relative paths
(including those starting with `/`) are appended to the path of the first URL
in `meta.urls`; an absolute URL is used only if the scheme is `http`/`https`.
Redirects are revalidated by the transport.

### `pagination`

```yaml
pagination: { type: page, start: 1, step: 1, max_pages: 5,
              param: page, stop_when: empty_rows }
```

| Type | Meaning |
|---|---|
| `none` (default) | a single request |
| `page` | numbered pages; `start` default 1, `step` default 1 |
| `offset` | increasing offset; `start` default 0, `step` default the request's `limit` |
| `cursor` | accepted by validation, **not yet implemented** by the engine: the search returns an explicit error |

`max_pages` default 1 (the first page). `stop_when` only accepts
`empty_rows`: pagination stops when a page produces no rows.
`param`, if present, sets the indicated query parameter (without
overwriting a value already produced by `params`).

### `response`

```yaml
response:
  type: html                 # html | json | xml
  rows: { selector: "table.torrents > tbody > tr" }
  fields:
    title: { selector: ["a.title", "td.name a"], filters: [trim] }
    size: { selector: "td.size", type: size }
  required: [title, size]
```

#### Charset

HTML responses (search, details page, login messages) are converted to UTF-8
before extraction. The charset comes from the `Content-Type` header,
then from a `<meta charset>` in the first 2 KiB; `windows-1251` (many
Russian trackers) and `ISO-8859-1`/`windows-1252` are handled. A body
that is already valid UTF-8 is not touched, so a page returned by
FlareSolverr is not decoded twice. Definitions must not
declare anything.

#### Rows

- **HTML**: `rows.selector` is a CSS selector, or a list of fallbacks;
  the first one that finds nodes is used. It is required.
- **JSON**: `rows.path` is a dotted path; numeric segments index
  arrays (`data.items`, `files.0.url`). If empty, the root must be an
  array.
- **XML**: `rows.path` is required (`rss.channel.item`); namespaces are
  matched on the local name.

#### Fields

An HTML field uses `selector` (list = fallback) and `attr`. With an empty `attr` or
`text` the node's text is extracted, otherwise the indicated attribute.

A JSON field uses `path`; if absent, it uses the field name. An XML field uses
`path` with the forms `title`, `title.#text`, `enclosure@url` and `@attr` (row
attribute).

The special names recognized by normalization, when declared, are:

`title`, `guid`, `details`, `download`, `magnet`, `infohash`, `size`, `seeders`,
`leechers`, `date` (or `published`), `category` (or `categories`), `imdbid`,
`tmdbid`, `tvdbid`, `freeleech`.

A row is discarded if the title is missing, if a field in `required` is empty or
not convertible, or if it has neither a magnet nor a download link nor a details link.
The title is normalized for whitespace; missing categories become
`8000`. Duplicates from the same search are removed by infohash, then GUID,
then download URL.

#### Types

| Type | Conversion |
|---|---|
| `text` | string, default |
| `int` | integer; tolerates thousands separators, non-numeric values become 0 |
| `size` | bytes or `1.5 GB`, `1,5 GiB`, etc. |
| `date` | Go layout from `layouts`, then a set of common formats; also accepts Unix timestamps |
| `url` | relative URL resolved against the response or the base URL |
| `category` | site id mapped by `categories` |

Types also count in `required`: a zero `size`, a zero `int`, an invalid
date or an empty URL cause the row to be discarded.

#### Filters

Filters are applied in the declared order:

| Filter | Effect |
|---|---|
| `trim` | removes surrounding spaces |
| `lower` / `upper` | changes case |
| `replace: "old:new"` | replaces all occurrences |
| `query_param: name` | extracts the parameter from the URL in the value |
| `prefix: "x"` / `suffix: "x"` | adds text |
| `regex: "pattern"` | uses group 1 of the first match; no match = empty string |
| `base64` | decodes base64 (standard or URL alphabet, with or without `=`); an invalid value becomes empty. Used by sites that hide the magnet in an encoded attribute |

Example:

```yaml
category:
  selector: "a.cat"
  attr: href
  filters: [{ query_param: cat }]
  type: category
```

### eD2k

`protocol: ed2k` describes a source that publishes
`ed2k://|file|name|size|hash|/` links instead of magnets or `.torrent`. The
release keeps the link and its MD4 hash; there is no swarm to count and
no file is saved. The Torznab server exposes the link in `<link>`,
in the enclosure and in the `ed2k` attribute, with `<guid>` equal to the hash. With
`torznab_proxy_ed2k` active (default) `<link>` and enclosure go through indexarr's
download link, which redirects to the `ed2k://` and records the grab; the
`ed2k` attribute always carries the direct link, which is what gextto reads. Sonarr,
Radarr and the other Servarr programs do not receive these indexers, because
they would only know how to pass the link to a torrent client.

Grabbing an eD2k release returns a redirect to the `ed2k://` link; the UI
also shows it as text to copy. An aMule client configured with
`kind: amule` receives the link via `POST /api/v1/downloads` of its Web API.
Downloading from the list, grabbing from the download link and sending to aMule
go through indexarr and leave a `grab` row with state `ed2k`, visible in
Log → Download with its card and its per-source column. Gextto asks for the
download link first, so its grabs are counted too; a client that opens
the `ed2k` attribute without asking indexarr anything stays invisible.

A **live** search on an eD2k indexer opens at most 10 details pages
(and does not skip them as for torrents): without that step the row would only have
an encrypted reference, which a client would not recognize as eD2k. The limit
keeps the search bound to a few pages; the complete archive is built by the
periodic run over the forum lists.

### Search on a local index (`search.index`)

Some forums have no usable search (flood control) but publish
alphabetical lists of titles. `search.index` replaces the search
request with a local read of those pages:

```yaml
search:
  index:
    max_topics: 5
    cache: 6h
    response:
      type: html
      rows: { selector: 'a.postlink-local[href*="viewtopic.php?t="]' }
      fields:
        title:   { self: true }
        details: { self: true, attr: href, type: url }
    pages:
      # TV: a single topic over several pages, followed with the «Next» link
      - { get: /viewtopic.php?t=61463, standard: 5000, next: "a.right-box.right" }
      - { get: /viewtopic.php?t=3941486, letter: A, standard: 2000 }
      - { get: /viewtopic.php?t=3941485, letter: B, standard: 2000 }
      # ... one page per letter
```

The engine reads only the pages without `letter` and the one for the query's first
letter (`#` for numbers and symbols), extracts the title/URL pairs, compares
the title locally (tolerating punctuation and common words) and opens only
the first `max_topics` topics. A query term counts only when it is the start
of a word in the title: «part» finds «Parte», but «It» does not find «Hitman» and
«Up» does not find «Superman», because a false positive would hide the fallback.
Letters separated by dots or hyphens also count joined, in the query and in the
title: «9-1-1» finds «911», «S.W.A.T.» finds «SWAT» and vice versa.
Book and music searches do not read the index: they go directly to
`search.request`. Each list is cached for `index.cache` (empty = 30
minutes): lists change rarely and a title missing from an old list still arrives
at the fallback. If
no title matches and the definition also has a normal `search.request`,
the engine uses that as a fallback. Index pages do not become
releases: only the links found by opening the chosen topics enter the archive.

`self: true` in an HTML field reads the row node itself instead of a
descendant: it is needed when the row is already the `<a>` element (lists of links).

`next` (per page) is the selector of the link to the next page of a list
split over several pages, like a phpBB topic with «Page 1 of 3». The engine
follows it up to 10 pages and caches the pages as a single list. Without
`next` it reads only the first page: on DDUnlimited's TV topic the first page
goes from `#` to `H`, so series from I to Z were never found.

A link pointing to one of the `pages` is navigation, not a title: the letter
bar at the top of a movie topic points to the other letter topics and
is discarded. A list that answers without the `auth.logged_in` marker
(session dropped because the same account logged in from another browser) triggers
a new login once and is re-read.

`index.forum` is optional: it indicates the forum that contains the letter pages
(for example `f=1988`) and is only used by the UI, which shows it read-only in
the indexer form together with the page without a letter. The engine reads the
`pages`, not the forum.

### Flood control (`search.blocked`, `search.cache`, `search.interval`)

A forum with flood control accepts one search every few minutes and answers the
others with `200` and a warning box instead of the results. To the
engine that page looked like «no results». Two optional fields handle
it:

```yaml
search:
  interval: 65s
  cache: 12h
  blocked: { selector: "#message p", contains: "non ti è permesso", message: text }
```

- `blocked` (requires a `selector`) recognizes the refusal page. With
  `contains` it only counts if the selector's text contains that phrase (case-insensitive):
  DDUnlimited uses the same box also for
  "no topics or posts matched this search criterion" and for "the
  following words were ignored", which are not refusals. The search
  fails with the site's message ("You are not currently allowed to
  search…"), which appears in the indexer's history, instead of returning zero
  releases. If the first pages have already answered, the engine keeps them.
- `cache` reuses, for that time, the result pages of the same `GET`
  request. Gextto and the Arr apps ask for the same series once per episode:
  without the cache every request after the first is refused. A refusal page
  never enters the cache.
- `interval` (requires `cache`) is the minimum time between two search requests
  sent to the site, for a flood control that counts per account. Searches
  go one at a time and the interval starts from the previous response
  (including a retry after a new login), because the site counts from the
  search it processed. The first page
  of a search waits for its turn up to two intervals, never beyond the
  caller's deadline minus 15 s; if the turn is further away the search does not
  start, ends up in the queue and answers with a "deferred" error (in the history
  it is "canceled", not a site failure). A background job sends
  queued searches at the allowed pace and caches their pages: the
  next identical request reads them from there. A page after the
  first takes only an immediately free turn; otherwise the search keeps the
  pages read and the page goes to the queue. Every send by the background job
  leaves a `search` row in the indexer's history with the query, the
  state, the latency and the error: that is how you see whether the forum accepts the
  queued search, refuses it or discards it after the two attempts. On DDUnlimited
  on 2026-10-28 the forum accepted one search every 57-58 s (refused at
  55 s, accepted at 58 s and at 70 s), and a refusal does not restart the count.
- After a refusal no search starts for four intervals; each further
  consecutive refusal doubles the pause, up to 32 intervals (about 35 minutes on
  DDUnlimited), and the first accepted search resets it to zero. During the pause a
  live search does not wait for its turn: it goes straight to the queue with the error "the site
  refused the last search, next turn in …", without touching the site. On
  2026-10-29 the forum refused all searches from 02:06 to 03:29,
  one every 5.5 minutes, and a live search waited up to 167 s to receive
  the same refusal.

With `auth.logged_in` a search page without the login sign
(the forum dropped the session and answers as a guest) also triggers a new login
once and a re-read of the page.

With `download.expand: file`, a search with a season (`season`, or
`S02E03`/`2x03` in the query) does not open topics that name another season:
the forum search lists all seasons of a series, and opening them costs one
page each without being able to yield the requested episode.

## `download`

Resolves the magnet, the `.torrent` link or the `ed2k://` link from a details
page when the listing does not expose them. The section is **optional** and backward-compatible:
definitions that already have `magnet` or `download` in the search row do not
change and `schema` stays 1.

```yaml
download:
  resolve_on: grab            # search | grab (default)
  from: details               # search field, default details
  max_concurrent: 2           # 1..8, default 2
  size: { selector: "table.torrentInfoTable tr:nth-child(1) td", filters: [{ regex: "([0-9][0-9.,]*\\s*(?:B|KB|MB|GB|TB))" }] }
  extract:
    - ed2k:    { selector: 'a[href^="ed2k://"]', attr: href }
      all: true
    - magnet:  { selector: 'a[href^="magnet:"]', attr: href }
    - torrent: { selector: 'a[href$=".torrent"]', attr: href, type: url }
```

| Field | Required | Rules |
|---|---|---|
| `resolve_on` | no | `search` or `grab` (default) |
| `from` | no | name of a field declared in `search.response.fields`; empty = `details` |
| `max_concurrent` | no | from 1 to 8; `0` or absent = 2. Out-of-range values are rejected |
| `size` | no | field read from the details page; the value is interpreted as a size and applied to the release |
| `select` | no | empty or `season_episode`: chooses, among several extractors, the one for the requested season/episode |
| `before` | no | general action before the download (for example a forum's «thanks»); requires `resolve_on: grab` |
| `expand` | no | empty or `file`: with `resolve_on: search` creates a release for every extracted file (a season pack becomes its episodes) |
| `extract` | yes | at least one extractor; each entry has **only** `magnet`, **only** `torrent` or **only** `ed2k` |

Each extractor is a `Field` with the same rules as search fields:
`selector` (list = fallback) or `path` for a JSON response, `attr`,
`type` (`text`, `int`, `size`, `date`, `url`, `category`) and `filters`. For
`torrent` the value is resolved as a URL relative to the details page;
for `magnet` it must be a `magnet:` URI; for `ed2k` it must be a valid
`ed2k://|file|...|/` link, from which name, size and MD4 hash are read.

An eD2k field can use `body: true` instead of `selector`: the engine reads
the whole response and looks for **all** the `ed2k://|file|...|/` URIs in the text,
including those inside a JavaScript array (`filearray[0]='ed2k://…'`) that the
forum uses for the "add all links to eMule" button. Duplicates are
discarded by MD4 hash, so a link present both in an `<a href>` and in the
array counts only once.

An extractor can add `all: true` (additive): it collects **all** the
values that the first selector finds instead of just the first one. It is needed when a
topic contains many magnets or many eD2k files. In that case
`select: season_episode` compares the name of each candidate (the `dn`
parameter of a magnet, the file name of an eD2k link) with the season and episode
of the request:

- season + episode: chooses the release that contains that episode; on a tie
  it prefers the narrowest range (single episode before the pack);
- season only: prefers the season pack, then any release
  of the same season;
- no season/episode: the first value remains valid, as before.

With `expand: file` the selection does not apply: every file in the topic becomes a
separate release. The title joins the topic title and the file name, the
size comes from the link and the MD4 hash becomes the row's identity. When the
file name already says what it contains, that is, it names the episode (`S06E12`, `6x12`)
or the year (`Dune.Parte.due.2024.bdrip…`) and shares a word with the topic, the
title is the file name alone: with the topic in front, Gextto and the Arr apps read
the wrong series or movie. In a movie or series topic (categories 2000 and
5000) accessory files do not become releases: separate audio tracks (`.mka`,
`.ac3`, `.dts`…), subtitles, images, `.nfo` and files with "extra", "bonus",
"sample" or "trailer" in the name, unless the topic itself uses that word
(the series "Extras"). A topic made only of accessory files stays whole. It is the
right path for an archive: a topic with 46 episodes must not lose 45
links. `expand` requires `resolve_on: search`, because the links must be read
before the results are stored.

In a `protocol: ed2k` source the protocol is decided row by row: a topic
that carries a magnet or a `.torrent` instead of an eD2k link becomes a
torrent row, enters the archive as a torrent and on grab goes to a torrent client, not
to aMule. Two `.torrent` files from the same topic receive distinct GUIDs (topic
GUID plus a digest of the URL, which does not appear in plain text). A topic that could not
be opened enters neither the archive nor the live results: its grab
would return the topic's first file, not the chosen one.

Season and episode are kept in the search item and travel
in the encrypted download reference; a definition can also repeat them as
`season` and `ep` parameters in the details link.

### `download.before`

`before` is a **general** action to be executed only on grab, never during a
search. It comes from the MirCrew case (the magnets are hidden until you thank
the first post) but applies to every forum with the same logic. Not even background jobs
execute it: the recovery of missing infohashes skips sources with
`before`, and so do references sealed for another indexer (topics pending
on MirCrew Smartfeed). Until 29/10 that recovery thanked about
ten MirCrew topics on every round, for releases no client had requested.

```yaml
download:
  resolve_on: grab
  from: details
  select: season_episode
  before:
    wait: 2s
    extract:
      thanks: { selector: 'a[href*="thanks="]', attr: href }
    action:
      get: "{{ thanks }}"
  extract:
    - magnet: { selector: 'a.magnetBtn[href^="magnet:"]', attr: href }
      all: true
```

Sequence on grab:

1. opens the details page (`from`);
2. if an extractor in `download.extract` already finds a magnet or a torrent, it
   uses them and **does not execute the action**;
3. otherwise it extracts the values declared in `before.extract` from the page;
4. executes `before.action` (`get` or `post`, with templated `params`/`form`/`headers`);
5. waits for `before.wait` and reopens the page;
6. extracts again and chooses the magnet with `select`.

If after the action no magnet appears the grab fails with an error; it is not
retried in a loop. The "grab only" constraint is validated: `before` with
`resolve_on: search` is rejected, and the engine also has a defensive guard
that skips early resolution. The `{{ item.* }}` values and the extracted ones
are available in the action.


### Semantics

- **`resolve_on: search`** — during the search, **after deduplication and limit**,
  for each row with the `from` field set and without a magnet or download
  link, the engine opens the details page with the same transport, the
  same session and the same authentication as the search. It applies the
  extractors **in the declared order**: the first one that produces a value wins.
  It sets `Magnet` and the normalized `InfoHash`, or `DownloadURL` for a
  `.torrent`. Rows that already have a magnet or a torrent are not opened.
  With 50 rows and `limit: 10`, at most 10 details pages are opened.
- **`resolve_on: grab`** (default when the field is absent) — the search does not
  open details; `Download` opens them at grab time. Fewer requests to the
  site, infohash available only after download.
- **`Download`** prefers a magnet already present. With `resolve_on: grab` (and
  a `download` section) it always resolves from the declared source (`from`,
  typically the details page) and **ignores** a `DownloadURL` carried
  by the item: that link may come from an import (for example Prowlarr) or from
  an old cache and point to a different host, even a private one, which the SSRF
  guard rejects. Without a `download` section, or with `resolve_on: search` (where the
  link has already been resolved from the declared source), it uses a direct `DownloadURL`
  and only afterwards the details page. A redirect of the page (or of the
  link) to `magnet:` is returned as `Payload.Magnet`.
- When the `.torrent` comes from a details page (or the release has a
  details link), the request carries `Referer: <details page>`,
  like a click in the browser: several DataLife Engine sites answer with an
  HTML page if it is missing. The referer is sent only to the same host.
- **`download.before`** is executed only by `Download`, after the client has
  chosen the release; a search does not open the details page and does not thank.
- With `select: season_episode` the choice uses the season and episode of the
  request, not the topic's first magnet.
- Concurrency is limited by `max_concurrent` (default 2, maximum 8). Successful
  resolutions are kept in an in-memory cache per indexer
  (256 entries, 10-minute TTL); rows with the same details URL share
  a single request.
- A details page that fails does not interrupt the page: the row stays without
  magnet/URL and the failure is counted. Errors never contain
  the tracker URL, which may include a passkey.

### Validation

- `resolve_on` must be `search` or `grab`;
- `from` (after defaulting to `details`) must exist in
  `search.response.fields`;
- `select` must be empty or `season_episode`;
- `before` requires `resolve_on: grab`; `action` must have `get` or
  `post`, never both, and each extractor must have a valid `selector`, `json` or
  `regex`;
- `extract` cannot be empty;
- `size`, when present, is a `Field` with `selector` or `path`;
- each entry must have exactly one of `magnet`, `torrent` and `ed2k`;
- each field must have a non-empty `selector` or `path`, an allowed `type` and
  valid filters (`replace: old:new`, `query_param` with a name, compilable
  `regex`, known names);
- `max_concurrent` must be `0` (default) or between 1 and 8.

## `health`

```yaml
health:
  query: test
  min_rows: 1
```

It is the query used by the connector test. The engine runs the first page and
requires at least `min_rows` results (default 1, default query `test`). The test
also reports the phases `reach` (GET on the base URL), `auth` (only if needed) and
`download` (today `skipped`). `Test.OK` requires reach, auth (when expected) and
search to succeed.

## `tests` and offline fixtures

```yaml
tests:
  - name: search
    fixture: fixtures/html-forum/search.html
    expect: { min_rows: 2, required: [title, download, size, category] }
  - name: login-ok
    fixture: fixtures/html-forum/login-ok.html
    expect: { authenticated: true }
  - name: login-failed
    fixture: fixtures/html-forum/login-failed.html
    expect: { authenticated: false }
```

- `fixture` is a path **relative to the definition file**, without `..`,
  without absolute paths and without `~`.
- `expect.min_rows` is the minimum number of extracted rows.
- `expect.required` lists the fields that must be present.
- `expect.authenticated` distinguishes a successful-login fixture from a failed one.

`indexarr-def check-fixtures` only checks that the files exist, are regular
files and are not empty. Extraction on the fixtures is exercised by the engine
tests (`internal/engine`) with `httptest`; the CLI executes no requests and no
extractions.

For the catalog, fixtures are needed for search and, if the indexer is private, for
successful and failed login. No fixture must contain passkeys, cookies,
credentials or real usernames: synthetic values such as
`synthetic-csrf-token` are used.

## `connector` and families

The optional `connector` field delegates interpretation to a native connector.
The names registered by `internal/family` are:

- `unit3d` — trackers based on UNIT3D (JSON API, torrent);
- `newznab` — generic Newznab API (usenet);
- `torznab` — generic Torznab API (torrent);
- `gazelle` — Gazelle software and derivatives (music, JSON API);
- `nexusphp` — trackers based on NexusPHP (HTML, login form);
- `torrenttrader` — TorrentTrader/TBDev and derivatives (HTML);
- `xbtit` — Xbtit/BtiTracker and derivatives (HTML).

The HTML families (`nexusphp`, `torrenttrader`, `xbtit`) have default selectors and
paths marked `bozza`: they must be verified on the real site and, if
different, overwritten with the profile options (no per-site definition
is needed).

With `connector` set, `search` can be omitted. The typical family
settings are URL, API key, categories and transport options; in the YAML the
names must be slugs, so for example `url`, `apikey`,
`categories` are used. The categories and capabilities declared in the definition
complete the connector.

What the format does not express goes into a Go connector, not into ad-hoc extensions.
The source hierarchy is: official API, standard feed, family connector,
native definition, dedicated connector.

## Versioning, upgrader and JSON Schema

The format is versioned with `schema`. The current version is **1**; `Parse`
only accepts `schema: 1`. The `internal/definition` package also exposes:

- `Upgrade(body)` — brings a document to the current version by applying the
  `RegisterUpgrader` chain; a future version or a missing link are
  explicit errors;
- `Fingerprint(body)` — hexadecimal SHA-256 of the raw bytes, used to detect
  changes to saved definitions;
- `JSONSchema()` — the embedded JSON Schema draft 2020-12
  (`internal/definition/schema.json`).

For each future version an upgrader `vN → vN+1` is registered; the engine runs
only the current version. Today there is no version 2.

The JSON Schema is obtained with:

```bash
indexarr-def schema
```

It can be used in editors and CI for autocompletion and structural validation;
semantic validation remains that of `definition.Validate` and of the CLI.

## `indexarr-def` CLI

```bash
indexarr-def validate [flags] <file-or-dir>...
indexarr-def schema
indexarr-def fmt [-w] <file>
indexarr-def fingerprint <file>
indexarr-def check-fixtures <file>
indexarr-def convert-cardigann [-o dir] [-author name] <file-or-dir>...
indexarr-def -version
```

### `validate`

It traverses directories recursively, considers only `*.yaml`/`*.yml`, skips
hidden directories (`.git`, …) and sorts the files. It does not exclude
`testdata/invalid`: if you point it at a directory with deliberately broken
fixtures, it reports them.

```text
ok examples/definitions/html-forum.yaml (id=html-forum, schema=1, 3 settings, 3 categories)
FAIL bad.yaml: definition: meta.id: is required
```

With `-json` it emits an array of `{path, ok, id, error}`. The exit code is 1 if
at least one file fails, 2 for a usage error.

`-catalog` (alias `-reject-derived`) adds the catalog policy: it rejects
derived drafts, that is, non-empty `meta.derived_from` or `meta.author` equal to
`cardigann-import` case-insensitively. `make def-validate` uses
this mode for `internal/catalog/definitions/`, while normal validation
still accepts local drafts produced by the converter.

```bash
indexarr-def validate -catalog internal/catalog/definitions
```

### `fmt`

Parses and re-serializes into canonical YAML (2 spaces). **Comments and the original
key order are not preserved.** Without `-w` it writes to stdout.

```bash
indexarr-def fmt definition.yaml
indexarr-def fmt -w definition.yaml
```

### `fingerprint`

Prints the SHA-256 of the file's raw bytes, without parsing or normalizing.

### `check-fixtures`

Lists each fixture with `ok <path> (<bytes>)` or `FAIL <path>: <error>`; exit 1
if a fixture is missing, empty or not a regular file.

### `convert-cardigann`

Converts **locally** Cardigann definitions (Jackett/Prowlarr) into **drafts** of
the native v1 format. It is a **writing tool, not a migration tool**: it is not a
Cardigann runtime, indexarr never executes these files and does not distribute them.
Every draft produced contains `meta.derived_from: cardigann`; there is no
UI/API route that imports it and the built-in catalog rejects it. That is the starting point
for rewriting the definition by observing the site, with our own fixtures.

The converter covers a subset documented in
`internal/cardigann/MAPPING.md`; what cannot be represented ends up in
`warning`/`unsupported` and the resulting definition stays `bozza`.

```bash
# one definition: native YAML (draft) on stdout
indexarr-def convert-cardigann ~/.config/Jackett/Indexers/example.yml

# one directory: one file per definition, then validation
indexarr-def convert-cardigann -o ./native ~/.config/Jackett/Indexers
indexarr-def validate ./native
```

Produced definitions have no fixtures: to enter the catalog they must be
rewritten and verified on real pages, with offline tests and without
`derived_from`. Credentials are not copied into the YAML: they remain
settings that the user fills in and that the vault encrypts.

## Security and clean writing

- **Definitions are data.** They contain no code, includes, scripts or commands.
  Templates only read the listed variables and the only filter is `join`.
- There are no implicit requests beyond those declared in `auth.steps`,
  `search.request`, `download.extract` (the details page of the
  `from` field) and `download.before` (the declared action, only on grab); the transport
  applies timeouts, size limits, per-host rate limits, circuit breaker and
  SSRF blocking.
- Secrets (passwords, tokens, API keys, cookies) are redacted in errors; download
  URLs may contain a passkey and do not leave the APIs.
- **No passkey, cookie or credential in fixtures.** Fixtures are
  synthetic and are kept together with the definition.
- **Clean-room.** Definitions are written by observing
  the site (pages, responses, API documentation), not by copying files or
  code from Jackett or Prowlarr. Every definition records author, date and the
  fixtures it was written from; the catalog is entirely ours and
  distributable under the license chosen by the project.
- **Derived drafts stay local.** The Cardigann converter is a writing
  aid: it produces `derived_from: cardigann`, is not reachable from the
  UI/API and the catalog (and `make def-validate`) rejects these drafts.
- no automatic CAPTCHA: a blocked indexer requires human intervention.

## How to add a definition

1. Choose the most stable source: official API, feed, family connector;
   use HTML scraping only if necessary.
2. Create `examples/definitions/<id>.yaml` (or the catalog directory) with
   `schema: 1` and a unique slug `meta.id`. If you start from a converter
   draft, rewrite it and remove `derived_from`: the catalog rejects
   derived drafts.
3. Declare `settings`, `categories` and `capabilities` based on what the site
   actually exposes.
4. Describe authentication with explicit steps; extract CSRF/token and verify
   `success`/`failure` with selectors or status.
5. Write `search.request`, pagination and typed fields. Put in
   `required` only the fields without which the release is unusable.
6. If the listing does not expose the magnet, declare `download` with the
   `from` field and the details page extractors; use `resolve_on: grab`
   if you want to postpone opening details until download.
7. Add `health` with a real query and a reasonable `min_rows`.
8. Save synthetic fixtures in `fixtures/<id>/` and declare the tests with
   `min_rows`, `required` and, for private sites, successful/failed login.
9. Run:

   ```bash
   indexarr-def validate examples/definitions
   indexarr-def check-fixtures examples/definitions/<id>.yaml
   indexarr-def fmt -w examples/definitions/<id>.yaml
   make def-validate
   ```

10. If the definition enters the catalog, regenerate the index read by the UI and
    by site search (no need to touch code: the catalog test
    fails if you forget):

    ```bash
    go generate ./internal/catalog
    ```

    The command writes `internal/catalog/index.json` starting from the manifest and
    the definitions; the "Add indexer" page reads only this file and does not
    parse the YAML. `make catalog-index` does the same thing.

11. Verify that the definition also passes the engine tests and that no
    sensitive data ended up in the fixtures.
12. Update `meta.updated`, author and description; for the catalog respect the
    requirements of §7.2 (`health`, tests, login fixtures, state `bozza` →
    `verificata`).

## Complete example

```yaml
schema: 1
meta:
  id: example-tracker
  name: Example Tracker
  language: it-IT
  type: private
  protocol: torrent
  author: indexarr examples
  urls:
    - https://tracker.example/
    - https://tracker-mirror.example/
  updated: "2026-10-23"

settings:
  - { name: username, type: text, required: true }
  - { name: password, type: password, required: true }

categories:
  - { site: "5",  standard: 5040, desc: "TV HD" }
  - { site: "12", standard: 2040, desc: "Film HD" }

capabilities:
  search: [q]
  tv-search: [q, season, ep, imdbid]
  movie-search: [q, imdbid]

auth:
  strategy: form
  steps:
    - get: /login.php
      extract:
        csrf: { selector: 'input[name="csrf_token"]', attr: value }
    - post: /login.php
      form:
        username: "{{ settings.username }}"
        password: "{{ settings.password }}"
        csrf_token: "{{ csrf }}"
  success: { selector: 'a[href="/logout.php"]' }
  failure:
    - { selector: "div.error", message: text }
  refresh_after: 6h

search:
  request:
    get: /torrents.php
    params:
      search: "{{ query.keywords }}"
      cat: "{{ categories | join(',') }}"
      page: "{{ page }}"
  pagination: { type: page, start: 1, max_pages: 5, stop_when: empty_rows }
  response:
    type: html
    rows: { selector: "table.torrents > tbody > tr" }
    fields:
      title:    { selector: ["a.title", "td.name a"], filters: [trim] }
      details:  { selector: "a.title", attr: href, type: url }
      download: { selector: "a.download", attr: href, type: url }
      category: { selector: "a.cat", attr: href, filters: [{ query_param: cat }], type: category }
      size:     { selector: "td.size", type: size }
      seeders:  { selector: "td.seeders", type: int }
      leechers: { selector: "td.leechers", type: int }
      date:     { selector: "td.date", type: date, layouts: ["02-01-2006 15:04"] }
    required: [title, download, size]

download:
  resolve_on: search
  from: details
  max_concurrent: 2
  extract:
    - magnet:  { selector: 'a[href^="magnet:"]', attr: href }
    - torrent: { selector: 'a[href$=".torrent"]', attr: href, type: url }

health:
  query: test
  min_rows: 1

tests:
  - name: search
    fixture: fixtures/example/search.html
    expect: { min_rows: 1, required: [title, download, size, category] }
  - name: login-ok
    fixture: fixtures/example/login-ok.html
    expect: { authenticated: true }
```

Ready and validated examples: `examples/definitions/` (`html-forum.yaml`,
`unit3d.yaml`, `newznab.yaml`).

## Changes 2.0.0-rc1

- `latest.stop_when_known: false` reads all pages of a cheap list.
- `params_multi` sends multiple values for the same parameter; empty values or `name=` are not sent.
- `{{ categories }}` allows a definition to follow the request's standard category.
- Definitions without `{{ query.* }}` are feed-only and do not enter live search.
