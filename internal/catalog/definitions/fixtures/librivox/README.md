# LibriVox fixtures

Captured on 2026-10-23 from public pages:

- `search.json` — `https://librivox.org/api/feed/audiobooks/?title=Alice%27s%20Adventures%20in%20Wonderland&format=json`
- `detail.html` — `https://librivox.org/alices-adventures-in-wonderland-by-lewis-carroll/`,
  trimmed to the title and the Internet Archive torrent link.

Cleaning applied before saving:

- kept the real API record (title, language, public LibriVox URL);
- changed `url_librivox` to a relative path so the offline test stays on the
  local server (the engine resolves it against `meta.urls`);
- no account, cookie or key is present.
