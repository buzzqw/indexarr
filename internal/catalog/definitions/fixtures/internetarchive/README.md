# Internet Archive fixtures

Captured on 2026-10-23 from public pages:

- `search.json` — `https://archive.org/advancedsearch.php?q=test AND (mediatype:texts OR mediatype:software)&fl=identifier,title,mediatype,publicdate,item_size&rows=10&output=json`
- `detail.html` — `https://archive.org/details/pubmed-PMC4119369`, trimmed to the
  title and the archive torrent link.

Cleaning applied before saving:

- kept the first ten search documents in the same JSON shape;
- kept the public archive torrent link (`/download/<id>/<id>_archive.torrent`);
- no account, cookie or key is present.
