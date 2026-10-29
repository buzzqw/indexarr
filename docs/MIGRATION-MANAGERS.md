# Replacing Jackett and Prowlarr

Date: 2026-10-25. indexarr **replaces** the two managers: it does not run
alongside them and does not connect to them, either inbound or outbound. There is
no import from Jackett/Prowlarr: sources are added from the native catalog.

## What this means

| Connection | Status |
|---|---|
| indexarr → Jackett/Prowlarr (search, download, proxy) | **absent** |
| Jackett/Prowlarr → indexarr | **absent** |
| Import of the managers' configuration | **removed** |
| Catalog | native, embedded in the binary |

\*arr applications talk **only** to indexarr. Jackett and Prowlarr are shut down;
they do not remain as a fallback.

## How to migrate

1. Open `/indexers/new` and choose the sources from the native catalog. Sources
   with a definition or a known software are configured there; the others remain
   queued as "to be written".
2. Enter the credentials of private sources. Jackett did not expose them and there
   is no file to copy: enter them once in indexarr.
3. Connect Sonarr/Radarr/Lidarr to indexarr (app sync or manually) and remove
   their indexers pointing to Jackett/Prowlarr.
4. Stop and remove Jackett and Prowlarr.

None of these steps reads the managers' database or APIs. If an entered URL
belongs to a manager (for example
`.../api/v2.0/indexers/.../results/torznab/` or `/<id>/api`), indexarr rejects it
with an explicit message.

## Native coverage (snapshot of 25/10)

The catalog contains 568 unique rows from an old snapshot of the managers' public
metadata; it is not a live connection. The definitions and connectors are ours.

| Route | Sources |
|---|---:|
| Native definition | 72 (54 verified, 18 draft) |
| Known software (NexusPHP, UNIT3D, Gazelle, XBTIT, TorrentTrader) | 155 |
| **Native or known software** | **227 out of 613** |
| Queued "to be written" | 386 |

Out of the 638 entries in the Jackett snapshot: 29 native definitions, 155 known
software, 184 with a native route (28.8%); the rest is queue. The difference from
the 403 real rows is due to host duplicates.

## The user's sources

| Jackett/Prowlarr | indexarr definition | Status |
|---|---|---|
| Knaben | `knaben`, `knaben-search` | verified |
| LimeTorrents | `limetorrents` | verified |
| The Pirate Bay | `thepiratebay` | verified |
| TorrentLeech | `torrentleech`, `torrentleech-rss` | draft / experimental |
| TorrentsCSV | `torrentscsv` | verified |
| TorrentGalaxyClone | `torrentgalaxyclone` | verified |
| showRSS | `showrss` | verified |
| ilDraGoNeRo | `ildragonegro` | draft / cookie required |

Six sources are verified and usable right away; two require one-time configuration
(TorrentLeech: account or rsskey; ilDraGoNeRo: account/cookie).

## Why the bridge is gone

A Torznab indexer pointing to the manager is not a source of its own: it leaves
intact the dependency that indexarr must remove. For this reason:

- a source without a definition or connector **is not created**;
- it remains in the catalog under the "to be written" filter with the reason visible;
- when the definition is ready, the source is adopted without touching the \*arr;
- no Jackett/Prowlarr URL is accepted as a source.

## Checks after the replacement

- every \*arr query must return at least the same distinct releases;
- check `app_searches` for `go-http-client` and errors;
- monitor live queue, timeouts and indexarr RAM;
- verify that Sonarr/Radarr keep using only `indexarr` on `:8788`;
- check that no indexers exist with a manager URL.

## Note

`indexarr` does not embed Cardigann. Definitions are not copied: they are written
by observing the sites. The "to be written" queue is the honest price of this rule.
