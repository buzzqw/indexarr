# Cardigann → native v1 mapping

`internal/cardigann` is a local importer. It reads a Cardigann YAML file and
writes a native definition; indexarr never interprets Cardigann at runtime.

## Supported subset

| Cardigann | Native v1 |
|---|---|
| `id`, `name`, `description`, `language` | `meta.*`; `id` is slugified |
| `type` (`public`, `semi-private`, `private`) | `meta.type`; fallback `private` |
| `protocol`, `usenet`, `type: usenet` | `meta.protocol` (`torrent` by default) |
| `links`, `legacyLinks`, `urls`, `link`, `url` | `meta.urls` (http/https only, deduplicated, userinfo removed) |
| `settings[]` / settings mapping | `settings[]`; names slugified, secrets become `password`, secret defaults are not copied |
| `categories` | `categories[]` (`site`, `standard`, `desc`) |
| `caps.categorymappings` | `categories[]` |
| `caps.modes` and `supportedParams` | `capabilities.*`; missing modes default to `search: [q]`, `tv-search: [q, season, ep]` with a warning |
| `login.path`, `login.method`, `login.inputs` | `auth.strategy: form`, explicit GET (hidden CSRF/token) + POST steps |
| `login.error`, `login.test` | `auth.failure`, `auth.success` (and an extra GET if `test.path` differs) |
| `login.cookies` mapping | explicit `Cookie` header; literal values become setting references |
| `search.paths[0]`, `search.inputs` | `search.request.get`/`post` with `params`/`form` |
| `search.rows`, `search.fields`, `search.response` | `search.response.type`, `rows`, `fields` |
| `download.selectors` / `download.selector` | `fields.download`; otherwise the existing `download`/`link` field |
| `download.method` / `download.path` | kept as a link, reported as **unsupported multi-step** |
| — | `health: {query: test, min_rows: 1}`, `tests: []` (definition stays `bozza`) |

Templates are rewritten to the native language only:

| Cardigann | Native |
|---|---|
| `.Keywords`, `.Query` | `{{ query.keywords }}` |
| `.Categories`, `.Category` | `{{ categories }}` |
| `.Page`, `.Offset` | `{{ page }}`, `{{ offset }}` |
| `.Config.<name>` | `{{ settings.<slug> }}` |
| `.Query.Season`, `.Query.Episode`, … | `{{ query.season }}`, `{{ query.ep }}`, … |
| `.Categories \| join(",")` | `{{ categories \| join(",") }}` |
| `.Today`, `.Yesterday`, `.Timestamp` | **unsupported**: conversion fails for requests |

Unknown variables and malformed expressions fail closed for request, login and
search templates. They never become empty strings or raw text.

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
`search.filter`, `search.count`, `search.dateheaders`, `search.headers`,
`response.count`, `response.dateheaders`, `request.delay`, `login.headers`,
fields with a `default`, unknown category names, and unknown capability
modes/parameters (warnings for the last two). If login cannot be represented
safely no `auth` block is emitted; the definition is still written with an
explicit warning.

## Security

Only setting **names** are referenced. Literal passwords, API keys, tokens,
usernames, CSRF values and cookie values found in `login.inputs`/`login.cookies`
are never copied: they become setting references or page extractors. All
warnings are redacted with the collected secret values.

All files in `testdata/` are synthetic and were written for these tests.
