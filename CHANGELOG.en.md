# Changelog

## 2.1.0 — 26 October 2026

### Indexer management
- Source page: the Overview tab always shows the address, the
  “works / does not work” verdict, the reason, the last error and the log of
  the latest operations, including process-log lines.
- Required credentials (username, password, API key) sit at the top of the
  form in a “Site access” box; the page says which ones are missing and links
  straight to the form. Catalogue sites that need a login show an “Enter
  credentials” button.
- Deleting or disabling a source returns to the filter you came from, for
  example “Disabled”.
- In “Add indexer”, type and language are dropdowns; needs (Films, Books, …)
  add up and stay highlighted.
- The catalogue search sits at the top of the guided path, not at the bottom.
- The **Problems** filter has **Test problems**: one button tests every flagged
  source (problem, degraded, blocked) and refreshes their state.
- A failing source is retried after **1, 2, 3, 6, 12, 24, 48 and 96 hours**;
  after the last attempt the scheduler stops and waits for the operator. A
  successful test or an edit resets the schedule.
- Every button answers at once: while the form is being sent it shows
  “Working…”, and “Change” on a password no longer clears what you already
  typed; it shows a “Save” button next to the field and explains that the new
  value must be entered and saved.
- The catalogue only lists sources that already work: “to write” entries are
  no longer shown.
- Search results have a “Copy magnet” button next to “Download”.
- A MirCrew Smartfeed URL pasted into the RSS form is recognised and routed to
  the “MirCrew (Smartfeed)” flow, which lists topics and uses the MirCrew
  indexer for the download and the thank-you; an existing RSS source created
  with that URL shows a notice and a “Switch to Smartfeed” button.
- A source whose URL lives in the vault (rsskey, API key) shows the stored
  address with its secrets masked, no longer the `example.invalid`
  placeholder.
- A fixed search (Query field) no longer sends the source's whole category
  list as one filter: ExtTo with `q=MIRCrew` returns results again instead of
  zero.
- Each source has its own refresh frequency, visible in the form (outside
  “Advanced”) and in the list, with minute/hour quick choices and the value
  actually in force (“global default” or “set for this source”).
- “Reduce the frequency”, on the 429 problem, doubles the interval of that one
  source and confirms the new value. Live app searches are not slowed down.

### Speed
- HTML, CSS, JS and SVG responses are gzipped and static files carry an ETag:
  a repeat visit gets a 304 instead of the whole file.

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
- On the App page the three ways to connect are real cards with correct
  margins and spacing, no longer text stuck to the panel edge.
- Documentation: a new [usage guide](docs/USAGE.md) and
  [JSON admin API](docs/ADMIN-API.md); the [Torznab API](docs/API.md) now lists
  the per-indexer paths and how to configure each client.
- Seven languages for the new parts.

### Maintenance
- `scripts/upgrade.sh` keeps the newest **3** pre-upgrade copies (and their
  matching binaries) and prunes the older ones; change the count with
  `INDEXARR_BACKUP_KEEP`.

### Known limits
- Native MirCrew and Smartfeed are verified live for login and search
  (Smartfeed: 77 topics). The grab with the thank-you still needs a real
  download, so the definition stays experimental.
- Sonarr’s Severance search stays around the measured 10 s: the variants are
  four requests and the first one waits the full window.
- The stored-source duration test must be completed in production.
