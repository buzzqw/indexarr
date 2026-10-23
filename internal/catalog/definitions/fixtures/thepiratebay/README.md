# The Pirate Bay fixtures

Captured on 2026-10-23 from the public JSON API:

- `search.json` — `https://apibay.org/q.php?q=test&cat=0`

Cleaning applied before saving:

- kept the first ten release objects;
- dropped `username`, `imdb`, `status` and `num_files` so no uploader account
  or external id is stored;
- kept `id`, `name`, `info_hash`, `leechers`, `seeders`, `size`, `added` and
  `category`.

The API returns a bare array, a hex info hash and no magnet: the definition
builds the magnet from the hash. The category is the numeric TPB category.
