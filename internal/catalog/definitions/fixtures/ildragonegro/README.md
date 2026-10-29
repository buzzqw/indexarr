# ilDraGoNeRo fixtures

Captured with a real account from `https://www.ildragonero2.info/forum/` on
2026-10-29:

- `login.html` — `ucp.php?mode=login`; the standard phpBB form with
  `form_token`, `creation_time`, `sid` and `redirect`;
- `search.html` — `search.php?keywords=mandalorian&terms=all&sf=titleonly&sr=topics`;
  the real topic list (`ul.topiclist.topics`), first five rows;
- `topic-locked.html` — a season-3 topic before the first post is thanked: the
  `#magnetlock-cfg` token, three locked `.ml-box` elements and the thanks
  button.

Synthetic pages built from the real error/logout markup:

- `login-ok.html` — a page with the `mode=logout` link;
- `login-failed.html` — a `div.error` with the wrong-password message.

Cleaning applied before saving:

- kept only the search list, the locked post and the login form;
- replaced `sid`, `form_token`, `creation_time`, the magnetlock token, the
  post/user ids, usernames and avatar URLs with synthetic values;
- removed scripts, styles, the site navigation, ads and every unrelated post.
