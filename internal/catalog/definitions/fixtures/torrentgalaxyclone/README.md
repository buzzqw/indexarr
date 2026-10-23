# TorrentGalaxyClone fixtures

Captured on 2026-10-23 from public pages on the TorrentGalaxy site, which
serves both the `torrentgalaxy.one` and `torrentgalaxy.info` names:

- `listing.html` — `https://torrentgalaxy.one/get-posts/keywords:test/`
- `detail.html` — `https://torrentgalaxy.one/post-detail/7e9d56/test-2025-1080p-web-dl-dual-audio-tamil-hindi-ddp5-1-x265-x/`

Cleaning applied before saving:

- kept the first eight `div.tgxtablerow` result rows and the `div.dl-actions`
  block of the release page;
- removed scripts, styles, tracking images and the uploader-name cell;
- removed Cloudflare and session attributes, cookies and every request URL
  parameter that could identify a visitor.

The keyword listing has no date cell. Categories are path fragments such as
`/get-posts/category:Movies:time:10D`; the magnet is only on the release page.
