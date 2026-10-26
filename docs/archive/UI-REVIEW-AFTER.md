> **Aggiornamento successivo.** Dopo questa verifica sono stati introdotti la
> sidebar con drawer mobile, l'i18n (inglese default + 7 lingue) e la
> separazione nativi/legacy nelle definizioni. I P1 "menu mobile" e
> "localization leftovers" sono quindi in gran parte risolti; restano i punti
> su tabelle molto larghe, paginazione e stringhe legacy non ancora estratte
> a chiavi `T`.

# Revisione UI dopo il redesign

> **Documento storico.** La pagina `/import` e l'import da Jackett/Prowlarr sono stati rimossi il 25/10/2026: le voci che li citano descrivono la revisione precedente, non il prodotto attuale.


Data: 2026-10-23 · Ambito: working tree corrente di `/home/andres/indexarr` (nessuna modifica al codice)
Istanza isolata: `127.0.0.1:18490`, DB temporaneo copiato dal DB live (37 sorgenti legacy, 37 indexer ponte, 7 definizioni built-in). Il servizio live su `:8484` non è stato toccato.

## Verdetto

**Sì per l'IA, no per la finitura.** La confusione vecchio/nuovo è risolta: una sola voce **Indexer**, separazione netta **Nativi / Ponte legacy**, nessuna duplicazione nei conteggi, redirect canonici funzionanti, attività unificata e menu **Sistema** coerente.

Restano però difetti P1 che impediscono di considerare il redesign "chiuso": la navigazione mobile nasconde la voce attiva, la colonna ID delle Definizioni è illeggibile a desktop, e cinque pagine storiche (Release, Regole, Impostazioni, Manutenzione, form sorgente legacy) sono ancora in inglese dentro una shell italiana.

## Cosa funziona

- **Navigazione**: una sola voce top-level `/indexers` (nessun doppione); `/sources` non è più in nav. Menu `Sistema` con Regole/Impostazioni/Manutenzione, apribile e leggibile anche a 390 px.
- **Tabs**: `Nativi 0` / `Ponte legacy 37` su `/indexers`; `Run` / `Log` su `/activity`. `aria-current="page"` presente sia sulla voce di nav sia sulla tab attiva.
- **Separazione dati**: la tab Nativi non contiene righe con `legacy_source_id > 0` (0 righe, empty state corretto). La tab legacy mostra le 37 sorgenti. `/definitions` mostra 7 voci di catalogo e **0** definizioni locali, con la nota "Ci sono 37 definizioni legacy generate dal ponte"; nessuna riga `legacy-*` compare come definizione locale.
- **Conteggi dashboard**: "Indexer nativi 0" e "Sorgenti legacy 37": i 37 indexer ponte non gonfiano il conteggio nativo.
- **Redirect**: `/sources` → 302 `/indexers?tab=legacy`; `/runs` → 302 `/activity?tab=runs`; `/logs` → 302 `/activity?tab=logs` (Location relativi, verificati con curl).
- **Lingua e struttura**: `<html lang="it">` e skip link "Salta al contenuto" su tutte le 12 pagine controllate; un solo `<h1>` per pagina; nessun ID duplicato.
- **Accessibilità di base**: tutti i controlli di form hanno label/aria-label; tutti i `th` hanno `scope="col"`; tab order corretto (skip link → brand → nav → Sistema); focus ring `2px` visibile su tutti i 10 elementi testati; contrasto AA (muted 5,28–5,98:1, accent 6,7:1, danger 6,57:1).
- **Responsive**: nessun overflow orizzontale della pagina (`scrollWidth == innerWidth`) a 390, 820, 1024 e 1440 px; le tabelle diventano card impilate sotto 720 px.
- **Stati vuoti e form**: empty state di Nativi, Applicazioni, Regole e tabella locale corretti; form Importa, nuovo indexer nativo e aggiunta rapida utilizzabili; input a 44 px su mobile.
- **Smoke test**: `scripts/ui-smoke.sh` passa sia in modalità normale sia con `UI_SMOKE_STRICT=1` (26/26 route, 437/437 link, 0 warning, 0 failure).

## Problemi residui

### P0
Nessuno.

### P1

1. **Nav mobile: la voce attiva è fuori schermo e non c'è alcun affordance di scroll**
   - Pagine: tutte, a 390 px; evidente su `/import`, `/applications`, `/activity?tab=runs`.
   - Evidenza: `.main-nav` ha `clientWidth=390` e `scrollWidth=732`; lo scrollbar è nascosto (`::-webkit-scrollbar { display:none }`). Lo screenshot mostra "Definizi…" tagliata a metà e le voci Applicazioni/Importa/Attività invisibili; la tab attiva non viene portata in vista.
   - Fix suggerito: a `<720px` mandare a capo la nav su due righe, oppure mostrare un gradiente/freccia di scroll e fare `scrollIntoView` della voce `.active` al caricamento. Non nascondere del tutto lo scrollbar.

2. **`/definitions`: colonna ID compressa fino a una lettera per riga**
   - Selettore: `#main .panel:first-of-type table tbody td:first-child code` (colonna ID).
   - Evidenza: larghezza cella 41 px a 1440 px e 14 px a 1024 px per `example-html`; il testo va a capo a ogni carattere ("exam / ple- / html"). L'azione lunga "Crea indexer da questa definizione" contribuisce alla compressione.
   - Fix suggerito: `td:first-child code { white-space: nowrap; }` + `min-width` della tabella (scroll orizzontale già presente), oppure troncare l'ID con `title`; accorciare il bottone a "Crea indexer".

3. **Localizzazione incompleta: pagine storiche ancora in inglese dentro la shell italiana**
   - `/releases`: filtri "Search / Source / Category / Sort / Resolution / Language / Min seeders / Favorites / Show hidden", tabella "TITLE / SIZE / SEEDERS / SOURCE / CATEGORY".
   - `/rules`: "Add rule", "Rule type", "Active rules", "No rules".
   - `/settings`: "Copy key", "Additional API keys", "New key name", "Add key", "Clients send this key…".
   - `/maintenance`: "Database size", "Stored .torrent data", "Database report", "Checks and housekeeping", "Refresh".
   - `/sources/new` e `/sources/{id}/edit`: titolo italiano ma campi "Preset / Name / Type / Default category / Manager / Categories / Interval / Enabled" e bottoni "Create source / Create and test / Create and run".
   - Impatto: il flusso legacy (proprio quello che il redesign doveva riordinare) è il più visibilmente "vecchio"; la pagina Release, primaria, è interamente in inglese.
   - Fix suggerito: tradurre i template `releases.html`, `rules.html`, `settings.html`, `maintenance.html`, `source_form.html` (e i `data-confirm`), mantenendo in inglese solo identificatori tecnici come `apikey`, `PRAGMA`, `Torznab`.

### P2

1. **Header a 3 righe tra 721 e ~900 px** — a 820 px `.topbar-inner` è alto 137 px: brand da solo, nav sotto, "Sistema" orfano su una terza riga. Fix: alzare il breakpoint mobile a ~960 px o lasciare la nav scrollabile già da lì.
2. **Touch target sotto i 44 px** — `.btn.small` 38 px e `.main-nav a` 40 px a 390 px; le checkbox di selezione sono 18×18 px. Fix: minimo 44 px per i controlli touch, mantenendo 38 px solo per desktop.
3. **Liste molto lunghe senza paginazione** — `/indexers?tab=legacy`: 37 card, altezza documento ~22.322 px a 390 px; `/activity?tab=runs`: 200 righe. Fix: paginazione o "mostra altri" (la lista legacy ha già i filtri di bulk, non un filtro di ricerca).
4. **Stringa inglese nel bulk mobile** — `content: "Select all"` in `app.css` (thead della tabella con checkbox) resta in inglese anche nelle pagine localizzate. Fix: tradurre in "Seleziona tutto".

## Screenshot (in `/tmp`, non committati)

- Desktop 1440×1000: `/tmp/ui-check/shots/desktop-{dashboard,indexers-native,indexers-legacy,definitions,applications,import,activity-runs,activity-logs,settings,maintenance}.png`
- Mobile 390×844: `/tmp/ui-check/shots/mobile-{dashboard,indexers-native,indexers-legacy,definitions,applications,import,activity-runs,activity-logs,settings,maintenance}.png`
- Extra: `/tmp/ui-check/shots/desktop-definitions-local.png`, `desktop-legacy-table.png`, `mobile-legacy-table.png`, `desktop-source-new.png`, `mobile-source-new.png`, `desktop-indexer-new.png`, `desktop-rules.png`, `desktop-releases.png`, `desktop-sistema-open.png`, `mobile-sistema-open.png`, `tablet-820-dashboard.png`, `tablet-1024-definitions.png`

## Verifiche eseguite

```bash
CGO_ENABLED=0 go build -o /tmp/ui-check/indexarr ./cmd/indexarr          # OK
sqlite3 data/indexarr.db ".backup '/tmp/ui-check/db.sqlite'"             # 37 sources
/tmp/ui-check/indexarr -config /tmp/ui-check/config.json                 # :18490, DB temp

curl -s -o /dev/null -w '%{http_code} %{redirect_url}' .../sources       # 302 .../indexers?tab=legacy
curl -s -o /dev/null -w '%{http_code} %{redirect_url}' .../runs          # 302 .../activity?tab=runs
curl -s -o /dev/null -w '%{http_code} %{redirect_url}' .../logs          # 302 .../activity?tab=logs

sqlite3 /tmp/ui-check/db.sqlite "select count(*) from indexers where legacy_source_id>0;"  # 37
sqlite3 /tmp/ui-check/db.sqlite "select count(*) from indexers where legacy_source_id is null or legacy_source_id=0;"  # 0
# HTML: /indexers?tab=native -> 0 nomi legacy, "Nessun indexer nativo configurato"
# HTML: /definitions -> 7 catalogo, 0 righe "legacy-*", nota "37 definizioni legacy"
# HTML: dashboard -> "Indexer nativi 0", "Sorgenti legacy 37"

INDEXARR_BASE=http://127.0.0.1:18490 ./scripts/ui-smoke.sh
# routes 26/26 ok, links 437/437 ok, nav failures 0, warnings 0, exit 0
UI_SMOKE_STRICT=1 INDEXARR_BASE=http://127.0.0.1:18490 ./scripts/ui-smoke.sh
# identico, exit 0
```

Misure DOM (Chrome DevTools Protocol, `--headless=new`):

- `document.documentElement.scrollWidth == innerWidth` sulle pagine campione a 390 (tutte le 10 pagine richieste), 820 (dashboard), 1024 (definizioni) e 1440 (tutte le 10 pagine richieste).
- `.main-nav` a 390: `clientWidth 390`, `scrollWidth 732`.
- `.topbar-inner` a 820: altezza 137 px (3 righe).
- Cella ID definizioni: 41 px a 1440, 14 px a 1024.
- Form: 0 controlli senza label accessibile; 0 bottoni senza testo; `th[scope]` 100%.
- Tab order: skip link, brand, 7 voci nav, summary Sistema; `:focus-visible` con outline 2 px.
- Contrasto: muted 5,28–5,98:1; accent bianco 6,7:1; danger 6,57:1; ok 5,87:1.

## Note

- Nessuna modifica a Go, template, CSS, script o history; nessun segreto riportato; screenshot e DB temporaneo restano in `/tmp/ui-check`.
- Istanza temporanea e Chrome headless fermati a fine revisione; servizio live `:8484` e container non toccati.
