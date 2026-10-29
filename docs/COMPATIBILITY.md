# Indexer compatibility

indexarr does not contain a parser for every tracker: it speaks **Torznab** for
torrents and **Newznab** for Usenet. Everything that exposes one of these
protocols works without changes. For the rest, indexarr adopts the source with a
native definition or with a family connector; if it has neither, the source stays
queued and **no** proxy to Jackett or Prowlarr is created.

## Matrix

| Source | Status | Notes |
| --- | --- | --- |
| **Jackett / Prowlarr** | not supported | indexarr replaces them: manager endpoints are rejected. Sources are added from the native catalog. |
| **MirCrew** (`mircrew-indexer`) | complete | Base or `/api`; magnet in link/enclosure; no seeders; `/status` for the forum login; slow responses. |
| **Direct Torznab** | complete (torrent) | Any tracker URL containing `/api` or `torznab` is used as is; otherwise `/api` is appended. Jackett/Prowlarr URLs are excluded. |
| **Newznab / Usenet** | complete (generic) | `newznab` connector and `newznab-generic` definition: caps, `search`/`tv`/`movie`/`music`/`book` search, `.nzb` payload stored or resolved at grab time, sending to SABnzbd/NZBGet clients. Native definitions for individual Usenet sites not yet in the catalog. |
| **RSS / Atom** | complete | Title, magnet, `.torrent` enclosure, size, numeric category, date; bulk import and OPML. |
| **ExtTo** | complete (HTML) | Dedicated parser: title and magnet from the same row; category from the `c=` filter. |
| **TorrentGalaxy** | complete (HTML) | Dedicated parser for `post-detail` pages with magnet cache (6 hours). |
| **CorsaroBlu** | listing only (HTML) | Dedicated parser for `/torrent/`, `/torrents/` links and direct magnets, with detail cache. Guest keyword search is disabled by the site; a FlareSolverr session is required because a plain client receives 429/503. |
| **HTTP Basic** | complete | Dedicated per-source fields, used for searches and downloads. |
| **API key** | complete | Sent as `apikey` and as the `X-Api-Key` header; it can be empty. |
| **Cloudflare / challenge** | with FlareSolverr | Automatic retry for RSS and direct Torznab; LAN addresses excluded. |
| **Proprietary APIs** | not handled automatically | The source stays in the "to be written" queue until a native definition exists; it does not appear in the user list. No automatic proxy to Jackett/Prowlarr. |

## MirCrew

`mircrew-indexer` is the reference case of a "hand-rolled" Torznab service: it
deserves an explicit list of what indexarr does for it.

| MirCrew behavior | indexarr handling |
| --- | --- |
| Endpoint `<base>/api`, port chosen by the user | Accepts both the base and the URL with `/api` |
| Magnet in `<link>` and `<enclosure>` | Recognized, no attempt to download it over HTTP |
| No `seeders` attribute | Seeders unknown (`-1`), attribute omitted in the output |
| `<response offset total>` without namespace | Ignored, pagination uses the request parameters |
| Date = forum topic date | Read from `<pubDate>` |
| `/status` with `logged_in`, `login_error`, `last_run` | The Test button queries it and distinguishes failed, in-progress and lost login |
| Slow forum search | Default timeout 45 s, run with margin |
| XML with raw `&` or HTML entities | Sanitization before parsing |

### MirCrew configuration

| Field | Value |
| --- | --- |
| Name | `MirCrew` |
| Type | Torznab / indexer |
| URL | `http://<host>:<port>` or `http://<host>:<port>/api` |
| Manager | MirCrew (or Direct Torznab, Auto is fine) |
| API key | the service's own, if present |
| Categories | `5000` for TV, `2000` for movies |
| HTTP username/password | only if the service requires them |

The path has been verified against a real `mircrew-indexer` (version 0.1):
caps, `/status` with an active forum login, ingestion of 100 releases from the
"latest" list and Torznab search with magnet and infohash.

### Diagnostics

| Symptom | Meaning |
| --- | --- |
| Test: "forum login failed: …" | MirCrew forum credentials must be redone in the service; the local index keeps responding |
| Test: "forum login in progress" | Service just started, not an error |
| Test: "forum login lost" | The service had completed a run and lost the login |
| FlareSolverr test failed | Service unreachable, wrong URL or no Internet access from the solver |
| HTTP 401 | Wrong API key or Basic credentials |
| HTTP 404 on download | Release stored only as a magnet: use the magnet link |
| HTML instead of XML | The indexer returned a login page or a block: check the URL and credentials |
| No results with an empty query | Direct Torznab may not support the "latest" list: set a search term on the source |

## HTML listings (ExtTo, TorrentGalaxy, CorsaroBlu)

`extto.org`/`ext.to`, `torrentgalaxy.*` and CorsaroBlu are not feeds: they
are HTML listings. indexarr recognizes them by host and uses the same approach as
Gextto:

| Site | How |
| --- | --- |
| ExtTo | Pairs the `post-detail` link and the magnet present in the same row; no detail fetch. |
| TorrentGalaxy | Follows the `post-detail` links of the rows, extracts the magnet from the page and caches it for 6 hours. |
| CorsaroBlu | Follows `/torrent/` or `/torrents/` links and direct magnets, extracts magnets in parallel and caches them. It is a latest-list source; guest search is disabled and all requests use FlareSolverr. |

Limits: first page, at most 50 releases per source; if the layout changes or the
site requires login, the test reports the error. **Quick add** recognizes the
host; **Default category** assigns a Torznab category when the page does not
provide one. The other sites without RSS remain in the native queue: the index
classifies them as "to be written" (maintenance view `/indexers/new?queue=1`) and
does not create a Torznab endpoint towards the manager.

## Cloudflare

With an endpoint configured in **Settings → Cloudflare / FlareSolverr**, a
response with a Cloudflare challenge is retried through the service:

- `cf-mitigated: challenge` header, or HTML page with `Just a moment...`,
  `cf-chl`, `__cf_chl`, `Attention Required`, on 403/503 (and on 200);
- RSS, direct Torznab and connection tests;
- no retry for LAN addresses: a local 403 is an authentication/network error,
  not a challenge;
- an XML/JSON error from the indexer is not treated as a challenge.

Limits: `.torrent` files are not downloaded through FlareSolverr (binary
response) and a resolution can take up to ~60 seconds. The indexer URL, including
the API key, is sent to the FlareSolverr service.

## Adding an indexer that is not listed

1. If it exposes **Torznab**, create a source with the full URL and the API key
   (if present). The *Direct Torznab* type avoids any heuristic about the manager.
2. If it requires **HTTP Basic**, fill in the dedicated fields.
3. If it only speaks its own **proprietary API**, the source stays queued: write
   a native definition by observing the site. indexarr does not create a proxy to
   Jackett/Prowlarr, does not contact them and does not implement ad-hoc protocols.
4. If the service exposes a **`/status`** next to `/api`, the test reads it
   automatically when the JSON contains `ok` or `logged_in`.

To verify without waiting for the scheduler, use **Test** or **Run** on the
Sources page and check **Runs** for the exact error returned by the indexer.
**Quick add** recognizes the type from the URL; **Discover feeds** finds the feeds
declared by a page; presets set type and manager without constraining the URL.

## Newznab / Usenet

The Usenet path uses the generic `newznab` connector and the `newznab-generic`
definition. It is not a native definition for a single site: the URL and API key
are provided by the user, so the catalog does not yet contain Usenet sources
verified site by site.

| Step | Behavior |
| --- | --- |
| Adding | API URL (with or without `/api`) and API key; the protocol is `usenet` and is derived from the definition |
| Caps | `t=caps` read and cached for an hour; the declared modes are `search`, `tv-search`, `movie-search`, `music-search`, `book-search` |
| Search | Newznab parameters (`q`, `season`, `ep`, `imdbid`, `tvdbid`, `tmdbid`, `rid`, `artist`, `album`, `author`, `title`, `cat`); entries without a valid link are discarded |
| Payload | `.nzb` downloaded with `t=get` or from the `application/x-nzb` enclosure; if the download budget is exhausted, a sealed reference remains, opened at grab time |
| Output | Torznab/Newznab with `protocol=usenet`, `application/x-nzb` enclosure and `.nzb` file name; no magnet and no infohash |
| Apps | Sonarr, Radarr, Lidarr, Readarr and Whisparr receive `Newznab`/`NewznabSettings` with protocol `usenet`; eD2k remains excluded |
| Download | the release is sent to the Usenet client (SABnzbd or NZBGet) configured for the `usenet` protocol |

Measured limits: live search uses the same fan-out as the other protocols and does
not download the NZB in advance; the payload arrives at the first grab. Native
definitions for individual Usenet sites are not part of the current catalog, so a
source that does not expose Newznab stays queued.

## Download clients

Sending to clients is not part of the Torznab protocol: it is an optional action
of the release detail, configured in **App → Download clients**.

| Client | Integration |
| --- | --- |
| qBittorrent | Web API v2: login, `torrents/add` with magnet or `.torrent` file, category, path, pause. |
| Transmission | RPC `/transmission/rpc`: `torrent-add`, base64 `metainfo` for `.torrent` files, `filename` for magnets, `409` session handled. |
| Deluge | JSON-RPC: login, `core.add_torrent_magnet`/`core.add_torrent_file`, path and pause. |
| rTorrent | XML-RPC: `load.raw_start`/`load.start` with magnet or `.torrent`. |
| SABnzbd / NZBGet | Usenet API: add a `.nzb` or a URL, category, pause. |
| aMule | `amuleapi` Web API (`/api/v1`): password login, `POST /downloads` with the `ed2k://` link. Protocol `ed2k`. |
| Aria2 | JSON-RPC `/jsonrpc`: `aria2.addUri` and `aria2.addTorrent`, token, `dir`, pause. |
| uTorrent | Web API: token from `token.html`, `add-url` and `add-file`. |
| Flood | REST `/api`: authentication, adding magnet/URL/file, tags, destination. |
| Download Station | Synology `auth.cgi` + `task.cgi`: magnet/URL and file upload, destination. |
| NZBVortex | `/api` API: nonce login, add `.nzb`, group. |
| Hadouken | JSON-RPC `/api`: `webui.addTorrent` with file or URL, label. |
| Blackhole | Local folder: writes `.torrent`, `.magnet` or `.nzb`; no URL required. |

Sonarr/Radarr/Lidarr continue to manage the download on their own; this feature
is for sending a release chosen manually from the UI.
