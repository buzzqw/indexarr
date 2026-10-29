# End-to-end simulation of indexarr 2

This directory contains a deterministic, self-contained simulation of
indexarr 2. It does not contact real trackers, Prowlarr, Sonarr or download
clients: everything is served by `simmock`, a single Go process that
implements the fake HTTP surfaces.

The goal is to verify the full path:

1. native definition and indexer created via `/api/v2`;
2. indexer run against a fake Torznab indexer;
3. releases in the database and indexarr's Torznab search;
4. download of the `.torrent` and `.nzb` payloads;
5. Sonarr application linked via `/api/v2`, sync and indexer created in Sonarr;
6. sending payloads to the SABnzbd, NZBGet, Deluge and rTorrent clients.

## Ports and keys

| Service | Host port | URL |
|---|---|---|
| indexarr | 18484 | `http://127.0.0.1:18484` |
| simmock | 19080 | `http://127.0.0.1:19080` |

Fixed simulation keys:

| Surface | Key |
|---|---|
| Torznab/Newznab (`apikey`) | `demo-key` |
| Sonarr (`X-Api-Key`) | `demo-sonarr` |
| Prowlarr (`X-Api-Key`) | `demo-prowlarr` |
| Deluge (`auth.login`) | `demo` / `demo` |

Ports 8788 and 9117 are never used. In Docker indexarr listens on `:8788`
inside the container, published on `18484`; simmock listens on `:19080`.

## Local run

```bash
./sim/run.sh
```

The script:

- builds `cmd/simmock`, `cmd/indexarr` and `sim/seed` into `sim/.bin/`
  (ignored by git);
- creates a temporary directory with the database, the vault key,
  configuration and logs;
- runs `sim/seed` to set `public_url`, enable torrent fetching and sync the
  built-in catalog;
- starts simmock on `127.0.0.1:19080` and indexarr on `127.0.0.1:18484`;
- reads indexarr's API key from the log (`apikey=...`), with
  `sim/seed -print-key` as fallback;
- runs all assertions and exits with code 1 if at least one fails.

To keep the database and logs of a run:

```bash
KEEP=1 ./sim/run.sh
```

## Running with Docker

```bash
./sim/run-docker.sh
```

The command uses `sim/docker-compose.yml`:

- `simmock`, built from `../sim/Dockerfile.mock` with the repository root as
  context (multi-stage `golang:1.26-alpine`), published on `19080`;
- `seed`, a one-shot with the same image, which prepares the `sim-data` volume
  before indexarr;
- `indexarr`, built from the repository's existing `Dockerfile`, published on
  `18484`, with a fixed `INDEXARR_MASTER_KEY` and a healthcheck.

The containers are named `indexarr-sim`, `indexarr-sim-mock` and
`indexarr-sim-seed`; the image is `indexarr-sim:local`, so the
`indexarr:latest` image and any existing containers are left untouched.

`run-docker.sh` runs `down -v` for the `indexarr-sim` project only at the
beginning and at the end (trap), so it leaves no orphan volumes. To stop it
manually:

```bash
docker compose -f sim/docker-compose.yml down -v --remove-orphans
```

Inside the Docker network indexarr reaches simmock at
`http://simmock:19080`. Public settings instead use
`http://127.0.0.1:18484`, so the `.torrent` links returned by indexarr can be
downloaded from the host. The fake Sonarr registers the indexer but does not
call indexarr.

## What is fake and what is real

Real:

- the indexarr binary and all packages used in production;
- SQLite, migrations, catalog, vault, `/api/v2`, native scheduling;
- the family Torznab client, search, download and payload proxy;
- the `appsync` Sonarr client and the `syncjob` reconciliation;
- the download clients of `internal/downloadclient`.

Fake:

- the Torznab/Newznab indexer that produces three synthetic releases;
- the HTML tracker with login form, CSRF, cookies and results table;
- Prowlarr and Sonarr, which keep state in memory;
- SABnzbd, NZBGet, Deluge and rTorrent, which record adds in memory;
- the payloads: a minimal bencoded `.torrent` and an XML `.nzb`.

`GET /debug/state` on simmock exposes the observable state: Torznab counters,
tracker downloads, indexers present in Sonarr and adds received by each
client. All responses are deterministic; no request leaves the machine.

## Deterministic fixtures

| # | Title | Type | GUID |
|---|---|---|---|
| 1 | `The.Simpsons.S01E01.1080p.WEB-DL` | torrent with magnet + infohash | `demo-magnet-1` |
| 2 | `Sim.Demo.Movie.2026.1080p.BluRay.x264` | `.torrent` enclosure | `demo-torrent-2` |
| 3 | `Sim.Demo.Album.2026.FLAC` | Usenet `.nzb` | `demo-nzb-3` |

The releases have categories 5040, 2040 and 3040, fixed sizes, seeders and
dates. `t=get` accepts both numeric ids and GUIDs.

The definition is in `sim/definitions/torznab-demo.yaml` (`connector: torznab`,
categories 2040/3040/5040). The indexer created by the script receives both
`apikey` (definition setting, encrypted in the vault) and `api_key` (the name
read by the family connector) and `allow_private=true`, required because
simmock listens on loopback/private network.

## Assertions performed

The script shares the assertions in `sim/assertions.sh` and verifies:

**simmock**

- `/healthz` returns `{"status":"ok"}`;
- `t=caps` is valid XML with categories and modes (`tv-search` included);
- `t=search` on Torznab and Newznab returns 3 items with `torznab`/`newznab`
  attributes, infohash, magnet and `.nzb` enclosure;
- `t=get` returns a bencoded `.torrent` with content type
  `application/x-bittorrent` and an `.nzb` with content type `application/x-nzb`;
- without `apikey` the Torznab search responds 401;
- the tracker shows the form with CSRF, login sets the cookie, the search
  contains 2 rows with `size`, `seeders`, `date`, `category` and 2 download
  links; the download works only with the cookie (without cookie: 401);
- Prowlarr returns indexers (Torznab/Newznab) and applications (Sonarr) with
  `X-Api-Key: demo-prowlarr`;
- Sonarr requires `X-Api-Key: demo-sonarr` and its state contains
  `appName: Sonarr`.

**indexarr**

- `POST /api/v2/definitions` creates `sim-torznab` (201);
- `POST /api/v2/indexers` creates the native indexer (201);
- `POST /api/v2/indexers/{id}/run` reports `seen=3` and `new=3`;
- `/api?t=search&apikey=...` returns `total="3"` and 3 items;
- the `.torrent` link returned by indexarr downloads with content type
  `application/x-bittorrent`;
- `POST /api/v2/applications` links Sonarr and the sync reports `added=1`;
- the indexer `indexarr · Sim Torznab` is present in Sonarr with a base URL
  containing `sim-torznab/api`.

**download clients and state**

- SABnzbd `mode=addurl` responds `status:true`;
- NZBGet `append` responds `result:1`;
- Deluge `auth.login` and `core.add_torrent_magnet` work with a cookie;
- rTorrent `system.listMethods` and `load.start` respond over XML-RPC;
- `/debug/state` contains the adds from all four clients, the tracker download
  and the indexer synced in Sonarr.

## Go tests and checks

```bash
gofmt -l cmd/simmock internal/simmock sim/seed
go vet ./cmd/simmock ./internal/simmock ./sim/seed
go test ./internal/simmock
go run ./cmd/indexarr-def validate sim/definitions/torznab-demo.yaml
bash -n sim/run.sh sim/run-docker.sh sim/assertions.sh
```

The `internal/simmock` tests use `httptest` and cover caps/search/get,
tracker, Prowlarr, Sonarr (add/update/delete/list) and every download client,
including `/debug/state`.
