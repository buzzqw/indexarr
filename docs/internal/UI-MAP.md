# Mappa UI di indexarr 2

> **Documento storico.** La pagina `/import` e l'import da Jackett/Prowlarr sono stati rimossi il 25/10/2026: le voci che li citano descrivono la revisione precedente, non il prodotto attuale.


Inventario delle route web e della navigazione dell'area Indexer unificata.
Serve a tenere allineati template, redirect e test: `scripts/ui-smoke.sh`
verifica questa mappa su un'istanza già in esecuzione, senza modificare nulla.

Stato: **indexer unico**. Ogni sito è un indexer nativo. Non esistono più le
sorgenti legacy, la tab "Legacy bridge" o le pagine `/sources`: i vecchi
bookmark vengono reindirizzati a `/indexers`, così i link esterni non si
rompono.

## Navigazione prevista

Una sola voce per azione. La creazione di un indexer vive solo su
`/indexers/new`; l'elenco e i dettagli stanno su `/indexers`.

| Voce | Route | Contenuto |
|---|---|---|
| Home | `/` | Stato da `HomeStatus`, endpoint da copiare, ultime 24 ore, prossimo passo |
| Cerca | `/releases` | Ricerca e cache release |
| Indexer | `/indexers` | Elenco unico, dettaglio, aggiunta, import |
| App | `/applications` | App \*arr e client di download |
| Attività | `/activity?tab=runs` | Aggiornamenti, ricerche, grab, log |
| Impostazioni | `/settings?section=…` | Generale, Sicurezza, Rete, Aggiornamenti, Regole, Backup, Manutenzione, Avanzate |

Le voci di primo livello sono esattamente sei. Definizioni, import e regole
vivono dentro Impostazioni (Avanzate e Regole) e non hanno più una voce
propria. `/definitions`, `/rules` e `/maintenance` rispondono 302 verso la
sezione corrispondente; `/definitions/new` e `/definitions/{id}` restano
raggiungibili dagli strumenti.

Regole di navigazione:

- Le etichette del `<nav>` devono essere univoche: due voci con la stessa
  etichetta e destinazioni diverse sono un errore.
- Lo stesso percorso può comparire due volte solo se è chiaramente un contenitore
  e un figlio; lo smoke test lo segnala come warning, non come errore hard.
- `/sources`, `/runs`, `/logs`, `/definitions`, `/rules` e `/maintenance` non
  sono più pagine di primo livello: rispondono 302 verso la nuova posizione.

## Inventario route

| Route | Scopo | Note |
|---|---|---|
| `/` | Dashboard | Sempre 200 |
| `/releases` | Elenco e ricerca release | `?indexer=` filtra per provenienza |
| `/releases/results` | Frammento HTML dei risultati | Non è una pagina completa: niente `<nav>` |
| `/releases/{id}` | Dettaglio release | Link generati dalle liste |
| `/releases/{id}/torrent` | Download `.torrent` dalla UI | Non nel route matrix |
| `/indexers` | Elenco unico per sito | Badge, stato, menu "⋯", azioni di massa dopo la selezione |
| `/indexers/new` | Ricerca sito o indirizzo incollato | Form compatto: Nome, Categorie, soli campi obbligatori |
| `/indexers/{id}` | Pagina di lavoro del sito | Stato, statistiche, release, "Avanzate e diagnostica" |
| `/indexers/{id}/edit` | Modifica indexer | Stesso form compatto dell'aggiunta |
| `/indexers/{id}/toggle` | Attiva o disattiva | POST |
| `/indexers/{id}/duplicate` | Duplica un indexer | POST |
| `/indexers/bulk` | Azioni di massa | POST: enable, disable, delete |
| `/indexers/export` | Export JSON della configurazione | Solo valori pubblici, mai segreti |
| `/indexers/import` | Import JSON della configurazione | Crea indexer nativi |
| `/indexers/discover` | Scoperta feed su una pagina | Aggiunge un indexer RSS |
| `/indexers/quick` | Aggiunta rapida di un feed RSS | POST |
| `/indexers/test-all` | Test di tutti gli indexer | POST |
| `/indexers/run-all` | Aggiornamento di tutti gli indexer | POST |
| `/definitions` | Definizioni e catalogo | — |
| `/definitions/new` | Nuova definizione YAML | — |
| `/definitions/example-html` | Definizione dimostrativa | Usata da QA e documentazione |
| `/definitions/{id}` | Dettaglio definizione | `id` è uno slug, non un numero |
| `/applications` | App \*arr e client di download | Ancore `#clients` |
| `/applications/new` | Nuova applicazione | — |
| `/applications/{id}/edit` | Modifica applicazione | — |
| `/import` | Import Prowlarr/Jackett | Anteprima, non applica |
| `/activity?tab=runs` | Storico aggiornamenti da `indexer_history` | — |
| `/activity?tab=logs` | Log runtime | — |
| `/runs` | Vecchio ingresso Attività | 302 verso `/activity?tab=runs` |
| `/runs/{id}` | Dettaglio di un aggiornamento | Resta fuori dalle tab |
| `/logs` | Vecchio ingresso Attività | 302 verso `/activity?tab=logs` |
| `/logs/download` | Download del log | File, non pagina |
| `/rules` | Regole/blocklist | Gruppo **Sistema** |
| `/settings` | Impostazioni a sezioni | `?section=general|security|network|updates|rules|backup|maintenance|advanced` |
| `/download-clients/new` | Aggiungi client di download | Form compatto |
| `/download-clients/{id}/edit` | Modifica client | Password mai nell'HTML |
| `/setup` | Primo avvio in 4 passi | Saltabile e riapribile |
| `/maintenance` | Report, backup, restore | Gruppo **Sistema** |
| `/maintenance/backup` | Download backup immediato | File, non pagina |
| `/maintenance/backups/{name}` | Download di un backup | File, non pagina |
| `/healthz` | Health JSON | Fuori autenticazione UI |
| `/metrics` | Metriche Prometheus | Fuori autenticazione UI |
| `/api?t=caps` | Capacità Torznab | `caps` non richiede API key |
| `/newznab/api?t=caps` | Alias Newznab | Stesso handler di `/api` |
| `/torznab/api?t=caps` | Alias Torznab | Stesso handler di `/api` |
| `/dl/{id}.torrent` | Download Torznab | Escluso dallo smoke (file) |
| `/static/app.css` | Foglio di stile | Asset, escluso dai link |
| `/static/app.js` | Script UI | Asset, escluso dai link |
| `/sw.js` | Service worker | Asset, escluso dai link |
| `/api/v2/...` | API amministrativa | Fuori UI, autenticata con API key |

### Route mantenute per compatibilità

Non vanno rimosse senza un redirect:

- `/sources` e ogni percorso sotto `/sources/` — 302 verso `/indexers`.
- `/indexers?tab=legacy` — 302 verso `/indexers`.
- `/runs` e `/logs` — 302 verso le tab corrispondenti di Attività.
- `/runs/{id}` e `/logs/download` — dettagli e file che non hanno una tab.
- `/api`, `/newznab/api`, `/torznab/api`, `/dl/` — contratti Torznab/Newznab:
  non sono UI e non cambiano con il redesign.
- `/healthz`, `/metrics`, `/sw.js`, `/static/` — integrazioni operative.

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

# Trasforma i warning in errori (utile in CI)
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
- Warning: `<html lang="…">` mancante, skip link mancante, voci di navigazione
  ambigue, link che rispondono `4xx` non critici. Con `UI_SMOKE_STRICT=1` i
  warning valgono come errori.
- Non serve `jq`: bastano `curl`, `grep`, `sed`, `awk`, `sort` e `uniq`.
- Esce `1` se ci sono errori hard (o warning in strict), `0` altrimenti.

### Output

```
ok      /releases 200
ok      /indexers 200
ok      /sources 302
WARN    <html lang="it"> missing on 19/19 HTML pages (e.g. /)
...
==> summary
routes:   26 checked: 26 ok, 0 failed, 0 pending
links:    659 checked: 659 ok, 0 failed
```

## Punti confusi che il test intercetta

1. **Voci di navigazione duplicate**: stessa etichetta verso destinazioni
   diverse, oppure stesso percorso con etichette diverse (warning).
2. **Link interni rotti** dopo una rinomina: un `href` o `action` che risponde
   `404`/`5xx` su una pagina HTML viene segnalato con il percorso esatto.
3. **Vecchie route sparite**: `/runs`, `/logs` e `/sources` devono continuare a
   funzionare o reindirizzare; un `404` secco è un errore hard.
4. **Lingua sbagliata**: `lang="en"` residuo o `<html>` senza `lang="it"` è un
   warning (errore in strict).
5. **Skip link mancante**: l'ancora “salta al contenuto” (`#main` o `#content`)
   deve restare in tutte le pagine.
6. **Form POST scambiati per link**: un `405` su un `action` è corretto; un `405`
   su un `href` è un warning perché il collegamento non può funzionare.
7. **Asset e download**: `/dl/`, `/static/*`, `/sw.js` e i backup sono esclusi
   dai link per non confondere file e pagine; i backup sono verificati solo
   come `GET` di file.
8. **Autenticazione**: se la UI richiede Basic auth e l'URL non contiene le
   credenziali, la matrice fallisce con `401`; non è un link rotto ma un test
   mal configurato.
9. **Query string**: i link vengono normalizzati al percorso, quindi
   `/releases?query=test` non viene testato due volte; le tab di `/activity`
   sono distinte nella navigazione perché lì la query conta.

## Stato al 23 ottobre 2026

- Indexer: una sola pagina `/indexers`, senza tab.
- Redirect: `/sources*` e `/indexers?tab=legacy` rispondono 302.
- Attività: run e log nelle due tab, storico da `indexer_history`.
- Skip link: presente in tutte le pagine.
