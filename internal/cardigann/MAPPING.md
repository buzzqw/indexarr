# Cardigann → native v1 mapping

`internal/cardigann` is a local importer. It reads a Cardigann YAML file and
writes a native definition; indexarr never interprets Cardigann at runtime.

Every produced document is serialized, parsed and validated with
`definition.Parse` before it is returned. Output is deterministic: the body is
canonical YAML and the hash is `definition.Fingerprint(body)`.

## Family mapping (API definitions)

When a definition is clearly an API client for a native family connector, the
converter emits a **family definition** instead of translating unreachable
request/response templates. It keeps `meta`, the category mappings and
capabilities, sets `connector`, and omits `auth`/`search`.

| Detected source | Native |
|---|---|
| `api/torrents` / `api/torrents/filter`, or a `download_link` response field | `connector: unit3d` |
| path containing `torznab`/`newznab`, or `t` + `apikey`/`cat` with an XML response | `connector: torznab` (`meta.protocol: torrent`) or `connector: newznab` (`meta.protocol: usenet`) |

Family definitions get these settings:

- `url` (text, default = primary link, or primary link + API path for
  Torznab/Newznab);
- `apikey` (password, required) or the source `passkey` when that is the only
  key; `info_*` help settings are dropped.

A warning records why the family mapping was chosen and that Bearer header
templates were not translated. API keys/defaults are never copied.

## Supported subset

| Cardigann | Native v1 |
|---|---|
| `id`, `name`, `description`, `language` | `meta.*`; `id` is slugified |
| `type` (`public`, `semi-private`, `private`) | `meta.type`; fallback `private` |
| `protocol`, `usenet`, `type: usenet` | `meta.protocol` (`torrent` by default) |
| `links`, `legacyLinks`, `urls`, `link`, `url` | `meta.urls` (http/https only, deduplicated, userinfo removed) |
| `settings[]` / settings mapping | `settings[]`; names slugified, secrets become `password`, secret defaults are not copied, `type: info` help entries are dropped |
| `categories` | `categories[]` (`site`, `standard`, `desc`) |
| `caps.categorymappings` | `categories[]` |
| `caps.modes` and `supportedParams` | `capabilities.*`; missing modes default to `search: [q]`, `tv-search: [q, season, ep]` with a warning |
| `login.path`, `login.method`, `login.inputs` | `auth.strategy: form`, explicit GET (hidden CSRF/token) + POST steps |
| `login.error`, `login.test` | `auth.failure`, `auth.success` (and an extra GET if `test.path` differs) |
| `login.cookies` mapping | explicit `Cookie` header; literal values become setting references |
| `search.paths[0]`, `search.inputs` | `search.request.get`/`post` with `params`/`form` |
| `search.rows`, `search.fields`, `search.response` | `search.response.type`, `rows`, `fields` |
| `search.paths[].response` | nested response type/rows/fields are accepted; the first usable path wins |
| `search.headers` / `search.paths[].headers` | `search.request.headers` |
| `download.selectors` / `download.selector` | `fields.download`; otherwise the existing `download`/`link` field |
| `download.method` / `download.path` | kept as a link, reported as **unsupported multi-step** |
| — | `health: {query: test, min_rows: 1}`, `tests: []` (definition stays `bozza`) |

### YAML tolerance

- Scalar, list and mapping shapes are accepted for settings, options, inputs,
  fields, selectors, modes and category mappings.
- Header values may be strings or lists. A list becomes one header with values
  joined by `", "` (HTTP-equivalent), with a warning.
- `login.error[].message` may be a string or `{text: ..., selector: ...}`.
- Double-quoted scalars with unknown escapes (for example `"\d"`) are retried
  with the backslash preserved, with a warning. Other YAML errors still fail.

## Templates

Templates are rewritten to the native language only:

| Cardigann | Native |
|---|---|
| `.Keywords`, `.Query` | `{{ query.keywords }}` |
| `.Categories`, `.Category` | `{{ categories }}` |
| `.Page`, `.Offset` | `{{ page }}`, `{{ offset }}` |
| `.Config.<name>` | `{{ settings.<slug> }}` |
| `.Query.Season`, `.Query.Episode` | `{{ query.season }}`, `{{ query.ep }}` |
| `.Query.IMDBID`, `.Query.IMDBIDShort` | `{{ query.imdbid }}` |
| `.Query.TVDBID`, `.Query.TMDBID` | `{{ query.tvdbid }}`, `{{ query.tmdbid }}` |
| `.Categories \| join(",")` | `{{ categories \| join(",") }}` |
| `join .Categories ","` (function form) | `{{ categories \| join(",") }}` |
| `.Today`, `.Yesterday`, `.Timestamp` | **unsupported**: conversion fails for requests |

### Approximations (always warned)

The native language has no loops, conditionals or fallback expressions, so a
few Cardigann constructs are deliberately approximated. Only patterns found in
the measurement corpus are accepted; anything else still fails closed.

| Cardigann | Native approximation |
|---|---|
| `search.inputs.$raw` | the rendered query fragment is parsed into individual `params`; repeated keys keep one value |
| `{{ range .Categories }}&categories[]={{.}}{{end}}` | param `categories[] = {{ categories }}` |
| `{{ range .Categories }}cat{{.}}=1&{{end}}` | param `cat = 1` (per-category keys collapsed) |
| `{{ range .Categories }}{{.}};{{end}}` | `{{ categories \| join(";") }}` |
| `{{ range .Categories }}prefix{{.}}suffix{{end}}` | prefix/suffix kept around the comma-joined ids |
| `{{ if .Keywords }}THEN{{ else }}ELSE{{ end }}` | `THEN` (an empty query sends the then-branch) |
| `{{ if .Config.X }}THEN{{ else }}ELSE{{ end }}` | branch chosen from the declared default; booleans default to `false` |
| `{{ if .Config.X }}` with no declared default | `THEN` |
| `{{ if and/or (...) (...) }}`, `eq`/`ne` on settings | best-effort evaluation of the condition |
| `{{ if .Query.DoubanID }}` (unsupported query variable) | `ELSE`: the native engine can never supply that value |
| inline `{{ or .A .B }}` | first operand the native language knows |
| inline `{{ re_replace .Keywords ... }}` | `{{ query.keywords }}` (the regex rewrite is lost) |
| field `text: "{{ .Result.title_optional }}…"` | resolved to the first referenced result field that has a real selector, filters preserved |

Unknown variables, unknown query variables outside conditions, malformed
expressions and unknown condition shapes fail closed for request, login and
search templates. They never become empty strings or raw text. The required
`title` field and a `download`/`magnet`/`details` link are never dropped
silently: conversion fails if no title can be resolved, and warns when no link
is available.

## Filters

`trim`, `tolower`/`lower`, `toupper`/`upper`, `replace`, `append`/`suffix`,
`prepend`/`prefix`, `dateparse`, `timeparse`, `regexp` (without replacement),
`querystring`/`queryparam`, and `htmldecode` (implicit in native extraction) are
mapped. `split`, `join`, `urldecode`, replacements in `regexp`, unknown filters,
and filter shapes that cannot be encoded are dropped and listed in
`Result.Unsupported`.

## Reported as unsupported

`captcha`/2FA, custom Go, non-form login methods, multi-step download flows,
unsupported `response.type`, `keywordsfilters`, `titlefilters`, `prefilter`,
`search.filter`, `search.count`, `search.dateheaders`, `response.count`,
`response.dateheaders`, `request.delay`, `login.headers`, fields with a
`default`, unknown category names, and unknown capability modes/parameters
(warnings for the last two). If login cannot be represented safely no `auth`
block is emitted; the definition is still written with an explicit warning.

## Security

Only setting **names** are referenced. Literal passwords, API keys, tokens,
usernames, CSRF values and cookie values found in `login.inputs`/`login.cookies`
are never copied: they become setting references or page extractors. All
warnings are redacted with the collected secret values.

## Corpus measurement

`TestCorpus` in `corpus_test.go` is skipped unless `CARDIGANN_CORPUS` points at
a directory of definitions. It reports converted/failed counts and a failure
histogram; `CARDIGANN_CORPUS_VERBOSE=1` also lists each failure. The corpus is
read-only measurement input and is never copied into the repository. Normal
`go test ./internal/cardigann` stays corpus-free.

The last measurement over the 927-file Prowlarr corpus converted **926** files
and failed **1** (a definition whose every field is filter-only with no
selector, so no native title can be extracted).

All files in `testdata/` are synthetic and were written for these tests.
