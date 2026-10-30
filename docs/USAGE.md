# indexarr user guide

This guide takes you from installation to everyday use. For the technical
details of the APIs see [API.md](API.md) (Torznab) and
[ADMIN-API.md](ADMIN-API.md) (JSON). For installation, systemd, Docker and
basic configuration see the [README](../README.md).

---

## 1. First start

1. Open `http://<host>:8788`.
2. The guided path asks for the address that the apps must use to reach
   indexarr. If the apps are on another machine, enter the IP of the indexarr
   machine (for example `http://192.168.1.10:8788`), not `localhost`.
3. The vault key is created on first start. **Keep it safe**: without it the
   saved passwords and API keys cannot be reopened. The path is shown in
   Settings → Vault.
4. Complete or skip the guided path: you can reopen it from Settings.

The dashboard shows sources, releases, problems and the app status. The
**Statistics** entry collects the incoming requests with outcome and timings,
the overview per app and per indexer, the downloads, the runs, the new releases
and the network counters. **Logs** holds only the system log of the process.

The **version** at the bottom of the sidebar opens the GitHub repository in a
new tab; on a narrow screen the same link sits at the top right. It is the
quick way to read the changelog or open an issue.

---

## 2. Adding a source

### Guided path

1. **Indexers → Add**.
2. The catalog search is at the top of the page. Below are the need cards:
   **Series, Films, Anime, Music, Books, Italian, Other languages**.
3. The cards are **cumulative**: you can enable Films and Books together and
   see the union of the results. An active card has a filled background and a
   check mark.
4. **Show all sites** opens the complete list of sources that are already
   usable, with two drop-down lists for **type** (public / semi-private /
   private) and **language**, plus the filters for category and protocol.
5. The catalog shows only the sources that work: the "to be written" entries,
   the drafts, the blocked ones and the dead ones stay in the index for
   maintenance but do not appear. The maintenance view
   `/indexers/new?queue=1` lists them all, with the filters for status and
   path.

### Source types

| Type | When to use it | What it asks for |
| --- | --- | --- |
| **Native** | Site with a bundled definition (most of the catalog) | URL and, if private, credentials |
| **Family** | Site running known software (NexusPHP, Unit3D, Gazelle, …) | URL and credentials/API key |
| **RSS / Atom** | Feed with direct links to `.torrent`/`.nzb` | Feed URL |
| **Torznab / Newznab** | External Torznab service (not Jackett/Prowlarr) | Full URL and API key |
| **MirCrew (Smartfeed)** | Personal URL `…/smartfeed/feed?u=…&e=…` | Smartfeed URL and, for the grab, the MirCrew indexer |

### Credentials

In the edit form, the mandatory login fields (user, password, API key, cookie,
token) are at the top, in the **Site access** box. They are not hidden in
Advanced.

- Fields marked with `*` are mandatory.
- Passwords and keys are encrypted in the vault.
- **Change** does not save: it prepares the field and shows a **Save** button
  next to it. You can also type directly and press Save.
- The source card shows **Configured / Missing** and, for secrets,
  **last update …**: it is used to verify that the save went through.

If a private source does not ask for user and password (for example an RSS feed
with rsskey), the form says so: the access is in the personal address.

### MirCrew and Smartfeed

- **MirCrew** (native definition) searches, opens the topic on download and
  thanks **only** at that moment. A search never thanks.
- **MirCrew (Smartfeed)** lists the new topics from your personal URL and pairs
  them with the MirCrew indexer for the grab. The Smartfeed URL expires: if the
  card warns "Smartfeed expired", open `/smartfeed/ui` on the site and paste
  the new URL.
- If you paste a Smartfeed URL into the generic RSS form, indexarr recognizes
  it and suggests the correct path; an RSS source already created with that URL
  shows **Switch to Smartfeed**.

---

## 3. The card of a source

### Overview

- **Address**: always visible under the name, with credentials and query
  redacted.
- **Source health**: verdict in one sentence ("Works well", "Not working: …",
  "Needs setup"), last error with date and time, log of the latest operations
  and lines of the process log. A source stopped by the circuit breaker after
  repeated errors is marked "Paused": indexarr does not contact it until the
  wait is over and then retries by itself.
- **Site access**: which credentials are configured or missing and when they
  were saved.
- **Status**: last update, next, frequency, query, latency, grab.
- **Filters**, **Latest releases**, link to the apps.

### Data

Chart of the runs, table of the latest runs with rejection reasons,
completeness of the fields (size, seeders, category, date, infohash) and last
raw/normalized sample. It helps to understand why a release does not arrive.

### Actions

- **Test**: runs reachability, capability, login and search. If it succeeds, it
  records a health check and clears the old error.
- **Update now**: runs an update immediately (for the sources with a latest
  releases list).
- **Edit**, **Disable/Enable**, **Duplicate**, **Delete**.
- Deleting and disabling return to the filter you started from (for example
  **Disabled**).

The frequency is per source and is set in the form, outside Advanced. "Reduce
the frequency" on the 429 problem doubles only that interval.

In the **Problems** filter there is **Test problems**: it runs the test on all
the flagged sources (problem, degraded, blocked) in the background and updates
their status. After fixing the credentials or an address, a single click checks
the remaining cases; healthy sources are not touched.

A source that fails is not retried every hour: the automatic retry follows the
ladder **1, 2, 3, 6, 12, 24, 48, 96 hours**. After the last failed attempt the
scheduler stops and waits for the user: the card shows "Automatic retry
paused". A successful test or an edit to the source resets the ladder and
resumes the normal frequency. A manual update ("Update now") works anyway. A
circuit breaker pause is not a failure: it does not lengthen the ladder, does
not disable the source and does not send alerts.

---

## 4. Searching for releases

**Search** (or `/releases`) filters the local cache. For each result:

- **Download** opens the `.torrent` or the magnet;
- **Copy magnet** copies the link to the clipboard;
- the title opens the card of the release, with provenance, download, sending
  to the client, favourites, hidden and block.

Advanced filters: category, minimum seeders, size, quality, language,
include/exclude, regex, files only, hide XXX, favourites, hidden and
duplicates. Searches can be saved.

### Manual live search

The **Live search** panel queries the chosen sites immediately instead of
reading only the cache. Choose the mode:

- **Free search**: any title;
- **TV**: query, season, episode, year and TVDb/IMDb/TMDb id;
- **Movies**: query, year and IMDb/TMDb id;
- **Music**: artist and album;
- **Books**: author and title.

You can select one or more indexers and a category. The results arrive as they
come in and are saved; sites that time out continue in the background and may
appear after a reload. The live view shows at most 50 rows and does not use the
normal pagination. With the checkboxes and **Send selected** you send several
releases to the download client in one go.

The rules (title, regex, category, infohash, source) block the releases before
saving; favourites and hidden stay local.

---

## 5. Connecting the apps

### Sonarr / Radarr / Lidarr / Readarr / Whisparr / Mylar3 / LazyLibrarian

1. **Apps → Connect an app**.
2. Enter type, name, app URL and app API key.
3. Choose the sync level:
   - **Disabled**: no contact;
   - **Add/remove only**;
   - **Full**: also updates the existing indexers.
4. In **Advanced** set the profile of this app: RSS, automatic search and
   interactive search, minimum seeders (0 = the value of the indexer) and the
   Torznab categories to send (empty = all those supported by the app). An
   indexer without categories in common is not synced with that app.
5. Save and press **Sync** (or sync all).

indexarr creates in the app only the indexers whose name starts with
`indexarr · `; the rest is not touched. The tag filter decides which sources go
to which app.

### Manual Torznab

For a client that accepts a single Torznab URL:

- aggregated endpoint: `http://<host>:8788/api`
- API key: the one of indexarr
- for a single indexer: `http://<host>:8788/torznab/<slug>/api`

Sonarr/Radarr created by indexarr use instead the base
`http://<host>:8788/torznab/<slug>`: they are the ones that add `/api`.

### gextto

In gextto add an **indexarr** source with base URL `http://<host>:8788`
(without `/api`): gextto adds the Torznab path by itself. If you choose "Other",
use the complete endpoint `/api`.

### Download clients

**Apps → Download clients**: qBittorrent, Transmission, Deluge, rTorrent,
SABnzbd, NZBGet, aMule, Aria2, uTorrent, Flood, Download Station, NZBVortex,
Hadouken and the torrent/Usenet blackholes. The default client receives the
grabs; the **Send** button in the release card uses the chosen one, and **Send
selected** in the Releases page sends several releases in one go. For aMule the
protocol of the release must be `ed2k` and the client is configured with the
URL of the Web API (usually `http://127.0.0.1:4711`) and the password of the
web interface.

In **Settings → Advanced**, "Send eD2k downloads through indexarr" is on by
default: the feed delivers the download link of indexarr, which records the
grab and redirects to `ed2k://`; the `ed2k` attribute remains the direct link
for the clients that read it. Turn it off only if a client does not follow the
redirect.

---

## 6. Notifications

**Settings → Notifications**:

- webhook and Telegram (token + chat id);
- Apprise, Discord, Slack, Gotify, ntfy, Pushover, SMTP email and custom
  script, each with its own secrets encrypted in the vault;
- events: new releases, errors, failed grabs, successful grab, catalog
  updated, failed backup;
- grab bursts from the same application are grouped into one message;
- anti-spam cooldown.

The messages are written in the language chosen in **Settings → Notifications →
Message language**: "Same as the interface" (default) follows the selector at
the top, while a fixed language stays also when you navigate in another one.
Choose the language before **Send test notification**, which checks the
channels immediately. If Telegram does not arrive, check the token, the chat id
and that the bot has been started in the chat.

---

## 7. Maintenance and security

- **Backup**: Settings → Maintenance. The backup includes the database and the
  vault state; keep the master key separately too.
- **Restore**: from the same page. After a restore, the next synchronization
  realigns the apps to the current state of indexarr.
- **Retention**: the releases and the history are pruned according to the
  configured days. A release already downloaded is not removed.
- **Logs**: the **Logs** entry in the sidebar shows the system log (the old
  Activity → Logs link redirects there). The level is changed from Settings → Advanced
  or directly from the page.
  Download the log from the dedicated button.
- **Most useful sources**: in Maintenance, the downloads served in the last 30
  days per source, with type (Feed, Web, Indexer), number and percentage.
  It is used to see which sources bring results that are actually downloaded.
- **API keys**: Settings → Torznab API. You can rotate the primary one and
  create separate keys per client, which can be revoked.
- **Vault**: passwords and keys are encrypted. The interface never shows them
  in clear; an empty field means "do not change".
- **UI access**: user and password in Settings → Security. Behind a reverse
  proxy set `public_url` and forward `X-Forwarded-Proto`/`Host`.

---

## 8. Common problems

| Symptom | What to check |
| --- | --- |
| "The site does not respond" | network, DNS, proxy, FlareSolverr; then **Test** |
| "The site rejects the requests" (403) | Cloudflare/challenge: configure FlareSolverr |
| "The site limits the requests" (429) | use **Reduce the frequency**; it is not a failure |
| "Paused after repeated errors" | the circuit breaker stopped the source after real errors: there is nothing to do, it retries by itself; if it comes back often, fix the cause of the last error |
| "The configured search returned no results" | the query or the filter of the site is wrong: open Edit |
| Login rejected | password updated? check **Site access** and the last update |
| CAPTCHA at login | FlareSolverr does not solve CAPTCHAs: wait or do a manual login |
| Smartfeed without releases | Smartfeed URL expired or generic RSS source: use **Switch to Smartfeed** |
| Latest releases empty | the source is "search only" or the update failed: look at the log |
| 404 on an old link | the release was removed by retention: search again |
| Truncated buttons | use a wider window or the mobile theme; the title wraps |

For a problem with a single source, the Overview already contains the cause,
the last error and the log: there is no need to open the system logs.

---

## 9. Useful commands

```bash
# service status
systemctl --user status indexarr.service

# health and version
curl -s http://127.0.0.1:8788/healthz

# Torznab capabilities
curl -s 'http://127.0.0.1:8788/api?t=caps'

# indexer list from the JSON API
curl -s -H "X-Api-Key: $KEY" http://127.0.0.1:8788/api/v2/indexers

# real-time log (rotating file next to the database, or the journal)
tail -f data/logs/indexarr.log
journalctl --user -u indexarr.service -f
```

---

## 10. Where to find more

- [Torznab API](API.md) — endpoints, parameters, downloads, errors.
- [JSON administrative API](ADMIN-API.md) — definitions, indexers, proxy, apps.
- [Definitions](DEFINITIONS.md) — how to write a native source.
- [Catalog](CATALOG.md) — sites, families, states and tests.
- [MirCrew](MIRCREW.md) — session, thanks and Smartfeed.
- [Vault](VAULT.md) — master key, backup, Docker secrets.
- [Architecture](ARCHITECTURE.md) — packages, flow and database.
- [QA](QA.md) — tests and checklist.
