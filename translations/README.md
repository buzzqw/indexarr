# UI translations

The actual catalogs are embedded in the binary from `internal/i18n/locales/`:

| Language | File |
|---|---|
| English (default and fallback) | `internal/i18n/locales/en.json` |
| Italian | `internal/i18n/locales/it.json` |
| French | `internal/i18n/locales/fr.json` |
| German | `internal/i18n/locales/de.json` |
| Spanish | `internal/i18n/locales/es.json` |
| Simplified Chinese | `internal/i18n/locales/zh.json` |
| Polish | `internal/i18n/locales/pl.json` |

Each file has the same keys as `en.json`; parity is checked by
`go test ./internal/i18n`. To add a language:

1. copy `en.json` to `<code>.json` (e.g. `pt.json`) and translate the values;
2. add the constant in `internal/i18n/catalog.go` (`Language`, `supported`,
   `languageNames`);
3. add the file to `internal/i18n/locales/` and check key parity with
   `go test ./internal/i18n`.

Strings with `%s`/`%d` must keep the placeholders. Product names and technical
terms (Torznab, Jackett, Prowlarr, Sonarr, `indexarr`, etc.) stay unchanged.
The language is chosen from the sidebar selector or with `?lang=<code>`; the
preference is saved in the `indexarr_lang` cookie.
