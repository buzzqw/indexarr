# LinuxTracker fixtures

Captured on 2026-10-23 from public pages:

- `listing.html` — `https://linuxtracker.org/torrents/?q=test`
- `detail.html` — the first release page linked from that search
  (`/torrents/dad8a156233aa21c2922d1979bfed285b19d8b06/`)

Cleaning applied before saving:

- kept the `table.torrent-table` with the first ten release rows;
- kept the detail header, the `.torrent` link, the magnet and the swarm stats;
- removed navigation, category selects, descriptions, screenshots, scripts and
  styles.

The listing has no magnet and no numeric category; the detail page carries the
magnet and `/download/`. Category filters on the site are numeric distro ids
that do not appear in the row, so the definition does not extract a per-row
category.
