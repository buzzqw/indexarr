# indexarr 2 — Analisi di evoluzione

**Da client di Jackett/Prowlarr a indexer autonomo, moderno e compatibile**

Data: 23 ottobre 2026  
Stato: documento di analisi e direzione tecnica  
Ambito: architettura, motore delle definizioni, ricerca, distribuzione, sicurezza, migrazione

---

## 1. Sintesi esecutiva

indexarr oggi è un **aggregatore con cache**: sa parlare Torznab, RSS, alcuni
listing HTML e le API di Jackett/Prowlarr, ma per la maggior parte dei tracker
dipende da un traduttore esterno. La sua forza è il modello "un solo binario,
SQLite, cache interrogabile, scheduling, UI e Torznab"; il suo limite è che la
conoscenza dei singoli siti vive fuori da indexarr.

L'evoluzione proposta — **indexarr 2** — è trasformarlo in una piattaforma di
indicizzazione autonoma:

- **sostituisce Jackett e Prowlarr** per la gestione e l'interrogazione degli
  indexer torrent;
- porta dentro il motore le **definizioni dei siti** e i connettori nativi;
- continua a **cercare, salvare e distribuire** le release, con cache SQLite e
  FTS5;
- mantiene la compatibilità Torznab con Sonarr/Radarr/Lidarr e aggiunge
  compatibilità Jackett/Prowlarr dove serve;
- supera i limiti tipici di Jackett con un motore più moderno: definizioni
  tipizzate, sessioni e cookie gestiti, rate limit adattivi, circuit breaker,
  estrazione HTML/JSON/XML, supporto browser opzionale, test riproducibili.

> Quello che nel messaggio è stato chiamato “cardigan” è **Cardigann**, il
> formato YAML di definizioni usato da Jackett e Prowlarr. La compatibilità con
> Cardigann è il ponte per ereditare l'ecosistema esistente; il passo successivo
> è un formato più espressivo e verificabile.

La raccomandazione è **non riscrivere indexarr da zero**. Conviene evolvere
l'attuale base Go/SQLite introducendo un livello `indexer` sopra l'attuale
`source`, mantenendo in funzione il percorso Torznab e la cache. Un MVP
credibile richiede circa 8–12 settimane di lavoro esperto; la parità funzionale
con Prowlarr (app sync, Usenet, catalogo ampio) è un percorso di 4–6 mesi.

---

## 2. Dove siamo: stato attuale e limiti

### 2.1 Cosa fa già bene indexarr

- **Scheduler e worker** con intervalli per sorgente, backoff dopo errore,
  auto-disable, trigger manuale, coda limitata.
- **Cache SQLite** con deduplica per infohash/GUID/titolo, magnet, BLOB
  `.torrent`, cronologia run, blocklist, preferiti/nascosti.
- **Ricerca FTS5** con tokenizer trigram, trigger di sincronizzazione e fallback
  `LIKE` per i termini corti.
- **Torznab server** completo: caps, search, tvsearch, movie, get, download,
  paginazione, filtri, API key multiple, rate limit.
- **UI SSR** con dashboard, sorgenti, release, regole, log, impostazioni,
  manutenzione, backup e PWA.
- **Operatività**: singolo binario Go, SQLite senza CGO, backup, metriche,
  notifiche, FlareSolverr, invio ai client torrent.
- **Compatibilità già acquisita**: Jackett, Prowlarr, MirCrew, Torznab diretto,
  RSS/Atom, ExtTo, TorrentGalaxy, Corsaro.

### 2.2 Cosa manca per sostituire davvero Jackett e Prowlarr

| Area | Stato attuale | Gap per indexarr 2 |
|---|---|---|
| Definizioni per sito | Parser compilati per 3 siti HTML + client per protocolli | Motore generico di definizioni e connettori nativi |
| Login e sessioni | HTTP Basic, API key, qualche cookie non persistito | Form login, CSRF, cookie jar, token, refresh, logout |
| Estrazione dati | RSS/Torznab + parser HTML dedicati | CSS selector, XPath, JSONPath, regex, template, filtri tipizzati |
| Paginazione | Dipende dall'endpoint | Paginazione dichiarativa (page/offset/cursor) con stop condition |
| Categorie | Mapping numerico per sorgente | Mapping per definizione, caps per indexer, categorie aggregate |
| Rate limit | Backoff globale per sorgente | Token bucket per host, Retry-After, circuit breaker, jitter |
| Sfide anti-bot | FlareSolverr per RSS/Torznab | Rilevamento challenge, browser opzionale, stato “blocked” esplicito |
| Ricerca | Cache; live search solo su Torznab e solo cache miss | Fan-out pianificato, cache+live, budget e risultati parziali |
| Distribuzione | Un solo endpoint Torznab aggregato | Endpoint per indexer, alias Jackett, app sync verso *arr |
| Catalogo | Sorgenti configurate a mano | Catalogo definizioni, versioni, import/export, aggiornamenti |
| Credenziali | API key/password in chiaro nel DB | Vault cifrato, chiave esterna, rotazione, redazione |
| Diagnostica | Log e run history | Metriche per indexer, latenza, 429, cache hit, stato sessione |
| Test | Test Go per parser e HTTP | Fixture, record/replay, golden Torznab, test di definizione |

### 2.3 Limiti di Jackett che indexarr 2 può superare

Jackett è maturo e ha un catalogo enorme, ma:

- molte definizioni sono HTML scraping fragile; ogni cambio layout rompe il
  parser;
- il runtime .NET e la gestione per-indexer hanno un costo di memoria e startup
  non trascurabile;
- l'aggregazione è pensata per un uso “da *arr”, non per una cache persistente
  con ricerca full-text;
- rate limit, login e challenge sono gestiti in modo eterogeneo tra definizioni;
- non esiste un percorso nativo per API JSON/GraphQL o per siti con auth
  particolare, se non scrivendo una definizione o un indexer C#;
- testare una definizione richiede spesso un'istanza reale e un sito raggiungibile.

### 2.4 Limiti di Prowlarr che indexarr 2 può superare

Prowlarr è il riferimento per la gestione centralizzata e l'app sync, ma:

- interroga gli indexer **live**: non è una cache di release;
- non è pensato per servire una ricerca full-text locale a bassa latenza;
- dipende da FlareSolverr per le challenge;
- l'app sync è prezioso ma è un livello separato dal motore di ricerca;
- il modello di definizioni è lo stesso Cardigann, con gli stessi compromessi.

La tesi di indexarr 2 è: **unire il catalogo/definizioni di Jackett, l'app sync
di Prowlarr e la cache/ricerca di indexarr**, in un solo processo Go.

---

## 3. Che cosa significa “sostituire” Jackett e Prowlarr

“Sostituire” non significa solo accettare un URL Torznab in più. Significa
coprire quattro livelli:

1. **Conoscenza del sito**: sapere come cercare, autenticarsi, impaginare,
   estrarre campi, risolvere il download.
2. **Esecuzione**: fare richieste in modo efficiente, educato e resiliente,
   con sessioni, rate limit, proxy e gestione degli errori.
3. **Memoria**: normalizzare, deduplicare, conservare e cercare le release.
4. **Distribuzione**: esporre Torznab, compatibilità Jackett/Prowlarr e app
   sync verso Sonarr/Radarr/Lidarr.

### 3.1 Matrice funzionale obiettivo

| Funzione | Jackett | Prowlarr | indexarr 2 (obiettivo) |
|---|---|---|---|
| Catalogo definizioni | Ampio (Cardigann) | Ampio (Cardigann esteso) | Import Cardigann + definizioni native |
| Ricerca live | Sì | Sì | Sì, con cache e budget |
| Cache release persistente | Limitata | No | Sì, SQLite + FTS5 |
| Login/sessioni | Per definizione | Per indexer | Motore sessioni unico |
| App sync *arr | No | Sì | Sì (fase 3) |
| Torznab aggregato | Sì | Sì | Sì |
| Endpoint per indexer | Sì | Sì | Sì |
| API compatibile Jackett | — | Parziale | Da valutare, utile per migrazione |
| Interfaccia web | Sì | Sì | Sì, già esistente |
| Metriche/storico | Base | Buono | Per indexer, latenza, errori, cache |
| Test definizioni | Limitato | Limitato | Fixture + golden + CI |
| Runtime | .NET | .NET | Go, binario statico |
| Credenziali | File/config | DB | Vault cifrato |

### 3.2 Compatibilità da non rompere

- **Sonarr/Radarr/Lidarr**: continuano a usare Torznab; l'endpoint aggregato
  attuale deve restare stabile.
- **API key**: quelle esistenti devono continuare a funzionare.
- **Download**: `/dl/{id}.torrent` e `t=get` (id numerico, GUID, infohash).
- **Caps**: categorie e parametri supportati.
- **Import**: le sorgenti attuali (`kind`, `manager`, URL, credenziali) devono
  essere migrabili senza perdere release o cronologia.
- **Convivenza**: durante la transizione indexarr deve poter ancora usare
  Jackett/Prowlarr come sorgenti, per poi dichiararli opzionali.

---

## 4. Architettura proposta

### 4.1 Principi

1. **Un solo binario**: niente plugin Go caricati dinamicamente; definizioni
   dati + motore compilato.
2. **Definizioni per il lungo tail, connettori nativi per i siti importanti**:
   non tutto deve essere YAML.
3. **Sicurezza per default**: nessun codice arbitrario nelle definizioni;
   Starlark/WASM solo in sandbox.
4. **Osservabilità**: ogni richiesta e ogni indexer devono essere misurabili.
5. **Compatibilità prima delle mode**: Torznab e *arr restano il contratto.
6. **Crescita incrementale**: la cache e il server Torznab attuali non si
   riscrivono; si aggiunge un livello sotto.

### 4.2 Diagramma dei componenti

```text
                         ┌──────────────────────────────┐
                         │            UI / API          │
                         │  dashboard, indexers, test   │
                         └──────────────┬───────────────┘
                                        │
        ┌───────────────────────────────┼────────────────────────────────┐
        │                               │                                │
┌───────▼────────┐            ┌─────────▼─────────┐            ┌─────────▼─────────┐
│  Distribution  │            │      Search       │            │    Operations     │
│ Torznab        │            │ planner, fan-out  │            │ scheduler, backup │
│ Jackett compat │            │ merge, ranking    │            │ metrics, logs     │
│ app sync       │            │ cache+live        │            │ rate limits       │
└───────┬────────┘            └─────────┬─────────┘            └─────────┬─────────┘
        │                               │                                │
        │                    ┌──────────▼──────────┐                     │
        │                    │        Cache        │                     │
        │                    │ SQLite + FTS5       │                     │
        │                    │ releases, sources   │                     │
        │                    └──────────┬──────────┘                     │
        │                               │                                │
        │                    ┌──────────▼──────────┐                     │
        │                    │   Connector layer   │                     │
        │                    │ HTTP, auth, cookie  │◄────────────────────┘
        │                    │ pagination, extract │
        │                    └──────────┬──────────┘
        │                               │
        │                    ┌──────────▼──────────┐
        │                    │ Definition engine   │
        │                    │ schema, validation  │
        │                    │ Cardigann importer  │
        │                    │ native connectors   │
        │                    └─────────────────────┘
        │
        └────────────────► Sonarr / Radarr / Lidarr
```

### 4.3 Nuovo modello di indexer

L'attuale `store.Source` è pensato per un endpoint generico:

```text
kind        torznab | rss
manager     auto | jackett | prowlarr | mircrew | torznab
url, api_key, username, password
categories, query, interval
```

indexarr 2 deve separare **definizione**, **configurazione** e **stato**:

```text
Definition   → come si parla con il sito (nome, capabilities, auth, request, extract)
Indexer      → istanza configurata (URL/credenziali, categorie, intervallo, enable)
IndexerState → cookie, token, ultimo login, circuit breaker, statistiche
Release      → risultato normalizzato, con provenance multi-indexer
```

Questo permette:

- la stessa definizione con più istanze (es. due account/URL);
- l'aggiornamento della definizione senza toccare le credenziali;
- l'import/export di una configurazione senza segreti;
- la migrazione dall'attuale `sources` senza perdere release.

---

## 5. Motore delle definizioni

### 5.1 Perché non solo Cardigann

Cardigann è un ottimo punto di partenza, ma è nato per HTML scraping e ha un
linguaggio di trasformazione limitato. indexarr 2 deve supportare:

- **HTML** con CSS selector (e, dove serve, XPath);
- **JSON** con JSONPath o selettori tipizzati;
- **XML/RSS/Torznab**;
- **API proprietarie** con header, firma, token, POST;
- **download resolution**: dalla riga al dettaglio al magnet/`.torrent`;
- **paginazione** e stop condition;
- **categorie** con mapping per sito;
- **filtri** di normalizzazione: trim, lower, replace, regex capture, date, size,
  entity decode, URL resolve, coalesce, default;
- **test** dichiarativi con fixture.

La compatibilità Cardigann resta un **importer**, non il formato interno
definitivo. Le definizioni importate vengono validate e, se usano costrutti non
supportati, producono un report invece di fallire in silenzio.

| Concetto Cardigann | Equivalente indexarr 2 |
|---|---|
| `id`, `name`, `description` | metadati della definizione |
| `type`, `language` | metadati e default |
| `caps` | `capabilities` + mapping categorie |
| `login` | `auth` con strategie esplicite |
| `search.paths` | `request.search` / `request.latest` |
| `search.rows.selector` | `response.rows.selector` |
| `search.fields` | `response.fields` + filtri |
| `search.inputs` | `request.params` |
| `download` | `download` + `resolve` |
| `ratio`, `limits` | `limits`, rate limit e circuit breaker |

L'importer deve produrre anche un **compatibility report**: campi mappati,
campi ignorati, filtri non supportati, stima di copertura e test mancanti.

### 5.2 Schema di definizione (bozza)

```yaml
schema: 2
id: example-tracker
name: Example Tracker
description: Esempio di definizione indexarr 2
language: it-IT
type: private                 # public | semi-private | private
base_url: https://tracker.example
capabilities: [search, tvsearch, movie, latest]
categories:
  tv: [5000, 5040]
  movies: [2000, 2040]
  music: [3000]
  books: [7000]

auth:
  type: form                  # none | api_key | basic | form | cookie | oauth
  login:
    path: /login.php
    method: POST
    fields:
      username: "{{ .username }}"
      password: "{{ .password }}"
      csrf_token: "{{ .csrf }}"
  csrf:
    selector: 'input[name="csrf_token"]'
    attr: value
  success:
    selector: 'a[href="/logout.php"]'
  session:
    cookie_jar: true
    refresh_after: 6h

request:
  search:
    path: /torrents.php
    method: GET
    params:
      search: "{{ .query }}"
      cat: "{{ .categories }}"
      page: "{{ .page }}"
    headers:
      Accept-Language: it-IT,it;q=0.9
  latest:
    path: /torrents.php
    params:
      search: ""
      page: "{{ .page }}"

pagination:
  type: page                  # none | page | offset | cursor
  start: 0
  step: 1
  max_pages: 10
  stop_when: empty_rows

response:
  type: html                  # html | json | xml | rss
  rows:
    selector: "table.torrents > tbody > tr"
  fields:
    title:
      selector: "a.title"
      text: true
      filters: [trim, html_unescape]
    details:
      selector: "a.title"
      attr: href
      filters: [url_resolve]
    size:
      selector: "td.size"
      text: true
      parse: size
    seeders:
      selector: "td.seeders"
      text: true
      parse: int
    leechers:
      selector: "td.leechers"
      text: true
      parse: int
    published:
      selector: "td.date"
      text: true
      parse: date
      layouts: ["02-01-2006 15:04", "2006-01-02"]
    category:
      selector: "td.category"
      text: true
      map: categories
    download:
      selector: "a.download"
      attr: href
    magnet:
      selector: 'a[href^="magnet:"]'
      attr: href
    infohash:
      from: magnet
      parse: infohash

download:
  resolve: details
  timeout: 30s
  max_bytes: 8MiB
  extract:
    magnet: 'a[href^="magnet:"]::attr(href)'
    torrent: 'a.download::attr(href)'

limits:
  requests_per_minute: 10
  max_concurrent: 1
  retry:
    attempts: 2
    backoff: exponential
    retry_on: [429, 502, 503, 504]
  respect_retry_after: true

tests:
  - name: search
    fixture: fixtures/search.html
    query: "show"
    expect:
      count: 20
      first_title: "Example Show S01E01"
  - name: login
    fixture: fixtures/login.html
    expect:
      authenticated: true
```

### 5.3 Estrazione e trasformazioni

Tipi di campo da supportare:

- testo, attributo, HTML interno, valore assoluto, template;
- `from`: derivare da un altro campo (magnet → infohash, details → base URL);
- coalesce di più selettori;
- parse di size (`1.2 GB`), date (formati multipli), categoria, booleani;
- normalizzazione titolo, rimozione entità HTML, URL relativi;
- regex capture numerate;
- mapping categorie per sito.

Per i casi davvero complessi (firma HMAC, token a più passi, payload JSON
dinamico) si può usare **Starlark** in sandbox, con accesso solo a input
dichiarati e nessuna rete/filesystem. In alternativa, un connettore Go nativo
compilato nel binario. La regola è: **nessun codice arbitrario scaricato dalla
rete**.

### 5.4 Connettori nativi

Le definizioni non bastano per siti con:

- API JSON/GraphQL non standard;
- login OAuth o token refresh;
- più passaggi (login → CSRF → ricerca → dettaglio → download);
- rate limit molto aggressivi o anti-bot;
- formati di risposta proprietari.

Per questi si scrive un connettore Go nel repository, testato con fixture. Il
connettore espone la stessa interfaccia della definizione:

```go
type Connector interface {
    Capabilities() Capabilities
    Search(ctx context.Context, req SearchRequest) ([]Item, error)
    Latest(ctx context.Context, req LatestRequest) ([]Item, error)
    Resolve(ctx context.Context, item Item) (Item, error)
    Test(ctx context.Context) (TestResult, error)
}
```

La differenza è solo l'implementazione: YAML interpretato o Go compilato.

### 5.5 Catalogo e versioning

- Definizioni built-in versionate nel repository, con hash e changelog.
- Import da file/local folder/URL firmato (opzionale).
- `schema_version` e migrazione automatica delle definizioni.
- Validazione all'import: errori bloccanti e warning separati.
- Dry-run: “questa definizione è compatibile al 70%, mancano X/Y”.
- Backup delle definizioni e rollback all'ultima versione funzionante.

### 5.6 Test harness delle definizioni

Ogni definizione dovrebbe poter essere testata senza rete:

```bash
indexarr indexer test \
  --definition definitions/example.yml \
  --fixture testdata/example-search.html \
  --query "show" \
  --expect-count 20
```

Il test harness deve coprire: login, ricerca, parsing, paginazione, download,
mapping categorie, errori HTTP, rate limit. Le fixture possono essere registrate
una volta e rigiocate in CI (record/replay), con dati sensibili rimossi.

---

## 6. Runtime dei connettori

### 6.1 HTTP e sessioni

Un client per indexer, non un client globale condiviso in modo indiscriminato:

- cookie jar persistente e cifrato;
- User-Agent, Accept-Language, Referer configurabili;
- proxy per indexer;
- TLS e redirect controllati;
- timeout di connessione e di risposta separati;
- limite dimensione risposta (es. 32 MiB feed, 8 MiB torrent);
- decompressione e charset;
- gestione 429 con `Retry-After`;
- niente retry su POST di login o su download non idempotenti.

### 6.2 Autenticazione

Strategie minime:

- API key in query/header;
- HTTP Basic;
- form login con CSRF;
- cookie/token preesistente;
- OAuth2 (opzionale, per siti che lo offrono);
- passkey/2FA: richiede interazione umana, non automatizzabile in modo sicuro.

Lo stato di sessione va conservato per indexer, con scadenza e refresh. Un login
fallito non deve martellare il sito: si passa a `blocked`/`degraded` e si
riprova con backoff.

### 6.3 Rate limit e circuit breaker

- Token bucket **per host**, non solo per indexer: due definizioni sullo stesso
  host devono condividere il budget.
- Limite globale di concorrenza e limite per indexer.
- Circuit breaker: `closed → open → half-open` con soglie di errori/timeout.
- Backoff esponenziale con jitter.
- Rispetto di `Retry-After`.
- Metriche per 429, 403, challenge, timeout, latenza.

### 6.4 Cloudflare, browser e anti-bot

Tre livelli:

1. **HTTP diretto** con header realistici e cookie.
2. **FlareSolverr** (già presente) per challenge Cloudflare.
3. **Browser opzionale** (CDP/Playwright esterno) per JS pesante.

indexarr non deve incorporare Chromium nel binario: sarebbe enorme e fragile.
Meglio un servizio browser esterno opzionale, con allowlist e timeout. Non si
automatizza la risoluzione di CAPTCHA; si segnala il sito come `blocked` e si
richiede intervento umano.

---

## 7. Ricerca e cache

### 7.1 Modalità di ricerca

| Modalità | Uso | Comportamento |
|---|---|---|
| `cache` | UI e *arr di default | Cerca solo in SQLite/FTS5, latenza bassa |
| `live` | “Cerca ora” / Torznab miss | Interroga gli indexer, salva, restituisce |
| `cache+live` | Configurabile per *arr | Cache subito; se pochi risultati, fan-out live con budget |
| `latest` | Scheduler | Acquisisce le ultime release per indexer |

La modalità `cache+live` è il compromesso più utile: risposta veloce quando la
cache basta, aggiornamento mirato quando serve.

### 7.2 Pianificazione del fan-out

Per una ricerca:

1. parse della query (termini, esclusioni, regex, stagione/episodio, id esterni);
2. selezione degli indexer per capability, categoria, stato, rate budget;
3. fan-out concorrente con limite globale e per host;
4. timeout per indexer e deadline complessiva;
5. normalizzazione e deduplica;
6. merge dei risultati e ranking;
7. scrittura in cache;
8. risposta con eventuali warning parziali.

Non tutti gli indexer devono essere interrogati per ogni ricerca: un planner
basato su capability/categorie/qualità riduce latenza e carico.

### 7.3 Deduplica e provenance

La chiave primaria resta l'infohash; in assenza, GUID/URL/titolo. Per la
provenance multi-indexer serve una tabella `release_sources`:

- quale indexer ha visto la release;
- quando l'ha vista la prima/ultima volta;
- seeders/leechers per indexer;
- link di download specifici;
- qualità del dato.

Questo permette di mostrare “visto da 3 indexer” e di scegliere il link
migliore, senza duplicare le release.

### 7.4 Ranking

Il ranking deve restare prevedibile per Torznab:

- ordinamento richiesto dal client (`published_at`, `seeders`, `size`, `title`);
- rilevanza testuale per la UI;
- qualità dell'indexer e freschezza come tie-break;
- mai mescolare risultati non pertinenti solo per riempire la pagina.

### 7.5 Cache e scala

SQLite + FTS5 è adeguato per decine/centinaia di migliaia di release. Oltre:

- valutare partizionamento per anno o per categoria;
- archiviare i BLOB `.torrent` fuori dal DB (filesystem content-addressed) e
  tenere nel DB solo il riferimento;
- compressione dei magnet/titoli;
- job di retention e vacuum;
- metriche su dimensione DB, WAL, freelist, latenza query.

Non serve un database esterno per il target “self-hosted personale/medio”. Un
adapter PostgreSQL può essere valutato solo se nasce un requisito multi-utente o
multi-nodo.

---

## 8. Distribuzione e compatibilità

### 8.1 Superfici API

| Endpoint | Scopo |
|---|---|
| `GET /api` e `/torznab/api` | Torznab aggregato attuale (compatibile *arr) |
| `GET /indexers/{slug}/api` | Torznab del singolo indexer |
| `GET /api/v2.0/indexers` | Elenco compatibile Jackett |
| `GET /api/v2.0/indexers/{id}/results/torznab/api` | Ricerca compatibile Jackett |
| `GET /api/v2.0/indexers/all/results/torznab/api` | Aggregato compatibile Jackett |
| `GET /api/v2/indexers` | API amministrativa indexarr 2 |
| `GET /api/v2/definitions` | Catalogo definizioni |
| `POST /api/v2/indexers/{id}/test` | Test con risultato strutturato |

La compatibilità Jackett è utile per migrare client e strumenti che oggi
puntano a Jackett; non deve diventare il modello interno.

### 8.2 App sync verso Sonarr/Radarr/Lidarr

Per sostituire Prowlarr serve un livello “applications”:

- registrare un'app (URL, API key cifrata, tipo);
- creare/aggiornare/rimuovere un indexer Torznab nell'app;
- sincronizzare categorie e caps;
- gestire conflitti e rollback;
- mostrare stato e ultimo sync.

Questo è un progetto nel progetto: l'API delle *arr è ben documentata ma cambia
tra versioni. Va affrontato dopo che il motore di ricerca e gli endpoint per
indexer sono stabili.

### 8.3 Compatibilità Torznab

Mantenere:

- `caps` con categorie e parametri;
- `search`, `tvsearch`, `movie`, `music`, `book`;
- `get` per id numerico, GUID e infohash;
- download `.torrent` con API key;
- `newznab:response` e attributi Torznab;
- API key in query e `X-Api-Key`;
- rate limit per chiave.

Aggiungere:

- caps per singolo indexer;
- `limit`/`offset` coerenti con il fan-out;
- risultati parziali con warning strutturati;
- header di rate limit (`X-RateLimit-*`, `Retry-After`).

### 8.4 Newznab/Usenet

Fuori scope per indexarr 2 nella prima fase. Le release Usenet non hanno
`.torrent` e richiedono un modello dati e client diversi. Se in futuro si
vorrà sostituire Prowlarr anche su Usenet, sarà un modulo separato.

---

## 9. Dati e persistenza

### 9.1 Schema proposto (aggiunte)

```sql
-- Definizioni (built-in e importate)
definitions (
  id            TEXT PRIMARY KEY,
  schema_version INTEGER NOT NULL,
  name          TEXT NOT NULL,
  version       TEXT NOT NULL,
  source        TEXT NOT NULL,       -- builtin | import | url
  hash          TEXT NOT NULL,
  yaml          TEXT NOT NULL,
  enabled       INTEGER NOT NULL DEFAULT 1,
  created_at    INTEGER NOT NULL
)

-- Istanza configurata
indexers (
  id            INTEGER PRIMARY KEY,
  definition_id TEXT NOT NULL REFERENCES definitions(id),
  slug          TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL,
  enabled       INTEGER NOT NULL DEFAULT 1,
  config_json   TEXT NOT NULL DEFAULT '{}',
  categories    TEXT NOT NULL DEFAULT '',
  interval_seconds INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
)

-- Segreti cifrati (non nel record indexer)
indexer_secrets (
  indexer_id    INTEGER NOT NULL REFERENCES indexers(id) ON DELETE CASCADE,
  key           TEXT NOT NULL,
  nonce         BLOB NOT NULL,
  ciphertext    BLOB NOT NULL,
  PRIMARY KEY (indexer_id, key)
)

-- Stato volatile/persistente della sessione
indexer_state (
  indexer_id    INTEGER PRIMARY KEY REFERENCES indexers(id) ON DELETE CASCADE,
  cookies       BLOB,
  last_login_at INTEGER NOT NULL DEFAULT 0,
  circuit       TEXT NOT NULL DEFAULT 'closed',
  last_error    TEXT NOT NULL DEFAULT '',
  updated_at    INTEGER NOT NULL
)

-- Provenance multi-indexer
release_sources (
  release_id    INTEGER NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  indexer_id    INTEGER NOT NULL REFERENCES indexers(id) ON DELETE CASCADE,
  first_seen_at INTEGER NOT NULL,
  last_seen_at  INTEGER NOT NULL,
  seeders       INTEGER NOT NULL DEFAULT -1,
  leechers      INTEGER NOT NULL DEFAULT -1,
  download_url  TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (release_id, indexer_id)
)

-- Statistiche per richiesta/indexer (retention)
indexer_requests (
  id            INTEGER PRIMARY KEY,
  indexer_id    INTEGER NOT NULL,
  kind          TEXT NOT NULL,       -- search | latest | login | download | test
  query         TEXT NOT NULL DEFAULT '',
  status        INTEGER NOT NULL,
  latency_ms    INTEGER NOT NULL,
  bytes         INTEGER NOT NULL,
  error         TEXT NOT NULL DEFAULT '',
  started_at    INTEGER NOT NULL
)

-- App sync
applications (
  id            INTEGER PRIMARY KEY,
  kind          TEXT NOT NULL,       -- sonarr | radarr | lidarr
  name          TEXT NOT NULL,
  url           TEXT NOT NULL,
  api_key_enc   BLOB NOT NULL,
  enabled       INTEGER NOT NULL DEFAULT 1,
  last_sync_at  INTEGER NOT NULL DEFAULT 0,
  last_error    TEXT NOT NULL DEFAULT ''
)
```

La tabella `releases` e `releases_fts` restano. `release_sources` aggiunge la
provenance; `sources` può essere migrata a `indexers` con una vista di
compatibilità durante la transizione.

### 9.2 Migrazione dallo schema attuale

1. creare `definitions` con le definizioni built-in e i parser nativi;
2. creare `indexers` da `sources`, mappando `kind`/`manager` su una definizione
   o su un connettore nativo;
3. copiare `api_key`/`username`/`password` nel vault cifrato;
4. mantenere `sources` come vista/tabella legacy per una release;
5. popolare `release_sources` con l'indexer corrente delle release esistenti;
6. scrivere test di migrazione da un DB reale (copia) e rollback.

---

### 9.3 Impatto sui pacchetti attuali

| Pacchetto | Evoluzione |
|---|---|
| `internal/feed` | Estrarre i parser puri; aggiungere `internal/indexer` per definizioni, connettori, sessioni, rate limit. `feed.Client` resta come adapter legacy verso Torznab/Jackett/Prowlarr durante la migrazione. |
| `internal/ingest` | `RunSource` diventa `RunIndexer`; lo scheduler usa il nuovo modello; il fan-out di ricerca si sposta in `internal/search`. |
| `internal/store` | Nuove tabelle `definitions`, `indexers`, `indexer_secrets`, `indexer_state`, `release_sources`, `indexer_requests`, `applications`; migrazione da `sources`; FTS5 resta. |
| `internal/torznabapi` | Routing per indexer, caps per indexer, alias Jackett; l'app sync va in `internal/appsync`. |
| `internal/web` | UI catalogo definizioni, editor indexer, test con fixture, stato sessioni/circuit breaker, metriche. |
| `internal/flaresolverr` | Astrazione `ChallengeSolver` con implementazioni FlareSolverr e, opzionale, browser CDP. |
| `internal/downloadclient` | Nessuna modifica sostanziale; resta il livello di invio ai client torrent. |
| `internal/backup` | Backup di DB + definizioni/config; restore con migrazione e rebuild FTS. |
| `cmd/indexarr` | Sottocomandi: `serve`, `indexer test`, `definition import`, `db migrate`, `db check`. |

## 10. Sicurezza

### 10.1 Credenziali

Oggi le credenziali delle sorgenti sono nel DB in chiaro. Per un servizio che
gestisce decine di account privati è un debito da chiudere:

- cifratura AES-GCM con chiave esterna (`INDEXARR_MASTER_KEY` o keyring);
- non scrivere mai segreti nei log (già fatto in parte);
- mascherare i campi nella UI e nell'export;
- rotazione e backup della chiave;
- import/export cifrato.

### 10.2 Definizioni non fidate

- Schema validato; niente codice arbitrario.
- Starlark in sandbox, senza rete/filesystem, con limiti di CPU/memoria.
- Firma opzionale delle definizioni importate.
- Dry-run e report prima dell'attivazione.

### 10.3 SSRF e URL non fidati

Le definizioni e i listing HTML possono contenere URL arbitrari. Regole:

- solo `http`/`https`;
- blocco di loopback, link-local, metadata cloud e reti private, salvo opt-in
  esplicito per un indexer locale;
- redirect limitati e rivalidati;
- limite dimensione e timeout;
- niente `file://`, `gopher://`, `ftp://`.

### 10.4 API e UI

- API key multiple, rate limit, revoca;
- Basic auth opzionale già presente;
- CSRF già presente;
- header di sicurezza;
- pprof solo opt-in e solo su localhost (già introdotto);
- audit log per modifiche a indexer e credenziali.

### 10.5 Aspetti legali/etici

indexarr non scarica contenuti e non aggira paywall/DRM. Le definizioni devono
rispettare i termini dei siti e i limiti di richiesta. L'utente resta
responsabile dell'uso. Il documento non propone bypass di CAPTCHA o accessi non
autorizzati.

---

## 11. Operatività e affidabilità

- **Scheduler** con jitter per non colpire tutti gli indexer allo stesso secondo.
- **Intervalli adattivi**: più frequenti per indexer che producono novità, più
  radi per quelli stabili; rispetto dei rate limit.
- **Circuit breaker** e stato `blocked` visibile in UI.
- **Metriche**: richieste, errori, 429, latenza p50/p95, cache hit, login,
  dimensione risposta, item nuovi.
- **Log strutturati**: `component`, `indexer`, `request_id`, `stage`; buffer
  limitato.
- **Backup** già presente; aggiungere backup delle definizioni/config.
- **Ripristino** testato con DB contenente FTS5 e definizioni.
- **Limiti risorse**: worker globali, semafori per host, cap risposta, pprof
  opt-in, soak test 24h.

### SLO indicativi

| Metrica | Obiettivo |
|---|---|
| Ricerca cache p95 | < 100 ms su 100k release |
| Fan-out live p95 (10 indexer) | < 8 s |
| Memoria a riposo | < 150 MiB |
| CPU a riposo | < 1% |
| Error rate per indexer | < 5% (esclusi blocchi esterni) |
| Recupero dopo 429 | rispetto `Retry-After`, nessun retry storm |
| Disponibilità | riavvio automatico, stato coerente |

---

## 12. Confronto sintetico

| Dimensione | Jackett | Prowlarr | indexarr 2 |
|---|---|---|---|
| Runtime | .NET | .NET | Go statico |
| Definizioni | Cardigann | Cardigann esteso | Schema v2 + import Cardigann |
| Cache release | Limitata | No | SQLite + FTS5 |
| Ricerca full-text | No | No | Sì |
| App sync | No | Sì | Sì (fase 3) |
| Endpoint per indexer | Sì | Sì | Sì |
| Compatibilità Torznab | Sì | Sì | Sì |
| Compatibilità Jackett API | — | Parziale | Sì (migrazione) |
| Sessioni/login | Per definizione | Per indexer | Motore unico |
| Rate limit per host | Variabile | Variabile | Token bucket + circuit breaker |
| Browser/JS | FlareSolverr | FlareSolverr | FlareSolverr + browser opzionale |
| Test definizioni | Limitato | Limitato | Fixture + golden + CI |
| Credenziali | Config | DB | Vault cifrato |
| Metriche | Base | Buone | Per indexer/richiesta |
| Single binary | Sì (.NET self-contained) | Sì (.NET) | Sì |

---

## 13. Roadmap consigliata

### Fase 0 — Fondamenta (2–3 settimane)

- schema definizioni v2 e validatore;
- motore di estrazione HTML/JSON/XML con CSS/JSONPath/regex;
- importer Cardigann per un sottoinsieme;
- 5–10 definizioni reali;
- test harness con fixture;
- interfaccia `Connector`.

**Exit criteria**: una definizione HTML e una JSON producono Item normalizzati
senza toccare il codice Go.

### Fase 1 — Connettori e sessioni (3–4 settimane)

- login form/CSRF/cookie jar;
- Basic/API key/OAuth (base);
- paginazione;
- rate limit per host;
- download resolution e cache;
- 20–30 definizioni, inclusi i siti oggi hardcoded;
- migrazione delle sorgenti attuali.

**Exit criteria**: i siti ExtTo/TorrentGalaxy/Corsaro e almeno 10 tracker
funzionano tramite definizione/connettore, con test.

### Fase 2 — Ricerca e distribuzione (3–4 settimane)

- planner e fan-out;
- cache+live;
- endpoint Torznab per indexer;
- provenance multi-indexer;
- metriche e stato;
- compatibilità Torznab con *arr.

**Exit criteria**: Sonarr/Radarr/Lidarr possono usare l'aggregato e i singoli
endpoint; latenza e errori misurati.

### Fase 3 — Sostituzione Prowlarr (2–3 settimane)

- app sync Sonarr/Radarr/Lidarr;
- UI applicazioni;
- compatibilità Jackett API;
- catalogo/import/export definizioni;
- vault credenziali.

**Exit criteria**: un'installazione può rimuovere Prowlarr e Jackett, usando
indexarr come unico provider Torznab, con sync automatico.

### Fase 4 — Rifinitura continua

- browser opzionale;
- rate limit adattivi;
- definizioni community e aggiornamenti;
- test di carico/soak 24h;
- metriche SLO;
- documentazione per chi scrive definizioni.

**Stima complessiva**: 8–12 settimane per un MVP usable; 4–6 mesi per una
sostituzione completa e affidabile di Prowlarr nei casi reali.

---

## 14. Rischi e decisioni aperte

| Rischio | Impatto | Mitigazione |
|---|---|---|
| Definizioni fragili | Ricerche rotte | Fixture, dry-run, stato degraded, aggiornamenti |
| Anti-bot/CAPTCHA | Siti inutilizzabili | FlareSolverr/browser, stato blocked, rispetto limiti |
| Credenziali | Sicurezza | Vault cifrato, redazione, audit |
| Complessità app sync | Scope | Fase separata, test con versioni *arr |
| Cardigann incompleto | Import parziale | Report di compatibilità, connettori nativi |
| SQLite oltre scala | Performance | Indici, FTS5, retention, adapter futuro |
| Codice nelle definizioni | Sicurezza | Sandbox Starlark/WASM, no rete |
| Licenze definizioni | Legale | Verifica licenza, import solo consentito |
| Manutenzione | Costo | Catalogo versionato, test automatici, priorità ai siti usati |

### Decisioni da prendere

1. **Ambito**: solo torrent o anche Usenet? (raccomandazione: torrent prima).
2. **App sync**: priorità alta o dopo il motore? (raccomandazione: dopo).
3. **Compatibilità Jackett API**: necessaria per i client o basta Torznab?
4. **Definizioni**: solo built-in o anche community/import da URL?
5. **Browser**: esterno opzionale o plugin incluso?
6. **Multi-utente**: single-user o autenticazione multi-account?
7. **Licenza del catalogo definizioni**: compatibilità con l'ecosistema.

---

### Cosa non fare

- Non incorporare Chromium nel binario: megabyte, superficie di attacco e
  manutenzione sproporzionati. Meglio un servizio browser opzionale.
- Non eseguire definizioni come codice arbitrario: niente script con accesso a
  rete/filesystem.
- Non promettere “tutti i siti di Jackett” al giorno uno: il catalogo va
  prioritizzato sui siti realmente usati.
- Non eliminare subito `sources`/Torznab attuali: la compatibilità è il valore
  principale durante la migrazione.
- Non trasformare indexarr in un client BitTorrent: il confine “indice, non
  download” va mantenuto.

## 15. Raccomandazione finale

La strada giusta non è “riscrivere Jackett in Go” né “copiare Prowlarr”. È
costruire un **motore di definizioni e connettori** che:

- importi Cardigann per non perdere il catalogo esistente;
- sia più espressivo e testabile del Cardigann classico;
- usi connettori nativi per i siti che contano;
- sfrutti la cache, FTS5 e il server Torznab già presenti;
- aggiunga sessioni, rate limit e observability moderni;
- esponga endpoint per indexer e, in una fase successiva, app sync.

Il primo traguardo concreto è **sostituire i tre parser HTML compilati con
definizioni**, poi importare 20–30 siti usati davvero, quindi aggiungere il
fan-out live e gli endpoint per indexer. Solo dopo ha senso promettere la
sostituzione completa di Prowlarr.

indexarr ha già il pezzo più difficile di un indexer moderno: la cache, la
deduplica, il server Torznab e un'operatività a 24 ore. indexarr 2 deve
aggiungere il pezzo che oggi manca — la **conoscenza dei siti** — senza perdere
quelle garanzie.

---

## Appendice A — Glossario

- **Cardigann**: formato YAML di definizioni per indexer, usato da Jackett e
  Prowlarr.
- **Torznab**: estensione Torznab/Newznab per i torrent; è il contratto con
  Sonarr/Radarr/Lidarr.
- **Caps**: descrizione delle capacità e categorie di un indexer.
- **Provenance**: traccia di quale indexer ha visto una release e quando.
- **Circuit breaker**: stato che sospende temporaneamente un indexer che sta
  fallendo.
- **Token bucket**: algoritmo di rate limiting.
- **Starlark**: linguaggio di configurazione sandboxato, adatto a trasformazioni
  complesse senza codice arbitrario.
- **SSRF**: server-side request forgery; rischio di far fetchare a indexarr URL
  interni.

## Appendice B — Domande guida per il prossimo passo

1. Quali sono i 10 siti che oggi funzionano peggio con Jackett?
2. Quali usano login, 2FA, Cloudflare o API proprietarie?
3. Quanti client *arr e quante istanze?
4. Quante release al giorno e quanta retention serve?
5. Si vuole mantenere l'import da Jackett/Prowlarr o solo esportare?
6. Qual è il budget di manutenzione per le definizioni?
7. Serve il supporto Usenet nel breve periodo?

Le risposte cambiano l'ordine delle fasi, non l'architettura di fondo.
