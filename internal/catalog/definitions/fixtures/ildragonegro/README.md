# ilDraGoNeRo fixtures

Public capture on 2026-10-23:

- `login.html` — `https://ildragonero2.info/forum/ucp.php?mode=login`

Cleaning applied before saving:

- kept only `form#login` with username, password, `form_token`,
  `creation_time`, `sid` and the submit button;
- replaced `form_token`, `creation_time` and `sid` with synthetic values;
- removed scripts, styles, cookies, session links and every other form on the
  page (login, registration, cookie consent).

Synthetic fixtures, built from standard phpBB markup:

- `search.html` — three `div.search` topic results;
- `detail.html` — a topic with a magnet and a `.torrent` link;
- `login-ok.html` — a page with a `mode=logout` link;
- `login-failed.html` — a page with a `div.error` message.

Guest search is refused by the live site, so no real search page could be
captured without an account. The site redirects the default client to the
index page; the definition sends a desktop browser User-Agent. The state stays
"in prova" until the coordinator verifies it with credentials.
