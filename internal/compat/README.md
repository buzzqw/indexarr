# Compatibility suite (`internal/compat`)

This suite tests indexarr 2's external contract toward the \*arr, not the
internal functions. It uses only exported APIs, local fixtures and `httptest`:
it makes no real network calls, does not modify the production packages and
does not add dependencies.

## How to run it

```bash
gofmt -l internal/compat
go vet ./internal/compat
go test ./internal/compat
go test -race ./internal/compat
```

The full suite takes less than a second (with `-race` about 8 seconds).

To regenerate the goldens after an **intentional** protocol change:

```bash
UPDATE_GOLDEN=1 go test ./internal/compat -run 'TestCapsGolden|TestSearchGolden'
```

Goldens must not be updated just to make a test pass: a diff is a signal of
incompatibility.

## Fixtures

| File | Use |
| --- | --- |
| `testdata/canonical.json` | Four canonical releases: magnet, torrent, Usenet nzb and multi-indexer release. It also contains a download reference with a passkey, which must never appear in the XML. |
| `testdata/caps.golden.xml` | Exact snapshot of `t=caps` (categories, limits, declared parameters, stable order). |
| `testdata/search.golden.xml` | Exact snapshot of an aggregated search: XML escaping, order, Torznab attributes, provenance and proxy links. |

Dates are fixed and the time zone is forced to UTC in `TestMain`, so the
goldens are reproducible on any machine.

## Coverage matrix

| Area | Covered | Notes |
| --- | --- | --- |
| Torznab/Newznab XML | `rss` 2.0 root, `torznab` and `newznab` namespaces, `<newznab:response offset total>`, `<item>`, `<title>`, `<guid isPermaLink="false">`, `<link>`, `<comments>`, `<pubDate>`, `<size>`, `<category>`, `<enclosure>` | Well-formed XML verified with `encoding/xml` |
| Modes | `search`, `tvsearch`, `movie`, `music`, `book` | `music` and `book` are aliases of `search`, as documented |
| Parameters | `q`, `season`, `ep`, `imdbid` (with and without `tt`), `tmdbid`, `tvdbid`, `limit`, `offset`, `cat`, `extended`, unknown parameters | `rid`, `artist`, `album`, `author`, `title` are tolerated and ignored where not supported |
| Limits | `limit` default 50, maximum 100, negative/zero values, negative/out-of-page `offset` | The cap of 100 is proven with 109 releases |
| Categories | Roots 1000–8000 and subcategories, filter by root and subcategory, out-of-range values | A non-numeric `cat` is ignored (current behavior) |
| Torznab attributes | `category`, `size`, `seeders`, `leechers`, `peers`, `infohash`, `magneturl`, `publishdate`, `downloadvolumefactor`, `uploadvolumefactor`, `protocol=usenet`, `indexers`, `provenance` | For Usenet, `infohash`/`magneturl` are absent |
| Errors | Codes 100, 101, 200, 201, 300, 400, 500 | Code 900 has no reachable paths in this handler |
| Download | `/dl/{id}.torrent`, `/dl/{id}.nzb`, `t=get`, `HEAD`, mandatory API key, additional key | Links always proxied: no tracker URL, passkey or `download_ref` |
| Aliases | `/api`, `/torznab/api`, `/newznab/api`, `/api/v2.0/indexers/all/results/torznab/api`, `/torznab/{slug}/api`, `/api/v2.0/indexers/{slug}/results/torznab/api` | Responses byte-identical for the same selection |
| Per-indexer selection | Two legacy sources, case-sensitive slug, unknown indexer → Torznab 201 | The aggregated filter by `tag`, `status`, `indexer_type` is covered |
| \*arr payload | Sonarr, Radarr, Lidarr, Readarr, Whisparr × Torznab torrent/Newznab usenet | `implementation`, `configContract`, `protocol`, `fields.baseUrl/apiKey/categories`, `priority`, `tags`, `enableRss`, `enableAutomaticSearch`, `enableInteractiveSearch` |
| Fake Servarr | Integration with `internal/simmock` | Verifies that the payload is accepted and stored |
| Concurrency and headers | 20 concurrent searches with the same key, limited responses, `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`, content type | Also run with `-race` |

## Out of scope

- Scraping, login and family connectors: covered by the tests of the respective
  packages.
- Live search (`torznab_live_search`), distributed rate limit and provenance
  refresh: they require a scheduler or external services.
- Full native Newznab: this suite verifies the Newznab shape compatible with
  the \*arr and the `.nzb` download, not a separate Usenet protocol.
- Download clients (qBittorrent, Transmission, SABnzbd, NZBGet, Deluge,
  rTorrent): covered by `internal/downloadclient`; here only the payload served
  by indexarr is verified.
- Compatibility with real versions of the \*arr: the suite uses the schema and
  requests of the v3/v1 APIs, not real Sonarr/Radarr/Lidarr binaries.

## Known issues

| Test | Status | Detail |
| --- | --- | --- |
| `TestUnknownSeedersAreOmitted` | `SKIP` | **BUG**: a release with `Seeders=-1` and no `release_sources` row is rendered with `seeders=0`, `leechers=0`, `peers=0`. `writeItem` compares the provenance to the zero value (0) with the “unknown” sentinel (-1) and overwrites it. This contradicts `https://github.com/buzzqw/indexarr/wiki/Torznab-API`, which requires the attributes to be omitted. Evidence in the golden: `<torznab:attr name="seeders" value="0"/>` on the item `Compat.NZB.Album.2026.FLAC`. |
| `TestErrorCode900` | `SKIP` | Not a bug: code 900 is defined but no request path produces it. The reachable codes are 100, 101, 200, 201, 300, 400 and 500. |
