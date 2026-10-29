# MirCrew: Torznab bridge and native definition

MirCrew is an Italian phpBB forum. Every release is a **topic** and the magnets in the
first post are **hidden until you thank** the author. Cloudflare
protects all pages, not just some. That is why it could not be used as an indexer
with Cardigann: it needs a persistent browser session, a
login verified on a protected page and a "thanks" action performed only
at download time.

This document describes the two paths available in indexarr:

1. the **bridge** to the Python service `mircrew-indexer` (legacy, disabled
   in production since 26/10/2026);
2. the **native `mircrew` definition** and the **`mircrew-smartfeed`** source
   (the current path).

> **Update 26/10/2026.** The main path is now the native definition:
> login and search were tested live (12:08 test, result ok), the
> card says «Works well» and the download uses `download.before` for the
> thanks. The **MirCrew (Smartfeed)** source lists new topics and is
> paired with the MirCrew indexer. The old `mircrew-indexer` is no longer needed:
> it has been stopped and disabled from automatic startup; the sections about it
> remain as historical reference.

The rule does not change in either path: **a search never thanks**.
The thanks is a visible action on the forum, performed with the user's
account, and it happens only when a client chooses a release and requests its
download.

---

## 1. Torznab bridge (legacy)

The `mircrew-indexer` service exposes a Torznab API at
`http://127.0.0.1:9118/api`. indexarr adds it as a **generic Torznab
indexer**: no definition or converter is needed.

| Field | Value |
|---|---|
| Base URL | `http://<host>:9118/api` (the complete Torznab endpoint, `/api` included) |
| API key | the one in the service's `config.json`, field `api_key` |
| Protocol | Torrent (also magnet) |
| Categories | 2000 Movies, 5000 TV Series |

Behavior verified on 2026-10-23 against the running service:

- `t=caps` answers `200 application/xml` and declares `search`,
  `tv-search` (`q,season,ep`) and `movie-search` (`q`);
- `t=tvsearch&q=Reacher&season=3&ep=1` answers `200` with 8 releases and magnets;
- `GET /dl/<topic>.torrent?k=…&season=3&ep=1` answers **302** with
  `Location: magnet:?xt=urn:btih:…`; the client (Sonarr/gextto) follows the
  redirect and receives the magnet. The topic used in the test was already indexed and
  already thanked: **no new thanks** was sent by the test.

Also verified **from indexarr**: on a temporary instance (dedicated DB and port,
no production writes) an indexer was created with the definition
`torznab-generic`, URL `http://127.0.0.1:9118/api` and the service's API key.
The indexer test is green (`reach`, `caps`, `search`) and `tvsearch` for
`Reacher S03E01` returned 8 releases with magnets. The URL **must include
`/api`**: the generic Torznab connector does not add it by itself; without `/api`
the service root answers with the web UI and the test fails with "invalid
caps XML".

The bridge was the path used while the native definition was a `bozza`. Now it is
**legacy**: the service is stopped and disabled, and the native definition plus the
Smartfeed source cover search, new items and download with thanks. The
following notes remain for anyone who has to reactivate it or compare the two paths.

### Sonarr compatibility: missing `pubDate`

Sonarr **rejects** the bridge as an additional Torznab indexer:

```text
Indexer feed is not supported: Each item in the RSS feed must have a
pubDate element with a valid publish date.
```

The cause is in the Python service, not in indexarr: topics not yet
thanked appear in the XML as `<item>` with title, `/dl/…` link,
category and season, but **without `<pubDate>`** (`core.torznab_search`,
"pending item" branch). Sonarr validates the RSS feed before saving the indexer and
therefore does not accept it; `enableRss: false` and `forceSave` do not bypass the
check. Gextto and clients that do not require `pubDate` work.

Sonarr's search and grab work if the feed is normalized: in the
2026-10-23 test a small test proxy was placed in between that adds
`<pubDate>` to items that lack it. With that proxy Sonarr accepted
the indexer, `tvsearch` for Reacher S03E01 returned 198 releases (117 with
`magnetUrl`) and the grab stopped only because no download client is configured in the
bench (`Torrent Download client isn't configured yet`), that is,
**after** resolving the magnet. The definitive fix belongs to the Python
service: add a date to pending items. Until then, for Sonarr use
an indexer that is not the direct bridge or accept the limit.

### Why the bridge is safe for search

The service has always separated two moments:

| Action | Opens the topic | Thanks |
|---|---|---|
| smartfeed, catalog, Torznab search | no | no |
| `/dl/<topic>.torrent` or the «Download» button | yes | yes, only if the magnets are still hidden |

The `/dl/` link contains an HMAC token for the individual topic, not the API key. A
topic already thanked is not thanked again.

### Offline verification of the bridge in indexarr

The generic Torznab path is covered by `internal/feed`:

```bash
go test ./internal/feed -run MirCrew -v
```

The tests use an XML response identical to the service's: magnet in
`<link>` and `<enclosure>`, no `seeders` attribute (MirCrew does not publish them,
so they stay unknown and not zero) and the topic date.

---

## 2. Native `mircrew` definition

The definition is in
`internal/catalog/definitions/mircrew.yaml`, with fixtures in
`internal/catalog/definitions/fixtures/mircrew/`. It is in the catalog with state
**`bozza`**: it must not be presented as verified until it is tested with a
real account.

### How it is built

| MirCrew obstacle | v1 extension used |
|---|---|
| Cloudflare on the whole site | `session: { mode: flaresolverr, … }`: every request goes through `sessions.create` + `request.get`/`request.post` |
| phpBB login to be verified on a protected page | `auth.strategy: form` with `creation_time`, `form_token`, `sid`, `redirect`; `success` on `a[href*="mode=logout"]` (ucp.php) |
| Magnets hidden until you thank | `download.before`: opens the topic, follows the «Thanks» link only if the magnets are missing, reopens and reads |
| A topic with many magnets (episodes, packs, resolutions) | `download.extract` with `all: true` + `download.select: season_episode` |
| phpBB HTML search | `search.request` on `/search.php` with `sf=titleonly&sr=topics` and pagination `start=0,25,…` |

The session is closed after 30 minutes without requests, recycled after 12 hours,
and orphan sessions with the `mircrew-idx-` prefix are closed on the first
request. Closing the browser when idle prevents a Chromium from staying
on for hours after a single search; the next request logs in again
by itself. The mode is documented in
[`DEFINITIONS.md`](DEFINITIONS.md#session). The action before the download and the
magnet choice are documented in
[`DEFINITIONS.md`](DEFINITIONS.md#downloadbefore) and
[`DEFINITIONS.md`](DEFINITIONS.md#download).

### Fixtures and privacy

The fixtures were recorded from the site on 2026-10-23 and then **sanitized**:

- `login.html` — public login form (for the "failed login" test);
- `login-ok.html` — extract of the `ucp.php` page when logged in, with the
  logout link;
- `search.html` — two real rows from the `Reacher` search;
- `topic.html` — first post of an **already thanked** topic, with the magnets;
- `topic-locked.html` — derived from the previous one by removing the magnets and adding
  the «Thanks» link: it is used by the `download.before` test.

No fixture contains real usernames, passwords, cookies, `sid`, `form_token`,
avatars or API keys: the values are replaced with `synthetic-*`. The offline
test makes no request to the forum and thanks nobody.

### Offline tests

```bash
go run ./cmd/indexarr-def validate -catalog internal/catalog/definitions/mircrew.yaml
go run ./cmd/indexarr-def check-fixtures internal/catalog/definitions/mircrew.yaml
go test ./internal/catalog -run TestMirCrewFixtures -v
go test ./internal/engine -run 'Session|Before|ReleaseName' -v
```

`TestMirCrewFixtures` deliberately disables the FlareSolverr session: it verifies
only the selectors, the category, the date and the choice of magnet S03E02 among the
three in the topic. The session mechanics (login via `request.post`, cleanup
by prefix, 12-hour recycling) have dedicated tests in `internal/engine` with a
fake FlareSolverr.

### State and limits

- **Cause of the error `download: before action rendered an empty URL` (2026-10-24).**
  Reproduced live with a valid FlareSolverr session but a failed login: the
  grab started without `ensureLogin`, so the topic page was the login
  form, without a «Thanks» link, and extraction stayed empty. The search logged
  in, the grab did not: after a restart or with an expired login every grab from a saved
  release failed like that. Fixed in `internal/engine/definition_before.go`: the
  download logs in, and if a value in `before.extract` is empty it logs in
  again once and re-reads the topic before giving up. Regression covered by
  `TestDownloadBeforeLogsInWithoutSearch` (grab without a preceding search).
  The selectors were compared with `mircrew-indexer` on real pages:
  «Thanks» link = `a[href*="thanks="]` (per-post, `lnk_thanks_post...`), magnet =
  `a[href^="magnet:"]` with `magnetBtn`, title/forum/date as in the fixture.
  The real captures (before/after the «thanks») stay outside the repository and
  are never committed because they contain real users; topics
  thanked in the test: 226759 (Silo S03, new) and 226850 (Silo S03, already
  thanked, reused).
- **State: verified (2026-10-26).** Login, search, normal grab and grab from
  Smartfeed were tested live with the user's account.
  - Normal grab: *Silo - Stagione 3 (2026) [COMPLETA] 1080p H264 …*, 302 to the
    magnet in 2.2 s, with a thanks on the topic.
  - Smartfeed grab: *Inside Man (2006) 1080p H264 …*, a pending topic listed
    by Smartfeed and resolved by the native MirCrew indexer, 302 to the magnet in
    7.2 s, with a thanks.
  Fine-grained selection by season/episode inside a topic with multiple magnets remains
  covered by the offline tests and still to be tried with a single episode.
- The native search is phpBB HTML; the new-items channel is the separate source
  `mircrew-smartfeed` (`meta.latest: true`). The native definition stays
  `latest: false` and answers searches.
- The login depends on the phpBB `prosilver` theme (field names and «Thanks» links).
  A theme change requires updating the selectors.
- The number of live thanks must be kept to a minimum: the real test must use
  **at most 3 deliberately chosen topics**.

### Promotion to «verified» (done on 26/10/2026)

Steps performed:

1. `mircrew` indexer added with username, password and FlareSolverr URL;
2. indexer test: login on `ucp.php`, search, download;
3. normal grab (Silo S03) and Smartfeed grab (Inside Man): both
   produced a magnet `magnet:?xt=urn:btih:…`, with a thanks;
4. topics thanked: 2;
5. `state: verificata` in `internal/catalog/catalog.yaml` for `mircrew` and
   `mircrew-smartfeed`, index regenerated with `make catalog-index` and this
   document updated.

Still to be tested is the automatic choice of the right magnet inside a topic that
contains several episodes and several resolutions (the logic is covered by the offline tests).

---

## 3. Test from the Sonarr bench

The bench is `indexarr-test-sonarr` on `127.0.0.1:18989` (container, without a download
client). Production on `:8788` must not be touched.

With the bridge:

1. add in Sonarr a generic Torznab indexer with URL
   `http://<host>:9118` and the service's API key;
2. search for an Italian series present on MirCrew (for example `Reacher`,
   `Silo`, `FBI Most Wanted`);
3. open the results: already known releases have the magnet; topics not yet
   thanked have a `/dl/…` link;
4. do a test grab and verify that Sonarr receives the magnet. Without a download
   client the grab stops at the returned link.

The test must not generate thanks: use an already indexed topic
(like `Reacher - Stagione 3`, topic 165159) or stop before the grab.
Any test that opens a not-yet-thanked topic must be counted.

---

## 4. Smartfeed (new-items channel, offline)

The personal Smartfeed is MirCrew's **latest** channel, next to the native
search indexer. It is added by pasting into «Add source» the URL copied
from `/smartfeed/ui`: indexarr recognizes it, proposes `t=2` if the URL has
`t=1` (keeping `f`, `d`, `x` and the other parameters) and shows the cleaned
URL, never the token.

- The URL contains `u` and `e`: `IsSensitiveURL` recognizes it and seals it in the
  vault. It does not appear in logs, in the export or in the indexer card.
- If a native MirCrew indexer already exists, the latest channel is paired with
  it. Otherwise it can be created together with the channel, entering user and
  password: the credentials are used **only** for search and grab.
- Each feed entry becomes a **pending topic**: cleaned title
  (`Forum :: title :: Reply by user`, CDATA and HTML), forum, date and link
  to the topic. No magnet and no thanks during polling.
- On grab the sealed reference opens the topic with the native indexer's
  `download.before`. Polling never calls that path.
- **Expired token.** The token changes every time `/smartfeed/ui` is opened.
  indexarr does not try to regenerate it without a live test: it shows the message
  «Smartfeed expired: paste a new URL from …/smartfeed/ui» and the channel
  card always has the link to the page. Pasting the URL again
  replaces the existing channel's one.
- Sanitized RSS and Atom fixtures in
  `internal/catalog/definitions/fixtures/mircrew-smartfeed/`.
- State: **verified (2026-10-26)**. The source read 77 topics from the personal
  feed; the grab of *Inside Man (2006)* was resolved by the native
  MirCrew indexer (302 to the magnet in 7.2 s, with a thanks).
