# Site catalog

The catalog has two levels, with different purposes.

| Level | What it contains | Where it lives | State |
| --- | --- | --- | --- |
| Native definition | Complete search, category, login and download instructions for a site | `internal/catalog/definitions/*.yaml` | `verificata`, `bozza`, `stabile`, `degradata` |
| Lightweight entry | Name, URL, type, language, categories, family and state of a site | `internal/catalog/sites.yaml` | `verificata`, `da-provvi`, `morta` |

Lightweight entries come from a **historical snapshot of the managers' public
metadata**: name, URL, privacy, language, categories and, when the site page
proves it, the software family. They are not a connection: indexarr does not
query Jackett or Prowlarr at runtime and does not import their configuration.
They are never generated from a Cardigann definition.

## Numbers as of 2026-10-29

Values counted on `index.json` after `go generate ./internal/catalog`.

| Measure | Value |
| --- | ---: |
| Total entries in the catalog (definitions + lightweight entries) | **627** |
| Native definitions | 111 (including 8 generic helpers) |
| Lightweight entries | **516** (511 from the Jackett snapshot, 5 manual) |
| Verified | **85** |
| Drafts | **24** |
| To try (`da-provvi`) | **495** |
| Blocked (`bloccata`) | **14** |
| Dead (`morta`) | **7** |
| Verified public ones (unique hosts) | **92** |
| Italian entries | **14** |

Verified public ones are counted by unique host, so a native definition
with a mirror counts as two hosts. All private lightweight entries stay
`da-provvi`: without an account you cannot verify search.

### eD2k: DDUnlimited

`ddunlimited` is the first `protocol: ed2k` definition: the Italian phpBB forum
publishes `ed2k://|file|...|/` and only a minority of topics also has a magnet
or a `.torrent`. On 27/10 login (FlareSolverr), search, reading a forum page and
grab were tested live; a «Movies · High Definition» page produced 53 releases in
69 s, 49 with eD2k links, and a season topic was expanded into one file per
episode. Season packs hide the links in a JavaScript array (`filearray[...]`):
the `body: true` extractor reads them all and de-duplicates them by MD4;
verified on the «The Last of Us» season 1 topic (10 episodes, S01E01 at
15.928.363.670 bytes). The forum search has flood control («wait a few
minutes»): live search uses a **local index** instead of `search.php`, the
alphabetical list of series (`t=61463`) and the 27 «Lista Titoli Globale» movie
pages (`f=1988`, one per letter). The two pages work differently: the TV list is
**one topic across three pages** (page 1 from `#` to H, page 2 from I to Q,
page 3 from R to Z, one post per letter) and the engine follows the «Prossimo»
link; until 29/10 it read only the first page, 6.521 links, and the series from
I to Z (Silo, New Tricks, Scrubs, Walker, Strike, «Misteri di Murdoch, I») all
went through `search.php`. Since 29/10 the list gives 15.028 titles. The movie
forum instead contains one topic per letter, a single page each (letter `#`: 982
titles), which opens with the letter bar: those links point to the other
letter topics and the engine discards them. The engine reads the TV list and the
topic for the requested letter (6-hour cache), compares the title locally and
opens only the first 5 topics; `search.php` (one turn every 65 s) remains the
fallback. Live on 29/10: «Silo» season 3 → 30 eD2k files from 3 topics (2160p,
1080p, SD) in 17 s without `search.php`; «Solo A Star Wars Story» → 6 eD2k files
in 9 s. The TV list is the list of **reports**, it does not include every
release: for old or unreported series the fallback is the forum search (with its
flood control), while the movie list `f=1988` is complete by letter. The
periodic archive reads four lists (Movies HD, Movies UHD, SerieTV HD ITA,
SerieTV UHD), two pages of 25 topics each, and not the search.

The flood control of `search.php`, measured on 28/10: the forum accepts one
search per account every 57-58 s (rejected at 55 s, accepted at 58 s and at
70 s), a rejection does not restart the count, and a search made only of overly
common or non-existent words does not count. That is why the definition has
`search.interval: 65s` and `search.cache: 12h`: forum searches go through one
at a time, 65 s after the response to the previous one; a search that does not
get a turn in time does not start, it is queued and the history shows it as
«Queued» instead of as an error. The queue starts by itself in the background
(tested on 28/10: 4 queued searches sent at 09:04:33, 09:05:39, 09:06:46 and
09:07:52, all accepted) and the next identical request reads the page from the
cache. Every background job dispatch leaves a `search` row in the indexer
history, with query, state, latency and error: without those rows a forum
rejection stayed invisible and the page just looked like it «never arrived».
The measurements of 28/10 show, however, that the forum also rejects at 65 s
when searches follow one another: in a burst of six, three queued dispatches
were accepted (09:53:33, 09:55:18, 09:56:28) and the next two rejected
(09:58:02, 09:59:20); with only two attempts the page was discarded while the
block was still active. Now the worker retries at most twice after a rejection,
with a pause of four intervals (about 4m20s) before spending another turn,
doubled on each consecutive rejection up to 32 intervals (since 29/10, after an
83-minute block) (`TestDDUnlimitedBackgroundSendRetriesPastABurstBlock`), and
the rejection stays visible in the history. To avoid risking the account,
manual tests stop here: the application queue does the rest at the allowed
pace. The same forum «Informazione» box also says «Nessun argomento…» and «le
seguenti parole sono state ignorate»: only «non ti è permesso» is a rejection.
Free-text searches with an episode («I misteri di Murdoch S13E01») send only
the title to the forum, because topics write «Stagione 13» and never `S13E01`.

The eD2k protocol is excluded from synchronization with Sonarr, Radarr and the
other Servarr programs; the aMule client (`kind: amule`) receives the links from
the Web API.

The catalog now contains **all** the sources of the snapshot, including the
private ones without a recognized family. A source without a native route stays
in the index as **to be written** and produces no proxy; it does not appear in
the user list.

### What the user sees, what the system keeps

The «Add indexer» list shows only the sources that already work: the
`verificata`/`stabile` native definitions and the generic connectors. Drafts,
`da-provvi` entries, `bloccata` and `morta` ones stay in `index.json` and in
`sites.yaml`, because maintenance needs them, but they are not proposed.

The queue is not deleted: it is classified by state. To review all of it there
is the maintenance view `/indexers/new?queue=1`, not linked from the UI, which
also shows the «To write», «Trying», «Problems» and «Blocked» badges.

The header of the maintenance view summarizes the composition of the queue
(`Queue: N to write, N drafts, N blocked, N dead`, translated into the 7
languages) and every `bloccata` or `morta` row carries the measured reason taken
from `metadata/blocked.yaml` (`note` in `index.json`), so the evidence is
visible without opening the manifest. In the user list neither the count nor the
reason appears: the `.site-note` class is rendered only in the queue.

| State | Meaning | Visible to the user |
| --- | --- | --- |
| `verificata`, `stabile` | ready and tested live | yes |
| `bozza` | definition to finish or verify | no |
| `degradata` | definition that works with problems, to improve | no |
| `da-provvi` | source to try or definition to write | no |
| `bloccata` | not manageable for a reason that is not code: out of the queue | no |
| `morta` | address gone: out of the queue | no |

### Native coverage of the Jackett snapshot

| Route | Sources | Notes |
| --- | ---: | --- |
| Native definition (id or host) | **127** | snapshot sources whose host matches a native definition |
| Known software (family connector) | **154** | NexusPHP 76, UNIT3D 59, XBTIT 12, Gazelle 6, TorrentTrader 2 |
| To write | **337** | 281 private, 56 semi-private |
| Out of the queue (blocked or dead) | **20** | 13 blocked, 7 dead |
| **Total Jackett snapshot** | **638** | metadata of 23/10 |

The count is by `source_id` of the snapshot: a source whose host matches a
native definition has no row in `sites.yaml` and counts as native. The 126
covered sources are more than the definitions because one definition can cover
several ids of the same snapshot. The sum does not include the 6 sources already
configured in Jackett when they match a native definition.

### Manager sources missing from the snapshot

On 27/10 the current Jackett list and the Prowlarr schema (only `GET`) were
compared with the catalog by name and host. Five torrent sources were missing,
now lightweight entries in `metadata/manual.yaml` with the `GET` evidence:

| Entry | Type | State | Note |
| --- | --- | --- | --- |
| Anidex (`mg-anidex`) | public | da provvi | native Prowlarr indexer; the site responds with a DDoS-Guard check |
| BinaryBay (`mg-binarybay`) | private | da provvi | API key; family not proven |
| Pornorip (`mg-pornorip`) | private | da provvi | login with cookie |
| RevolutionTT (`mg-revolutiontt`) | private | da provvi | native Prowlarr indexer |
| SAPIbr (`mg-sapibr`) | private | da provvi | UNIT3D from the login page title |

Magnetico and BitMagnet (local DHT, see below), the generic Torznab,
TorrentPotato and the generic Prowlarr RSS feed are deliberately left out,
because in indexarr they are already source types and not sites.

### Public queue: outcome of 27/10

Fifteen public sources in the queue now have a native definition verified live
(real search and, where needed, grab of the magnet or the `.torrent`) with real
fixtures: TorrentDownload, MagnetDownload, BlueRoms, XXXClub, Free JAV Torrent,
PornXLab, BigFANGroup, Catorrent, SkidrowRepack, PC-torrent, Zamunda.LIFE,
MyPornClub, PornoTorrent, MixtapeTorrent and sosulki. `animetosho.net` is a
mirror of the AnimeTosho definition.

The same day the Cloudflare group was taken up again: five new definitions
(BT.etree, ExtraTorrent.st, kickasstorrents.to, Magnet Cat, DaMagNet) and the
`kickass.ws` mirror are described in the
[Cloudflare](#cloudflare-outcome-of-2710) section.

The seven sources that cannot be managed for a reason that is not code have left
the queue and are `bloccata` (52BT is `morta`); the evidence is in
`metadata/blocked.yaml`.

### Second round on the queue: outcome of 27/10

The same day the eleven remaining public sources were gone through one by one,
with a real GET and, where the page responds, the grab. Four now have a native
definition:

| Source | Search | Grab |
| --- | --- | --- |
| E-Hentai | GET `/?f_search=` | `.torrent` from `gallerytorrents.php`: 200 `application/x-bittorrent`, 18212 bytes |
| XXXTor | GET `/b.php?search=` through FlareSolverr | magnet in the row |
| U3C3 | GET `/?search2=<token fisso>&search=` | magnet and `.torrent` in the row |
| torrent.by | GET `/search/?search=`, with `meta.insecure_tls` | magnet in the row (100 magnets out of 100 rows) |

Five left the queue as `bloccata` and two as `morta`, with the measured
evidence in `metadata/blocked.yaml`:

| Source | State | Measured reason |
| --- | --- | --- |
| FileMood | `bloccata` | the Cloudflare challenge does not open on the search page: three attempts through the solver return `One moment...` (3620 bytes) |
| VSTHouse | `bloccata` | it is not a BitTorrent indexer: the pages offer a direct uCoz download, 0 magnets and 0 `.torrent` |
| EpubLibre | `bloccata` | it is not a BitTorrent indexer: the catalog serves EPUB, 0 magnets and 0 `.torrent` |
| torrent-pirat | `bloccata` | phpBB portal without magnets; `/tracker/` stays an empty Cloudflare challenge (718 bytes) |
| Anidex | `bloccata` | DDoS-Guard `403` to the client, origin `Error 502` through the solver |
| OpenSharing | `morta` | timeout on IPv4 and IPv6, the solver responds `500` |
| AniSource | `morta` | no A record, connection refused on 443 and 80 |

**torrent.by** deserves a separate note: the server sends an incomplete TLS
chain (the Let's Encrypt intermediate `YE1` is missing) and both Go and
`openssl` reject the certificate, so the definition uses
`meta.insecure_tls: true`. The exception applies only to that public source, the
shared transport is not touched, and the UI shows the «TLS not verified» badge
with the warning in the form. The search was tested live
(`/search/?search=1080p`, 100 rows with magnet, size, date and seeds) and the
grab returns the row's magnet.


| Blocked | Measured reason |
| --- | --- |
| MejorTorrent | `.torrent` behind Cloudflare; the solver returns only text, search works and the grab does not |
| OneJAV | same as MejorTorrent |
| Torrent[CORE] | empty `/search` (40 bytes), opaque handles, no magnets |
| DonTorrent | download with JavaScript proof-of-work |
| Byrutor | `.torrent` behind captcha; the link does not return the file even through the solver |
| HDRTorrent | the page exposes neither magnet nor `.torrent`, it points to archive.org |
| World-torrent | the domain serves another portal (Darkfox) |
| FileMood | the Cloudflare challenge does not open on search (3 attempts through the solver) |
| VSTHouse | it is not a BitTorrent indexer: direct uCoz download, no magnets and no `.torrent` |
| EpubLibre | it is not a BitTorrent indexer: the catalog serves EPUB |
| torrent-pirat | phpBB portal without magnets; `/tracker/` behind a Cloudflare challenge |
| Anidex | DDoS-Guard `403` and origin `502` through the solver |
| comicat | search behind a JavaScript visitor test (`captcha/addon.php`), 1132 bytes with no rows |
| miobt | same as comicat: same visitor test, same 1132-byte body |
| OpenSharing (`morta`) | IPv4/IPv6 timeout, solver `500` |
| AniSource (`morta`) | no A record, connection refused |
| 52BT (`morta`) | HTTP 410 «The address is dead.» |

### Cloudflare: outcome of 27/10

The previous line said «not verifiable without FlareSolverr». The evidence of
27/10 says the opposite: with the local FlareSolverr active the **eleven**
public sites that had responded `403 "Just a moment..."` on 25/10 all resolve.
Five now have a new native definition (BT.etree, ExtraTorrent.st,
kickasstorrents.to, kickass.ws, Magnet Cat), three were already native and
remain MejorTorrent, OneJAV and Torrent[CORE]. DaMagNet is added to these: it
had the home at 200 but the detail behind a challenge, and now it is native as
well.

| Source | Search | Grab |
| --- | --- | --- |
| BT.etree | FlareSolverr | direct `.torrent`: `download.php` responds 200 `application/x-bittorrent` even without the solver |
| ExtraTorrent.st | FlareSolverr | magnet in the row |
| kickasstorrents.to | FlareSolverr | magnet in the row |
| kickass.ws | FlareSolverr | magnet in the row (mirror of the Kickass definition) |
| Magnet Cat | FlareSolverr | infohash in the `/hash/<40-hex>.html` link |
| DaMagNet | FlareSolverr | magnet on the detail page |

The definitions do not use a persistent session: the solver on a single request
is enough for the search and leaves the magnet grab to the normal transport.
Where the site also protects the file, the grab does not work and the definition
is not written halfway.

Three remain, now `bloccata` and out of the queue, for reasons measured on
27/10:

- **MejorTorrent and OneJAV**: the challenge also covers the `.torrent` (`403
  text/html` both to a normal client and when repeating the solver's
  `cf_clearance` cookies with the same User-Agent). Cloudflare ties the pass to
  the browser's TLS fingerprint, which curl/Go do not reproduce, and
  FlareSolverr does not return binary bodies: the grab is not possible.
- **Torrent[CORE]**: `/search` returns an empty 40-byte body and the pages use
  opaque base64 handles without magnets. There is no HTML search to describe.

### Semi-private: census of 27/10

The 57 semi-private sources left in the queue were gone through one by one with
a real request, reading the historical Jackett definition **only as a writing
aid** to learn the search path (no Cardigann definition is imported or
distributed). The outcome is clear-cut and does not depend on the code:

| Outcome | Sources | Detail |
| --- | ---: | --- |
| An account is needed | **52** | the historical definition declares `username`/`password` (or cookie): without credentials there is nothing to test |
| Login page | **4** | AniDUB, Erai-Raws, RuTracker.org, UniOtaku: `type="password"` on the page, search not available to guests |
| JavaScript visitor test | **2** | comicat and miobt: 1132-byte body that loads `captcha/captcha.js` and posts to `/addon.php`; now `bloccata` |
| TV series without torrents for guests | **1** | LostFilm: `/search/?q=` responds 200 but lists series, the torrents require access |

None of these can be verified without an account, so they stay `da-provvi`: that
is the right state for a source that is reachable but cannot be proven. They
must not be marked `bloccata`, because the reason is not the site but the lack
of credentials.

A second round tried the guest search anyway on all 57, even where the
historical definition declares credentials. Six sources behind Cloudflare
(helltorrents, kinozal, seatracker/Marine Tracker, musebootlegs, MVGroup Forum,
UzTracker) pass the challenge with FlareSolverr — 41-204 KB of real page — but
respond with a login form; the others return the access page directly. No
semi-private source exposes rows with magnets to guests, so the block is not
technical: an account is needed.

If an account arrives (indexer vault), the path is the usual one: search, detail
and grab are observed with the session, the fixtures are saved and the
definition is written. Without one, the only honest thing is to leave them in
the queue.

### Definition health: live test of 27/10

Every definition with a `health` section was really executed (reach + auth +
search) with the local FlareSolverr. Out of **109** definitions:

| Outcome | Definitions | Detail |
| --- | ---: | --- |
| Search succeeded | **69** | counting `rutracker` after the fix |
| No `search` section | **20** | feed/latest or API-only definitions: search is not part of the contract, these are not errors |
| Settings needed | **16** | private or parameterized (`username`, `cookie`, `url`): without them they do not open |
| Slow but working | **2** | `btdirectory` and `mixtapetorrent` resolve the detail during the search and exceed 90 s; the pages have 20 rows and 10 results |
| Not working | **2** | `torrentbyte` and `torrentsome`, now `degradata` |

The two measured regressions:

- **torrentbyte**: the home responds `403` even through the solver, which
  returns a 698-byte `403 Forbidden` page; search no longer produces rows. The
  definition stays in the catalog as `degradata` until someone re-reads the
  site.
- **torrentsome**: the `POST /topic/index` with `_csrf` and `keywords` now
  responds `400` (it worked on 25/10); the site is alive (108 KB on the home),
  the search changed. This one is `degradata` too.

**rutracker**, on the other hand, was fixed: the domain responds `403` to a
normal client but the search is readable from a browser (112 KB, 27 magnets).
The definition now declares a FlareSolverr session
(`name_prefix: indexarr-rutracker`) and the live test passes in 4.1 s.

## Honest states

- **`verificata`** (lightweight entry): a real `GET` was made on the home page,
  the site responded 2xx/3xx with a real page and it is not a login or an
  anti-bot screen. The evidence is in `metadata/verification.yaml`, with date,
  HTTP, bytes and title. It does not mean search was tested: that takes a native
  definition and its fixture.
- **`da-provvi`**: no evidence, or a private site that is reachable but without
  an account, or `403`/`5xx`/login/anti-bot. It is the right badge for a private
  site that has not been tested from the inside.
- **`bloccata`**: the site cannot be implemented for a reason that is not a
  missing line of code (captcha, proof-of-work, binary behind Cloudflare, no
  search at all, different portal). The row stays in the catalog with the
  evidence and the reason in `metadata/blocked.yaml`, but it is out of the
  `da-provvi` queue, `proposable: false` and is not shown to the user.
- **`morta`**: the `GET` failed because the name does not resolve or the
  connection was refused. The row stays in the catalog with the evidence of the
  failure, but `proposable: false` and it must not be proposed. A timeout, an
  unreachable IPv6 route or a TLS error are **inconclusive**: the entry stays
  `da-provvi` and is retried, because they do not prove that the site is dead.
- **`bozza`** (native definition): definition written but not yet tested live.
  For `ilDraGoNeRo` it also means an account is needed.

No private entry is `verificata` in this cycle. No entry was marked alive
without a recorded `GET`.

## Covered families

Families are recorded with evidence in the site page, in the manager's own name
or in a strong marker observed live (for example `nexusphp`, `unit3d`, `xbtit`,
`gazelle`, `torrent trader`). Generic markers shared by several software
packages (`getrss.php`, `ajax.php?action`, `download.php?id=`) are no longer
used: a wrong family is worse than a source in the queue. If a page shows two
families, the entry stays without a family until it is observed better.

| Family | Entries | Examples and evidence |
| --- | ---: | --- |
| UNIT3D | 59 | HDTorrents.it, ShareIsland, ItaTorrents, JME-REUNIT3D, HD-UNiT3D and others detected live; `unit3d-generic` |
| Gazelle | 6 | GazelleGames and others detected live; `gazelle-generic` |
| NexusPHP | 76 | SewerPT, HDU, HHanClub, OpenCD, HDSky, CHDBits, PterClub, …; `nexusphp-generic` |
| TorrentTrader | 2 | The Torrent Trader (`takelogin.php`, logo); `torrenttrader-generic` |
| XBTIT | 12 | il CorSaRo Blu, ArabaFenice, Girotorrent, Fenyarnyek and others; `xbtit-generic` |
| Torznab | 4 | MirCrew (local bridge), Torlock, TorrentFunk; `torznab-generic` |
| custom | 2 | ExtTo (public HTML), ilDraGoNeRo (phpBB forum) |

## Italian list

`italian: true` is an explicit signal for the «Italian» quick filter,
independent of the language declared by the site. It includes the priority list
of Italian sites.

| Site | Family | State | Notes |
| --- | --- | --- | --- |
| CorsaroBlu (`corsaro`) | XBTIT | verificata | Native definition; guest list and magnets verified, guest search disabled by the site, requests through FlareSolverr since 29/10/2026 |
| ItaTorrents (`jk-itatorrents`) | UNIT3D (probable) | da provvi | Private; login 200, `/api/torrents` 401; no account |
| ArabaFenice (`jk-arabafenice`) | XBTIT | da provvi | Private; xbtitFM login 200; no account |
| FutureTorrent (`jk-futuretorrent`) | — | da provvi | Private; family not proven by the HTML (TSSE), no account |
| HDTorrents.it (`jk-hdtorrentsit`) | UNIT3D | da provvi | Private; UNIT3D login 200; no account |
| ShareIsland (`jk-shareisland-api`) | UNIT3D | da provvi | Private; UNIT3D login 200; no account |
| Girotorrent (`jk-girotorrent`) | XBTIT | da provvi | Private; xbtitFM 4.2.14 footer; no account |
| TrackerZero (`jk-trackerzero`) | — | da provvi | Private; SvelteKit frontend, family not proven |
| ilDraGoNeRo (`ildragonegro`) | custom (phpBB) | bozza | Private; native definition being tested, an account is needed |
| MirCrew (`mircrew`) | Torznab | da provvi | Local bridge from block B (`localhost:9118`), not a public tracker |
| ExtTo (`extto`) | custom (HTML) | verificata | Public native definition, fixtures and parity verified |

**UNIT3D:** HDTorrents.it, ShareIsland and, with `probable` confidence,
ItaTorrents. MirCrew is not UNIT3D: it is a local Torznab service.

## How it is regenerated

Everything is GET, except writing the generated files.

```sh
# 1. Live test of the whole catalog, private ones included. Only GET on the home
#    page, no login, no writes on the sites (-all). It does not contact
#    Jackett or Prowlarr: it only reads the already committed metadata.
go run ./internal/catalog/genverify -all -workers 16 -timeout 10s

# 2. Regenerate sites.yaml and index.json (does not touch the network)
go generate ./internal/catalog

# 3. Checks
make def-validate
go test ./...
```

`verification.yaml` is written by `genverify`; `gensites` reads the snapshot,
the families, the evidence and the manual entries and produces `sites.yaml`;
`genindex` merges native definitions and lightweight entries into `index.json`.
The snapshot `metadata/jackett.json` is frozen: there is no longer a command
that contacts the managers.

### Files

| File | Source | Editable by hand |
| --- | --- | --- |
| `metadata/jackett.json` | historical snapshot | no |
| `metadata/prowlarr.json` | historical snapshot | no |
| `metadata/families.yaml` | curation with GET evidence | yes, by adding the evidence |
| `metadata/verification.yaml` | `genverify` (GET) | no |
| `metadata/blocked.yaml` | curation with measured evidence | yes, by adding the evidence |
| `metadata/manual.yaml` | priority list | yes |
| `sites.yaml` | `gensites` | no |
| `index.json` | `genindex` | no |

## Metadata for the UI

`index.json` exposes for each row:

- `italian` — «Italian» quick filter;
- `usage` — number of configurations observed in the managers (Jackett
  `configured=true`, Prowlarr `enable=true`, plus the production snapshot);
  it is the signal for the «most used» ordering;
- `family` — software family, empty if not proven;
- `url` — primary address, for display and for the «Try» button;
- `source` — `native`, `jackett` or `prowlarr`;
- `state`, `evidence`, `proposable` — honest state and evidence.

Lightweight entries always have at least one required field (`url` or the
credentials), so the old quick-add path does not add them by mistake: the UI
integration must use `catalog.DefinitionForSite` to build the definition of the
chosen family.

## Limits and blocks

- **Cloudflare 403**: 11 public sources in the snapshot responded 403
  «Just a moment...» to the 25/10 probe. Eight now have a native definition
  (three already did, five were added on 27/10 with FlareSolverr);
  MejorTorrent, OneJAV and Torrent[CORE] are `bloccata` and out of the queue,
  with the reason in the Cloudflare section above. DaMagNet, which had the home
  at 200 but the detail behind a challenge, is now native. The probe does not
  use FlareSolverr: that is why it records 403 even when search works with the
  solver.
- **Timeouts**: `NewStudio` did not respond within 45 s; it is `morta` with the
  evidence. `xxxtor` takes ~35 s and is verified with a 45 s timeout.
- **I2P**: Postman uses an `.i2p` address that does not resolve here; it is
  `morta`.
- **Local DHT**: `Magnetico` and `BitMagnet` are self-hosted services, not
  public sites; they are excluded from the catalog.
- **Private**: without an account search cannot be verified. The 45 private
  entries stay `da-provvi`.
- **Native definitions**: a native parser was not added for every public site;
  lightweight entries with a known family can be added through the family
  connector. A public site without a proven family stays `da-provvi` until a
  definition is written or the family is proven.
- **Usage**: almost all new sites have `usage: 0`; the managers' signal is real
  but sparse (Prowlarr has 3 enabled indexers, Jackett none configured in this
  snapshot). The «most used» ordering works but only distinguishes the native
  entries already configured in production.
