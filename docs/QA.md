# QA and tests

indexarr has a Go test suite for every package and an end-to-end smoke test
that starts the real binary on a temporary database. The goal is to cover both
the correctness of the internal functions and the HTTP behavior observable from
Sonarr/Radarr and from the browser.

## Commands

```bash
make test   # go test ./...
make vet    # go vet ./...
make qa     # vet + test + build + HTTP smoke test on a temporary instance
make e2e    # headless browser, field by field, on a temporary DB and port
```

`make qa` runs `scripts/qa.sh`: it builds the binary, starts it on
`127.0.0.1` with a random port and a database in a temporary directory,
checks the main pages, the mobile style sheet, the PWA manifest,
the service worker and the Torznab `t=caps` response. At the end it stops the
process and deletes the temporary files.

To use a fixed port:

```bash
INDEXARR_QA_PORT=18080 make qa
```

`make e2e` runs `scripts/ui-e2e.py`: it builds the binary, opens a temporary
database on a random port, starts headless Chrome with the Chrome DevTools
Protocol and a small local service that imitates FlareSolverr, qBittorrent and
Sonarr. It changes every field of the forms (Settings, indexer edit, catalog,
App, download clients, definitions, API keys, first start), saves, reloads and
reads the value back both from the page and from the temporary database. It
covers closed `<details>` too, the «remove value» checkbox, secret fields left
empty, category/URL drop-downs, `data-confirm`, double click and confirm next to
the button. Without Chrome it prints `SKIP` and exits 0; with
`UI_E2E_REQUIRE_CHROME=1` the absence of the browser is an error. `make qa` runs
it when it finds Chrome.

## Verification levels

1. **Static** — `go vet ./...` and compilation of all packages.
2. **Unit/integration** — `go test ./...` on the `backup`, `config`,
   `downloadclient`, `feed`, `flaresolverr`, `ingest`, `logbuffer`, `notify`,
   `store`, `torznabapi`, `web` packages.
3. **HTTP integration** — the `internal/web` tests use `httptest` and
   exercise handlers, CSRF, cookies, redirects and template rendering.
4. **End-to-end smoke** — `scripts/qa.sh` uses the compiled binary, so it
   also verifies the embedded assets and the real server startup.

## What is covered

- **Feeds and sources**: RSS/Atom parsing, Torznab, MirCrew,
  HTML listing (ExtTo, TorrentGalaxy, CorsaroBlu), feed discovery, sanitization,
  redaction of secrets in logs and category mapping.
- **Ingestion**: de-duplication, release updates, backoff, auto-disable,
  scheduler, manual trigger and error handling.
- **Store**: schema and migrations, source CRUD, API keys, searches and filters,
  FTS5 index (triggers, rebuild on existing databases, fallback for short
  terms and query plan), blocklist, saved searches, backup, vacuum,
  integrity, de-duplication, maintenance actions and database report.
- **Torznab**: `caps`, `search`, `tvsearch`, `movie`, `get`, pagination,
  filters, authentication and rate limit.
- **Web UI**: dashboard, releases, detail, sources, quick add, indexer JSON export/import,
  settings, logs, maintenance, CSRF, optional Basic authentication,
  PWA and static assets.
- **Logs**: filter by level and by origin (`indexarr`, indexer, RSS,
  notifications), problem count, `source`/`kind`/`host` context on source
  errors, and filtered download log.
- **Maintenance**: integrity and foreign key check, analyze, WAL checkpoint,
  vacuum, reindex, cleanup by age/source, releases without links, hidden
  releases, run history, API counters and source counter recalculation.

## Isolation

The tests do not use the network and do not touch the production database:

- every test opens a database in `t.TempDir()`;
- remote sources are simulated with `httptest.Server` or fixtures;
- the smoke test uses an `mktemp` directory and terminates the process in the
  `trap`;
- no test requires external services or Docker; only `make e2e` uses a
  headless browser, and it skips if it is not installed.

## CI

CI is local: `make ci` (`scripts/ci-local.sh`) requires a clean tree,
writes the log to `ci-logs/<commit>.log` and ends with `CI-LOCAL <commit> PASS`
or `CI-LOCAL <commit> FAIL <passaggio>`. It runs in order gofmt and `go vet`,
definition validation, `make qa`, `go test`, `go test -race`, the build for all
platforms, the test build for Windows and macOS and the browser test; the
resource budget check (binary size and idle memory, error above 256 MiB) runs in
the background during the tests. Every step prints its duration. All temporary
files (test databases, `go test` binaries, builds for the other platforms, the
budget server, the browser profile) are in a RAM folder under `/dev/shm`
(`CI_TMPFS` to choose another one), deleted at the end even if a step fails;
the Go build cache stays on disk. `.github/workflows/qa.yml` remains
only as a manual start (`workflow_dispatch`) and does not run by itself.

## Manual checklist for the UI

The automated tests verify that the mobile CSS is served, but the visual
rendering must be checked by hand when the templates are changed:

- 390 px width: scrollable navigation, no horizontal overflow;
- tables turned into cards with readable labels;
- selection checkboxes and “Select all” reachable;
- 16 px inputs and buttons with an adequate touch area;
- pages `/`, `/releases`, `/sources`, `/runs`, `/settings`, `/maintenance`,
  `/logs` and release detail;
- light and dark theme;
- PWA installation and offline loading of the static assets.

## Adding a test

- For database logic: `internal/store/*_test.go`, using
  `newTestStore(t)`.
- For a handler or a page: `internal/web/*_test.go`, using
  `newTestEnv(t)`, `csrfFrom`, `postForm` and `get`.
- For a parser or a client: local fixtures and `httptest.Server`.

Every new maintenance utility must have at least one test in the store and one
in the web handler that verifies redirects and the effect on the database.

## End-to-end simulation

Besides the package tests, `sim/` contains a deterministic simulation with
fake services (Torznab/Newznab, Sonarr, SABnzbd, NZBGet, Deluge,
rTorrent and an HTML tracker) and assertions with a non-zero exit code. It runs
locally with `./sim/run.sh` or in Docker with `./sim/run-docker.sh`, on the
isolated ports 18484 and 19080. Details, assertions and reset in
[`sim/README.md`](../sim/README.md).
