# Corsaro account fixtures

The guest definition `corsaro.yaml` has its own fixtures. This directory is
for the account variant.

Public capture on 2026-10-23:

- `login.html` — `https://ilcorsaroblu.org/index.php?page=login`

Cleaning applied before saving:

- kept only `form[name=login]` with the `uid` and `pwd` fields and the
  `trp_recaptcha_required` marker;
- removed scripts, Cloudflare attributes, cookies, session ids and every
  link outside the form.

Synthetic fixtures, built from the observed listing markup:

- `search.html` — a cleaned copy of the public Corsaro listing rows, used to
  test the search selectors;
- `login-ok.html` — successful login page with a `page=logout` link;
- `login-failed.html` — login page with a `div.error` message.

The live login page asks for a reCAPTCHA. No automated login was attempted and
no credentials are stored here. The definition stays "in prova" until it is
verified with a real account.
