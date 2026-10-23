# Traduzioni UI

I cataloghi effettivi sono incorporati nel binario da `internal/i18n/locales/`:

| Lingua | File |
|---|---|
| Inglese (default e fallback) | `internal/i18n/locales/en.json` |
| Italiano | `internal/i18n/locales/it.json` |
| Francese | `internal/i18n/locales/fr.json` |
| Tedesco | `internal/i18n/locales/de.json` |
| Spagnolo | `internal/i18n/locales/es.json` |
| Cinese semplificato | `internal/i18n/locales/zh.json` |
| Polacco | `internal/i18n/locales/pl.json` |

Ogni file ha le stesse chiavi di `en.json` (427). Per aggiungere una lingua:

1. copia `en.json` in `<codice>.json` (es. `pt.json`) e traduci i valori;
2. aggiungi la costante in `internal/i18n/catalog.go` (`Language`, `supported`,
   `languageNames`);
3. aggiungi il file a `internal/i18n/locales/` e verifica la parità delle chiavi
   con `go test ./internal/i18n`.

Le stringhe con `%s`/`%d` devono mantenere i segnaposto. I nomi di prodotto e i
termini tecnici (Torznab, Jackett, Prowlarr, Sonarr, `indexarr`, ecc.) restano
invariati. La lingua si sceglie dal selettore nella sidebar o con
`?lang=<codice>`; la preferenza viene salvata nel cookie `indexarr_lang`.
