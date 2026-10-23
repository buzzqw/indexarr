# AudioBook Bay fixtures

Captured on 2026-10-23 from public pages:

- `search.html` — `https://audiobookbay.lu/?s=test`
- `detail.html` — the release page linked from the first result

Cleaning applied before saving:

- kept the first eight posts in the listing;
- removed scripts, styles, cover images, advertising and the direct-download
  links (their query strings are per-request);
- replaced the "Shared by" username with `user` and removed the member link;
- the detail fixture keeps only the torrent info row with the public
  infohash and the release title;
- no account, cookie or key is present.
