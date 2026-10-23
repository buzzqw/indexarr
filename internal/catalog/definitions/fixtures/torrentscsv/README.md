# TorrentsCSV fixtures

Captured on 2026-10-23 from the public JSON API:

- `search.json` — `https://torrents-csv.com/service/search?q=test&size=20`

Cleaning applied before saving:

- kept the first ten objects of the `torrents` array;
- dropped `scraped_date` and `completed`, which the definition does not use;
- kept `infohash`, `name`, `size_bytes`, `created_unix`, `seeders`, `leechers`
  and `id`.

The API returns no magnet and no category: the definition builds the magnet
from the infohash and rows use the generic 8000 category.
