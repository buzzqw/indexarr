# QA e test

indexarr ha una suite di test Go per ogni pacchetto e uno smoke test end-to-end
che avvia il binario reale su un database temporaneo. L'obiettivo è coprire sia
la correttezza delle funzioni interne sia il comportamento HTTP osservabile da
Sonarr/Radarr e dal browser.

## Comandi

```bash
make test   # go test ./...
make vet    # go vet ./...
make qa     # vet + test + build + smoke test HTTP su un'istanza temporanea
make e2e    # browser headless, campo per campo, su DB e porta temporanei
```

`make qa` esegue `scripts/qa.sh`: compila il binario, lo avvia su
`127.0.0.1` con una porta casuale e un database in una directory temporanea,
verifica le pagine principali, il foglio di stile mobile, il manifest PWA,
il service worker e la risposta Torznab `t=caps`. Al termine arresta il
processo e cancella i file temporanei.

Per usare una porta fissa:

```bash
INDEXARR_QA_PORT=18080 make qa
```

`make e2e` esegue `scripts/ui-e2e.py`: compila il binario, apre un database
temporaneo su una porta casuale, avvia Chrome headless con il Chrome DevTools
Protocol e un piccolo servizio locale che imita FlareSolverr, qBittorrent e
Sonarr. Cambia ogni campo dei moduli (Impostazioni, modifica indexer, catalogo,
App, client di download, definizioni, API key, primo avvio), salva, ricarica e
rilegge il valore sia dalla pagina sia dal database temporaneo. Copre anche
`<details>` chiusi, checkbox «rimuovi valore», campi segreti lasciati vuoti,
tendine di categoria/URL, `data-confirm`, doppio clic e conferma vicino al
bottone. Senza Chrome stampa `SKIP` ed esce 0; con
`UI_E2E_REQUIRE_CHROME=1` l'assenza del browser è un errore. `make qa` lo
esegue quando trova Chrome.

## Livelli di verifica

1. **Statico** — `go vet ./...` e compilazione di tutti i pacchetti.
2. **Unit/integration** — `go test ./...` sui pacchetti `backup`, `config`,
   `downloadclient`, `feed`, `flaresolverr`, `ingest`, `logbuffer`, `notify`,
   `store`, `torznabapi`, `web`.
3. **HTTP integration** — i test di `internal/web` usano `httptest` e
   esercitano handler, CSRF, cookie, redirect e rendering dei template.
4. **Smoke end-to-end** — `scripts/qa.sh` usa il binario compilato, quindi
   verifica anche gli asset embedded e l'avvio reale del server.

## Cosa è coperto

- **Feed e sorgenti**: parsing RSS/Atom, Torznab, MirCrew,
  listing HTML (ExtTo, TorrentGalaxy, Corsaro), scoperta feed, sanitizzazione,
  redazione dei segreti nei log e mapping categorie.
- **Ingestion**: deduplica, aggiornamento delle release, backoff, auto-disable,
  scheduler, trigger manuale e gestione degli errori.
- **Store**: schema e migrazioni, CRUD sorgenti, API key, ricerche e filtri,
  indice FTS5 (trigger, rebuild su database esistenti, fallback per termini
  corti e piano di query), blocklist, ricerche salvate, backup, vacuum,
  integrità, deduplica, azioni di manutenzione e report del database.
- **Torznab**: `caps`, `search`, `tvsearch`, `movie`, `get`, paginazione,
  filtri, autenticazione e rate limit.
- **Web UI**: dashboard, release, dettaglio, sorgenti, quick add, export/import JSON degli indexer,
  impostazioni, log, manutenzione, CSRF, autenticazione Basic opzionale,
  PWA e asset statici.
- **Log**: filtro per livello e per origine (`indexarr`, indexer, RSS,
  notifiche), conteggio dei problemi, contesto `source`/`kind`/`host` sugli
  errori delle sorgenti e download filtrato.
- **Manutenzione**: check di integrità e foreign key, analyze, checkpoint WAL,
  vacuum, reindex, pulizia per età/sorgente, release senza link, release
  nascoste, cronologia run, contatori API e ricalcolo dei contatori sorgente.

## Isolamento

I test non usano la rete e non toccano il database di produzione:

- ogni test apre un database in `t.TempDir()`;
- le sorgenti remote sono simulate con `httptest.Server` o fixture;
- lo smoke test usa una directory `mktemp` e termina il processo nel `trap`;
- nessun test richiede servizi esterni o Docker; il solo `make e2e` usa un
  browser headless, e salta se non è installato.

## CI

`.github/workflows/qa.yml` esegue `make qa` su ogni push e pull request. La
workflow usa la versione Go dichiarata in `go.mod`.

## Checklist manuale per la UI

I test automatici verificano che il CSS mobile sia servito, ma la resa visiva
va controllata a mano quando si modificano i template:

- larghezza 390 px: navigazione scorrevole, nessun overflow orizzontale;
- tabelle trasformate in card con etichette leggibili;
- checkbox di selezione e “Select all” raggiungibili;
- input a 16 px e bottoni con area di tocco adeguata;
- pagine `/`, `/releases`, `/sources`, `/runs`, `/settings`, `/maintenance`,
  `/logs` e dettaglio release;
- tema chiaro e scuro;
- installazione PWA e caricamento offline degli asset statici.

## Aggiungere un test

- Per la logica del database: `internal/store/*_test.go`, usando
  `newTestStore(t)`.
- Per un handler o una pagina: `internal/web/*_test.go`, usando
  `newTestEnv(t)`, `csrfFrom`, `postForm` e `get`.
- Per un parser o un client: fixture locali e `httptest.Server`.

Ogni nuova utility di manutenzione deve avere almeno un test nello store e uno
nell'handler web che ne verifichi redirect ed effetto sul database.

## Simulazione end-to-end

Oltre ai test di package, `sim/` contiene una simulazione deterministica con
servizi finti (Torznab/Newznab, Sonarr, SABnzbd, NZBGet, Deluge,
rTorrent e un tracker HTML) e asserzioni con exit code non zero. Si esegue in
locale con `./sim/run.sh` oppure in Docker con `./sim/run-docker.sh`, sulle
porte isolate 18484 e 19080. Dettagli, asserzioni e reset in
[`sim/README.md`](../sim/README.md).
