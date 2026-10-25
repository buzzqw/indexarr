# Changelog

## 2.0.0-rc1

First release candidate of indexarr 2.

### Sources and import
- All gextto sources: TorrentGalaxy and ExtTo uploaders, Knaben, TorrentLeech RSS.
- Repeatable read-only gextto import, no duplicates; keys stay in the vault.
- Jackett and Prowlarr import.

### Duplicates and cache
- Conservative copy fusion before pagination.
- Full Torznab pages, stable `total`, no gaps between pages.
- One-time cleanup of hashless TGx rows when the hashed copy exists.

### Stored sources
- Measured fourth-source warning, growth counter and explicit retention.
- Latest lists read in full; uploader lists are excluded from live search.

### Live search
- Shared workers per indexer/query, bounded queue, per-search timeout.
- ExtTo follows the request category: Movies for films, TV for series.
- Result cache and timeout memory without cutting releases.
- The title variants Sonarr sends for one episode share a single budget: the
  first one waits the full window, the following ones answer as soon as the
  fast sites are done. Slow searches keep running in the background and their
  rows stay in the cache. Severance goes from ~20 s to ~10 s.
- Home, `/healthz` and metrics counters are cached for 15 s and invalidated by
  writes (releases, app searches, indexer state).

### UI
- Guided “Add source” flow, uploader and experimental-source cards.
- Seven languages for the new parts.

### Known limits
- Native MirCrew and Smartfeed are not verified live (CAPTCHA); they stay
  experimental.
- Sonarr’s Severance search stays around the measured 10 s: the variants are
  four requests and the first one waits the full window.
- The stored-source duration test must be completed in production.
