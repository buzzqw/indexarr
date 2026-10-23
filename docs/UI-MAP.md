# Mappa UI di indexarr 2

Inventario delle route web e della navigazione prevista dal redesign. Serve a
tenere allineati template, redirect e test: `scripts/ui-smoke.sh` verifica questa
mappa su un'istanza già in esecuzione, senza modificare nulla.

Stato: **in transizione**. Le route `/activity` sono il nuovo punto di ingresso
per run e log, ma durante il redesign possono rispondere `404`: lo smoke test le
riporta come `pending`, non come errore. Le route legacy restano raggiungibili o
reindirizzano, così i bookmark e i link esterni non si rompono.

## Navigazione prevista

Una sola voce per azione, soprattutto per “aggiungi indexer”. Il legacy
(`/sources`) e il nativo (`/indexers`) restano due concetti distinti, non due
pulsanti che portano allo stesso posto.

| Gruppo | Voce | Route | Note |
|---|---|---|---|
| — | Dashboard | `/` | Pagina iniziale |
| — | Releases | `/releases` | Ricerca e cache release |
| — | Attività | `/activity?tab=runs` | Sostituisce `/runs` nella navigazione |
| — | Attività | `/activity?tab=logs` | Sostituisce `/logs` nella navigazione |
| Indexer | Indexer nativi | `/indexers` | Runtime nativo (definizioni, test, run) |
| Indexer | Sorgenti legacy | `/indexers?tab=legacy` | Vista legacy; `/sources` resta compatibile |
| Indexer | Aggiungi indexer | `/indexers/new` | Unica voce di creazione nativa |
| Definizioni | Definizioni | `/definitions` | Catalogo e definizioni locali |
| Integrazioni | Applicazioni | `/applications` | Sync Sonarr/Radarr/… |
| Integrazioni | Importa | `/import` | Anteprima Prowlarr/Jackett |
| Sistema | Regole | `/rules` | Blocklist e regole |
| Sistema | Impostazioni | `/settings` | API key, FlareSolverr, opzioni |
| Sistema | Manutenzione | `/maintenance` | Report, backup, restore |

Regole di navigazione:

- `/sources` e `/indexers` non devono comparire **entrambe** come voci
  “aggiungi indexer” nel `<nav>`; una delle due è un elenco, l'altra è la
  creazione, oppure `/sources` reindirizza alla tab legacy.
- Le etichette del `<nav>` devono essere univoche: due voci con la stessa
  etichetta e destinazioni diverse sono un errore.
- Lo stesso percorso può comparire due volte solo se è chiaramente un contenitore
  e un figlio; lo smoke test lo segnala come warning, non come errore hard.

## Inventario route

| Vecchia route | Route canonica / nuova | Scopo | Redirect | Note |
|---|---|---|---|---|
| `/` | `/` | Dashboard | — | Sempre 200 |
| `/releases` | `/releases` | Elenco e ricerca release | — | `?query=` usato dalla ricerca live |
| `/releases/results` | `/releases/results` | Frammento HTML dei risultati | — | Non è una pagina completa: niente `<nav>` |
| `/releases/{id}` | `/releases/{id}` | Dettaglio release | — | Link generati dalle liste |
| `/releases/{id}/torrent` | invariata | Download `.torrent` dalla UI | — | Non nel route matrix |
| `/sources` | `/indexers?tab=legacy` | Elenco sorgenti legacy | 200 o 302/303 | **Compatibilità**: accettati entrambi durante il redesign |
| `/sources/new` | `/sources/new` | Nuova sorgente legacy | 200 o 302/303 | Può diventare `/indexers/new?tab=legacy`; lo smoke accetta il redirect |
| `/sources/{id}/edit` | invariata | Modifica sorgente legacy | — | Resta sul percorso legacy |
| `/sources/discover` | invariata | Scoperta automatica | 303 | Redirect già esistente |
| `/sources/bulk` | invariata | Import massivo RSS | — | Form POST separato |
| `/sources/export` | invariata | Export JSON | — | Download, non pagina |
| `/indexers` | `/indexers` | Elenco indexer nativi | — | Non è la pagina “aggiungi” |
| `/indexers?tab=legacy` | `/indexers?tab=legacy` | Vista legacy dentro Indexer | — | Nuova tab prevista dal redesign |
| `/indexers/new` | `/indexers/new` | Nuovo indexer nativo | — | Unica voce “aggiungi indexer” |
| `/indexers/{id}` | `/indexers/{id}` | Dettaglio indexer nativo | — | Test e run nell'azione |
| `/indexers/{id}/edit` | invariata | Modifica indexer nativo | — | — |
| `/definitions` | `/definitions` | Definizioni e catalogo | — | — |
| `/definitions/new` | `/definitions/new` | Nuova definizione YAML | — | — |
| `/definitions/example-html` | invariata | Definizione dimostrativa | — | Usata da QA e documentazione |
| `/definitions/{id}` | `/definitions/{id}` | Dettaglio definizione | — | `id` è uno slug, non un numero |
| `/applications` | `/applications` | Applicazioni \*arr collegate | — | — |
| `/applications/new` | `/applications/new` | Nuova applicazione | — | — |
| `/applications/{id}/edit` | invariata | Modifica applicazione | — | — |
| `/import` | `/import` | Import Prowlarr/Jackett | — | Anteprima, non applica |
| `/runs` | `/activity?tab=runs` | Storico run | 302/303 verso Attività | **Compatibilità**: 200 accettato durante la transizione |
| `/runs/{id}` | `/runs/{id}` | Dettaglio run | — | Resta fuori dalle tab |
| `/logs` | `/activity?tab=logs` | Log runtime | 302/303 verso Attività | **Compatibilità** |
| `/logs/download` | invariata | Download del log | — | File, non pagina |
| `/activity?tab=runs` | stessa | Tab run | — | Nuova: `pending` se 404 |
| `/activity?tab=logs` | stessa | Tab log | — | Nuova: `pending` se 404 |
| `/rules` | `/rules` | Regole/blocklist | — | Gruppo **Sistema** |
| `/settings` | `/settings` | Impostazioni e API key | — | Gruppo **Sistema** |
| `/maintenance` | `/maintenance` | Report, backup, restore | — | Gruppo **Sistema** |
| `/maintenance/backup` | invariata | Download backup immediato | — | File, non pagina |
| `/maintenance/backups/{name}` | invariata | Download di un backup | — | File, non pagina |
| `/healthz` | `/healthz` | Health JSON | — | Fuori autenticazione UI |
| `/metrics` | `/metrics` | Metriche Prometheus | — | Fuori autenticazione UI |
| `/api?t=caps` | invariata | Capacità Torznab | — | `caps` non richiede API key |
| `/newznab/api?t=caps` | invariata | Alias Newznab | — | Stesso handler di `/api` |
| `/torznab/api?t=caps` | invariata | Alias Torznab | — | Stesso handler di `/api` |
| `/dl/{id}.torrent` | invariata | Download Torznab | — | Escluso dallo smoke (file) |
| `/static/app.css` | invariata | Foglio di stile | — | Asset, escluso dai link |
| `/static/app.js` | invariata | Script UI | — | Asset, escluso dai link |
| `/sw.js` | invariata | Service worker | — | Asset, escluso dai link |
| `/api/v2/...` | invariata | API amministrativa | — | Fuori UI, autenticata con API key |

### Route mantenute per compatibilità

Non vanno rimosse senza un redirect:

- `/sources`, `/sources/new`, `/sources/{id}/edit`, `/sources/discover`,
  `/sources/bulk`, `/sources/export` — mondo legacy.
- `/runs` e `/logs` — vecchi ingressi di Attività; devono reindirizzare alle
  tab corrispondenti oppure continuare a rispondere 200 durante la transizione.
- `/runs/{id}` e `/logs/download` — dettagli e file che non hanno una tab.
- `/api`, `/newznab/api`, `/torznab/api`, `/dl/` — contratti Torznab/Newznab:
  non sono UI e non cambiano con il redesign.
- `/healthz`, `/metrics`, `/sw.js`, `/static/` — integrazioni operative.

### Legacy e nativo

- **Legacy** (`sources`): feed RSS/HTML e import Prowlarr/Jackett. Ha ancora il
  suo form (`/sources/new`) e il suo export.
- **Nativo** (`indexers`): definizioni YAML, vault dei segreti, test a fasi,
  run scheduler. Nasce da `/indexers/new`.
- La vista `/indexers?tab=legacy` è il ponte: mostra le sorgenti legacy dentro
  la nuova area Indexer. `/sources` può rispondere 200 o reindirizzare lì.
- Il ponte non deve diventare un doppione: se entrambe le pagine offrono un
  pulsante “aggiungi indexer”, il test fallisce.

## Come eseguire

Lo smoke test richiede un'istanza **già avviata** e fa solo richieste `GET`.
Non avvia, ferma o riavvia il servizio e non legge né scrive il database.

```sh
# Istanza locale sulla porta predefinita (http://127.0.0.1:8484)
./scripts/ui-smoke.sh

# Tramite Make
make ui-smoke

# Base diversa
INDEXARR_BASE=http://127.0.0.1:9000 ./scripts/ui-smoke.sh

# Con autenticazione Basic attiva, le credenziali possono stare nell'URL
INDEXARR_BASE=http://utente:password@127.0.0.1:8484 ./scripts/ui-smoke.sh

# Trasforma i warning in errori (utile in CI dopo il redesign)
UI_SMOKE_STRICT=1 ./scripts/ui-smoke.sh
```

Comportamento:

- Controlla la matrice di route e, per ogni pagina HTML, estrae `href` e
  `action` interni; ogni percorso distinto viene verificato una sola volta.
- Salta `/dl/`, `mailto:`, i frammenti `#...`, gli host esterni, `/static/*`,
  `/sw.js`, i file (CSS, JS, immagini), `/maintenance/backup` e
  `/maintenance/backups/{name}`: questi ultimi creerebbero o scaricherebbero un
  database, quindi restano fuori da un test dichiaratamente read-only.
- Un `404` o un `5xx` su un link è un errore hard. Un `302/303` è ammesso. Un
  `405` su un `action` è normale: il form è POST-only e lo smoke non fa POST.
- `/activity?tab=runs` e `/activity?tab=logs` sono `pending` se rispondono `404`;
  non fanno fallire né la modalità normale né quella strict.
- Warning: `<html lang="it">` mancante, skip link mancante, voci di navigazione
  ambigue, link che rispondono `4xx` non critici. Con `UI_SMOKE_STRICT=1` i
  warning valgono come errori.
- Non serve `jq`: bastano `curl`, `grep`, `sed`, `awk`, `sort` e `uniq`.
- Esce `1` se ci sono errori hard (o warning in strict), `0` altrimenti.

### Output

```
ok      /releases 200
pending /activity?tab=runs 404 (expected after redesign)
WARN    <html lang="it"> missing on 19/19 HTML pages (e.g. /)
...
==> summary
routes:   26 checked: 24 ok, 0 failed, 2 pending
links:    659 checked: 659 ok, 0 failed
```

## Punti confusi che il test intercetta

1. **Due “aggiungi indexer”**: `/sources` e `/indexers` entrambe presentate come
   voce di creazione nel `<nav>`. Il test fallisce; la creazione nativa deve
   stare su `/indexers/new` e il legacy deve essere una tab o un redirect.
2. **Voci di navigazione duplicate**: stessa etichetta verso destinazioni
   diverse, oppure stesso percorso con etichette diverse (warning).
3. **Link interni rotti** dopo una rinomina: un `href` o `action` che risponde
   `404`/`5xx` su una pagina HTML viene segnalato con il percorso esatto.
4. **Vecchie route sparite**: `/runs` e `/logs` devono continuare a funzionare o
   reindirizzare; un `404` secco è un errore hard.
5. **Nuove tab non ancora pronte**: `/activity?tab=runs` e `?tab=logs` sono
   `pending` finché il redesign non le monta; non bloccano la CI.
6. **Lingua sbagliata**: `lang="en"` residuo o `<html>` senza `lang="it"` è un
   warning (errore in strict).
7. **Skip link mancante**: l'ancora “salta al contenuto” (`#main` o `#content`)
   deve restare in tutte le pagine.
8. **Form POST scambiati per link**: un `405` su un `action` è corretto; un `405`
   su un `href` è un warning perché il collegamento non può funzionare.
9. **Legacy e nativo confusi**: `/indexers?tab=legacy` deve mostrare il legacy
   senza creare una seconda pagina di creazione; `/sources` non deve duplicare
   `/indexers`.
10. **Asset e download**: `/dl/`, `/static/*`, `/sw.js` e i backup sono esclusi
    dai link per non confondere file e pagine; i backup sono verificati solo
    come `GET` di file.
11. **Autenticazione**: se la UI richiede Basic auth e l'URL non contiene le
    credenziali, la matrice fallisce con `401`; non è un link rotto ma un test
    mal configurato.
12. **Query string**: i link vengono normalizzati al percorso, quindi
    `/releases?query=test` non viene testato due volte; le tab di `/activity`
    sono distinte nella navigazione perché lì la query conta.

## Stato al 23 ottobre 2026

- `/activity?tab=runs` e `/activity?tab=logs`: `404` → `pending`.
- `<html lang="it">`: ancora assente (`lang="en"`) → warning.
- Skip link: presente.
- Link interni verificati sull'istanza locale: 659, nessuno rotto.
- Route legacy: tutte raggiungibili con `200` o redirect.
