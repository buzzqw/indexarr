# indexarr 2 — Visione e architettura

**Un indexer che parte con il 100% di ciò che serve e cresce di continuo**

Data: 23 ottobre 2026
Stato: documento di direzione tecnica (sostituisce la prima analisi di evoluzione)
Ambito: obiettivi, parità con Jackett/Prowlarr, architettura, formato delle
definizioni, catalogo nativo, roadmap

---

## 1. Obiettivo

indexarr 2 è **il nuovo indexer**: sostituisce Jackett e Prowlarr e ne supera i
limiti. Il progetto è indexarr stesso, che evolve; non nasce un repository
separato.

Tre principi guidano tutto il resto:

1. **Parità al 100% al giorno uno.** La 1.0 esce solo quando tutto ciò che un
   utente di Jackett o Prowlarr usa davvero funziona. La parità è una
   checklist verificabile (§3), non un'impressione.
2. **Le nostre funzioni si innestano di continuo.** Il nucleo espone contratti
   stabili; ogni funzione nuova è un modulo che si aggancia a uno stadio della
   pipeline senza toccare il resto (§5).
3. **Cardigann è il passato.** indexarr non lo usa. Tutte le definizioni
   sono riscritte in un formato nativo più robusto, con API e connettori di
   famiglia prima dello scraping (§6, §7).

Contratto verso l'esterno: **compatibilità 100% con le \*arr**, per torrent e
Usenet. Torznab e Newznab possono crescere ma non si rompono mai.

---

## 2. Decisioni

### 2.1 Prese

| Tema | Decisione |
|---|---|
| Progetto | indexarr è il nuovo progetto; evolve, non si riscrive da zero |
| Compatibilità \*arr | 100%: tutte le applicazioni che Prowlarr sincronizza |
| Cardigann | Non usato: tutte le definizioni sono riscritte nel formato nativo |
| Protocolli | Torrent **e Usenet** (Newznab) nella 1.0, come Prowlarr |
| Distribuzione | Un solo binario Go, SQLite, nessun runtime esterno obbligatorio |

### 2.2 Aperte

| Tema | Stato | Raccomandazione |
|---|---|---|
| **Licenza** | Libera, da scegliere | Codice e catalogo sono interamente nostri grazie alla scrittura pulita (§6.6); resta da scegliere quale licenza libera |

---

## 3. Checklist di parità (contratto della 1.0)

Ogni voce ha un criterio di verifica. La 1.0 esce quando sono tutte verdi.

### 3.1 Distribuzione verso le \*arr

| Voce | Verifica |
|---|---|
| Torznab `caps` aggregato e per indexer | Golden test sull'XML; le \*arr importano le caps senza errori |
| `search`, `tvsearch`, `movie`, `music`, `book` | Test per modalità con tutti i parametri |
| Parametri id: `imdbid`, `tvdbid`, `tmdbid`, `tvmazeid`, `rid`, `season`, `ep`, `artist`, `album`, `author`, `title` | Mappati verso le definizioni che li supportano; ignorati in modo pulito dove non supportati |
| `limit`/`offset`, `cat`, `extended`, attributi Torznab | Golden test |
| Download tramite proxy (`/dl/...`) con API key; nessun passkey esposto | Test: il link restituito non contiene mai URL del tracker |
| Endpoint aggregato, per indexer e **aggregato filtrato** (tag, lingua, tipo, stato), come i filtri di Jackett | Test di routing |
| Newznab per indexer Usenet: stesse modalità e parametri, `t=get` che restituisce `.nzb` tramite proxy | Golden test; le \*arr aggiungono l'indexer come Newznab |
| URL in stile Jackett (`/api/v2.0/indexers/{id}/results/torznab/api`) | Alias per chi migra da Jackett senza toccare le \*arr |
| Errori Torznab standard (codici 100–900) | Le \*arr mostrano l'errore giusto (API key errata, limite raggiunto, indexer giù) |

### 3.2 App sync (sostituzione di Prowlarr)

| Voce | Verifica |
|---|---|
| Applicazioni: Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3, LazyLibrarian | Test di integrazione contro le versioni supportate di ciascuna |
| Livelli di sync: disabilitato, solo aggiunta/rimozione, sync completo | Test sul ciclo completo |
| Indexer sincronizzati con il protocollo giusto: Torznab per i torrent, Newznab per Usenet | Test con indexer misti |
| Campi sincronizzati: nome, URL, API key, categorie, priorità, tag, seeders minimi, seed ratio, seed time, season pack seed time, abilitazione RSS/ricerca automatica/interattiva | Confronto con ciò che Prowlarr scrive nell'app |
| Riconciliazione: rileva modifiche manuali nell'app e indexer orfani | Test con stato divergente |

Nota: Readarr è stato ritirato dal team Servarr, ma ha ancora installazioni
attive; resta in lista finché esistono utenti.

### 3.3 Gestione degli indexer

| Voce | Verifica |
|---|---|
| Aggiunta da catalogo con form generato dai `settings` della definizione | Test UI su definizioni di ciascun tipo di login |
| Test indexer (login, ricerca, download) con esito strutturato | Esito per fase, non solo "ok/errore" |
| Proxy per indexer: HTTP, SOCKS4/5, FlareSolverr | Test con proxy simulati |
| Limiti per indexer: query/giorno, grab/giorno | Le \*arr ricevono l'errore Torznab corretto al superamento |
| Priorità, tag, abilitazione, redirect del download | Test |
| Storico query e grab, statistiche per indexer | Visibili in UI e API |
| Health: indexer falliti, disabilitati temporaneamente, motivo | Stato visibile e notificabile |
| Ricerca manuale dalla UI e invio ai client di download: qBittorrent e Transmission (esistenti), più Deluge e rTorrent; SABnzbd e NZBGet per Usenet | Test end-to-end con client simulato |
| Notifiche (health, grab, aggiornamenti) | Test sui canali supportati |
| Backup/restore di DB, configurazione e definizioni | Restore su istanza pulita |

### 3.4 Motore e catalogo

| Voce | Verifica |
|---|---|
| Motore: login di ogni tipo (form, CSRF, cookie, token, API key, Basic), flussi a più passi, paginazione, HTML/JSON/XML, categorie, errori del sito, download a più passaggi, mirror | Suite di test del motore con fixture per ogni funzione |
| Connettori di famiglia per i software di tracker più diffusi | Ogni famiglia testata su fixture di almeno due siti |
| Catalogo nativo che copre l'inventario prioritario (§7.3) | 100% dell'inventario prioritario `verificato` |
| Ponte verso Jackett/Prowlarr per i siti non ancora riscritti (§7.4) | Ogni indexer importato funziona, nativo o tramite ponte |
| Connettore Newznab generico con preset per gli indexer Usenet noti (come i preset di Prowlarr) | Caps importate dall'indexer; test su fixture Newznab |

La parità del catalogo si misura così: **100% dei siti dell'utente
funzionanti al giorno uno** (nativi o tramite ponte) e **quota nativa** in
crescita continua fino al 100%.

---

## 4. Oltre la parità: cosa supera Jackett e Prowlarr

Queste sono le funzioni che si innestano sopra il nucleo. Alcune esistono già
in indexarr.

| Funzione | Limite superato | Stato |
|---|---|---|
| Cache persistente SQLite + FTS5 | Prowlarr interroga sempre live; Jackett ha solo una cache breve | Esiste |
| Ricerca `cache+live` con budget e risultati parziali | Latenza e carico sugli indexer | Da fare |
| Provenance multi-indexer ("visto da 3 indexer") con scelta del link migliore | Duplicati e perdita di informazione | Da fare |
| Rate limit per host (token bucket) + circuit breaker + `Retry-After` | Comportamento eterogeneo tra definizioni | Da fare |
| Definizioni testate offline con fixture e record/replay | Test che richiedono il sito vivo | Da fare |
| Connettori di famiglia e API prima dello scraping (§6) | Definizioni fragili copiate sito per sito | Da fare |
| Definizioni tipizzate con fallback, mirror e health integrato (§6.4) | Definizioni che si rompono al primo cambio di layout | Da fare |
| Seeders aggiornati: TTL e refresh dei risultati restituiti dalla cache | Seeders vecchi che falsano le scelte delle \*arr | Da fare |
| Vault per credenziali e cookie | Credenziali in chiaro | Da fare |
| Metriche per indexer e per richiesta (latenza, 429, challenge) | Diagnosi difficile | Parziale |
| Footprint: ~30 MiB RSS a riposo oggi (vedi audit) | Runtime .NET | Esiste |

---

## 5. Architettura per aggiunte continue

### 5.1 Pipeline a stadi

Ogni ricerca o acquisizione attraversa la stessa pipeline:

```text
plan → request → fetch → extract → normalize → enrich → dedup → store → distribute
```

| Stadio | Responsabilità |
|---|---|
| `plan` | Sceglie gli indexer per capability, categoria, stato, budget |
| `request` | Costruisce la richiesta dalla definizione (template, input, paginazione) |
| `fetch` | HTTP con sessione, proxy, rate limit, challenge solver, limiti di dimensione |
| `extract` | Righe e campi da HTML/JSON/XML secondo la definizione |
| `normalize` | Titolo, size, date, categorie Torznab, infohash |
| `enrich` | Dati aggiuntivi (id esterni, qualità, lingua) |
| `dedup` | Chiave infohash/GUID; provenance |
| `store` | Cache SQLite + FTS5 |
| `distribute` | Torznab, API, UI, app sync, notifiche |

Ogni stadio ha un'interfaccia Go stabile e accetta **middleware** prima e dopo.
Una funzione nuova (per esempio un filtro di qualità o un arricchimento con
TMDB) è un modulo che registra un middleware: non tocca gli altri stadi.

### 5.2 Moduli

- Registrati a compile time (nessun plugin dinamico): un solo binario.
- Ogni modulo dichiara stadi, impostazioni, migrazioni del DB e feature flag.
- Un modulo spento non ha effetti, nemmeno sullo schema delle risposte
  Torznab.

```go
type Module interface {
    Name() string
    Migrations() []Migration
    Register(r *Registry) // middleware, endpoint, job, impostazioni
}
```

### 5.3 Connettori

Definizione YAML interpretata e connettore Go nativo espongono la stessa
interfaccia:

```go
type Connector interface {
    Capabilities() Capabilities
    Search(ctx context.Context, req SearchRequest) ([]Item, error)
    Latest(ctx context.Context, req LatestRequest) ([]Item, error)
    Download(ctx context.Context, item Item) (Payload, error)
    Test(ctx context.Context) (TestResult, error)
}
```

### 5.4 Tutto è versionato

| Elemento | Meccanismo |
|---|---|
| Schema del DB | Migrazioni numerate, testate su copie di DB reali |
| Formato delle definizioni | Catena di upgrader v0 → vN (§6) |
| API Torznab | Solo aggiunte compatibili |
| API amministrativa | `/api/v2/...` versionata |

Questa è la condizione che permette di aggiungere funzioni di continuo senza
rompere installazioni esistenti.

---

## 6. Formato nativo e connettori di famiglia

### 6.1 Perché non Cardigann

indexarr **non usa Cardigann**, né come formato interno né come formato di
ingresso. Tutte le definizioni vengono riscritte. Il motivo è funzionale:

- Cardigann nasce per lo scraping HTML: ogni cambio di layout rompe la
  definizione, anche quando il sito offre un'API stabile;
- i siti che usano lo stesso software (stesso codice, stesse API) hanno
  definizioni copiate e mantenute una per una;
- la logica è scritta in template testuali senza tipi: gli errori emergono
  solo a runtime, sul sito vero;
- non esistono test offline né un modo standard per dire "questa definizione
  funziona";
- flussi a più passi, paginazione, mirror e fallback sono gestiti in modo
  diverso da definizione a definizione.

### 6.2 Gerarchia delle fonti

Per ogni sito si usa la fonte più stabile disponibile, in quest'ordine:

| Priorità | Fonte | Perché |
|---|---|---|
| 1 | **API ufficiale** del sito (JSON, con API key o token) | Contratto stabile, dati strutturati, meno carico sul sito |
| 2 | **Feed standard** esposti dal sito (Torznab, Newznab, RSS) | Formato noto, già supportato |
| 3 | **Connettore di famiglia** (§6.3) | Un solo connettore per tutti i siti con lo stesso software |
| 4 | **Definizione** HTML/JSON nel formato nativo (§6.4) | Per i siti con software proprietario |
| 5 | **Connettore Go dedicato** | Per i casi che il formato non esprime |

Lo scraping HTML è l'ultima scelta, non la prima.

### 6.3 Connettori di famiglia

Molti tracker usano lo stesso software (per esempio UNIT3D o Gazelle e i suoi
derivati). Per ciascuna famiglia si scrive **un** connettore, che usa le API
del software quando esistono:

- aggiungere un sito della famiglia richiede solo URL, nome, categorie e le
  eventuali differenze dichiarate (`family: unit3d` + opzioni);
- una correzione al connettore ripara tutti i siti della famiglia;
- l'elenco delle famiglie da coprire esce dall'inventario dei siti (§7.3).

### 6.4 Formato delle definizioni

```yaml
schema: 1
meta:
  id: example-tracker
  name: Example Tracker
  language: it-IT
  type: private               # public | semi-private | private
  protocol: torrent           # torrent | usenet
  urls:                       # il primo è il principale, gli altri mirror
    - https://tracker.example/
    - https://tracker-mirror.example/

settings:
  - { name: username, type: text, required: true }
  - { name: password, type: password, required: true }

categories:                   # id del sito → categoria Torznab/Newznab
  - { site: "5",  standard: 5040, desc: "TV HD" }
  - { site: "12", standard: 2040, desc: "Film HD" }

capabilities:
  search: [q]
  tv-search: [q, season, ep, imdbid]
  movie-search: [q, imdbid]

auth:
  strategy: form              # none | api_key | basic | form | cookie | token
  steps:                      # flusso esplicito, passo per passo
    - get: /login.php
      extract:
        csrf: { selector: 'input[name="csrf_token"]', attr: value }
    - post: /login.php
      form:
        username: "{{ settings.username }}"
        password: "{{ settings.password }}"
        csrf_token: "{{ csrf }}"
  success: { selector: 'a[href="/logout.php"]' }
  failure:
    - { selector: "div.error", message: text }
  refresh_after: 6h

search:
  request:
    get: /torrents.php
    params:
      search: "{{ query.keywords }}"
      cat: "{{ categories | join(',') }}"
      page: "{{ page }}"
  pagination: { type: page, start: 1, max_pages: 5, stop_when: empty_rows }
  response:
    type: html                # html | json | xml
    rows: { selector: "table.torrents > tbody > tr" }
    fields:
      title:    { selector: ["a.title", "td.name a"], filters: [trim] }   # fallback in ordine
      details:  { selector: "a.title", attr: href, type: url }
      download: { selector: "a.download", attr: href, type: url }
      category: { selector: "a.cat", attr: href, filters: [{ query_param: cat }], type: category }
      size:     { selector: "td.size", type: size }
      seeders:  { selector: "td.seeders", type: int }
      leechers: { selector: "td.leechers", type: int }
      date:     { selector: "td.date", type: date, layouts: ["02-01-2006 15:04"] }
    required: [title, download, size]

health:
  query: "test"               # ricerca usata dai controlli periodici
  min_rows: 1

tests:
  - name: search
    fixture: fixtures/example/search.html
    expect: { min_rows: 1, required: [title, download, size, category] }
  - name: login-ok
    fixture: fixtures/example/login-ok.html
    expect: { authenticated: true }
```

Cosa rende il formato più funzionante:

| Caratteristica | Effetto |
|---|---|
| **Campi tipizzati** (`size`, `date`, `int`, `url`, `category`) | Parsing e validazione centralizzati, niente filtri ripetuti in ogni definizione |
| **Selettori con fallback** | Un piccolo cambio di layout non rompe la definizione |
| **Mirror dichiarati** | Failover automatico tra domini |
| **Flussi a passi espliciti** (`steps`) | Login, CSRF, token e download a più passaggi leggibili e testabili |
| **Campi obbligatori** (`required`) | Una riga incompleta viene scartata e segnalata, non pubblicata come rotta |
| **`health` integrato** | Ogni definizione sa come controllarsi da sola |
| **Test obbligatori** | Nessuna definizione entra nel catalogo senza fixture e test |
| **JSON Schema pubblicato** | Autocompletamento e validazione negli editor e nello studio |
| **Template con funzioni dichiarate** | Nessun codice arbitrario; errori rilevati alla validazione |

Ciò che il formato non esprime va in un connettore Go (§5.3), non in
estensioni ad hoc del formato.

### 6.5 Evoluzione del formato

Il formato è versionato (`schema: 1`, `2`, …). Per ogni nuova versione si
scrive un upgrader vN → vN+1; il motore esegue solo la versione corrente e le
definizioni vecchie vengono aggiornate automaticamente al caricamento e nel
catalogo.

### 6.6 Scrittura pulita

Le definizioni e i connettori si scrivono **osservando il sito** (pagine,
risposte, documentazione delle API), non copiando file o codice di Jackett e
Prowlarr. Ogni definizione registra autore, data e le fixture da cui è stata
scritta. Questo garantisce che il catalogo sia interamente nostro e
distribuibile con la licenza che scegliamo.

---

## 7. Catalogo

### 7.1 Stati

```text
bozza ──► verificata ──► stabile
              ▲             │
              └── degradata ◄┘  (health check fallito; torna verificata dopo la correzione)
```

| Stato | Significato |
|---|---|
| `bozza` | Scritta, test su fixture non ancora completi |
| `verificata` | Passa tutti i test su fixture |
| `stabile` | Verificata e con health check live riuscito di recente |
| `degradata` | Health check fallito: risultati parziali o errori |

La UI e l'API mostrano sempre lo stato.

### 7.2 Requisiti per entrare nel catalogo

- validazione dello schema senza errori;
- fixture per ricerca e, se privato, login riuscito e fallito;
- test con invarianti (`min_rows`, campi obbligatori);
- query di `health` definita;
- dati sensibili rimossi dalle fixture (passkey, cookie, nomi utente).

### 7.3 Cosa scrivere prima

Il catalogo si scrive in ordine di valore, non in ordine alfabetico:

1. **Inventario dei siti**: quelli configurati nelle istanze reali, quelli
   presenti nelle configurazioni importate da Jackett/Prowlarr (§10) e, con
   consenso, le statistiche anonime di quali siti vengono richiesti.
2. **Famiglie prima dei siti**: un connettore di famiglia copre molti siti con
   un solo lavoro.
3. **Siti più usati**, poi la coda lunga.

### 7.4 Il ponte: nessun sito perso durante la riscrittura

Riscrivere il catalogo richiede tempo. Perché l'utente abbia il 100% dei suoi
siti al giorno uno:

- i siti che non hanno ancora una definizione nativa continuano a funzionare
  tramite il Jackett o il Prowlarr dell'utente, usati come sorgente Torznab
  (supporto già esistente in indexarr);
- la UI mostra quali indexer passano dal ponte e avvisa quando è disponibile
  la definizione nativa, con migrazione in un clic;
- il ponte si svuota sito per sito; quando è vuoto, Jackett e Prowlarr si
  possono spegnere.

Il ponte è un meccanismo di transizione: indexarr non legge né esegue
definizioni Cardigann, interroga solo l'endpoint Torznab di un servizio già
installato dall'utente.

### 7.5 Verifica continua

- **CI**: tutte le definizioni testate sulle fixture a ogni modifica del motore
  o del catalogo; una regressione blocca la build.
- **Canary**: health check periodici sui siti pubblici dall'infrastruttura del
  progetto.
- **Rete di salute (opt-in)**: le istanze segnalano in modo anonimo l'esito
  degli health check per definizione e versione; un sito rotto si scopre in
  minuti.
- **Distribuzione**: il catalogo si aggiorna come pacchetto di dati firmato,
  senza aggiornare il binario.

### 7.6 Manutenzione

- **Studio delle definizioni**: scrittura guidata dalla UI, selezionando i
  campi sulla pagina del sito, con anteprima e fixture generate.
- **Riparazione assistita**: quando un sito cambia layout, il motore confronta
  la pagina nuova con l'ultima fixture valida e propone i nuovi selettori.

---

## 8. Modello dati

Separazione tra definizione, istanza, segreti e stato. Il protocollo è un
campo di primo livello: release torrent e Usenet convivono nella stessa cache.
Per Usenet la chiave di deduplica è il GUID (non esiste infohash) e il payload
salvato è il `.nzb` invece del `.torrent`.

```sql
definitions (
  id TEXT PRIMARY KEY, schema_version INTEGER NOT NULL,
  family TEXT,                    -- connettore di famiglia, se usato
  state TEXT NOT NULL,            -- bozza | verificata | stabile | degradata
  origin TEXT NOT NULL,           -- builtin | catalog | local
  author TEXT, body TEXT NOT NULL, hash TEXT NOT NULL,
  updated_at INTEGER NOT NULL
)

indexers (
  id INTEGER PRIMARY KEY,
  definition_id TEXT NOT NULL REFERENCES definitions(id),
  protocol TEXT NOT NULL,         -- torrent | usenet
  slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1, priority INTEGER NOT NULL DEFAULT 25,
  config_json TEXT NOT NULL DEFAULT '{}',   -- impostazioni non segrete
  proxy_id INTEGER, tags TEXT NOT NULL DEFAULT '',
  query_limit INTEGER, grab_limit INTEGER,
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
)

indexer_secrets (indexer_id, key, nonce, ciphertext)      -- vault
indexer_state   (indexer_id, cookies_enc, last_login_at,
                 circuit, disabled_until, last_error, updated_at)
release_sources (release_id, indexer_id, first_seen_at, last_seen_at,
                 seeders, leechers, download_ref)          -- provenance
indexer_history (id, indexer_id, kind, query, status,
                 latency_ms, bytes, error, app, started_at)
applications    (id, kind, name, url, api_key_enc, sync_level,
                 last_sync_at, last_error)
app_indexers    (application_id, indexer_id, remote_id, synced_hash)
proxies         (id, kind, url, secrets_enc)
```

`download_ref` è un riferimento interno: l'URL reale del tracker (con passkey)
resta cifrato e non esce mai dalle API.

---

## 9. Sicurezza

- **Passkey e API key degli indexer**: i link verso le \*arr passano sempre
  dal proxy `/dl/` (`.torrent` e `.nzb`); nessuna risposta API contiene URL
  del tracker o l'API key dell'indexer Newznab.
- **Vault**: AES-GCM con chiave esterna (`INDEXARR_MASTER_KEY` o file
  separato). Protegge backup e copie del DB, **non** chi ha accesso completo
  all'host: va detto chiaramente nella documentazione.
- **Definizioni = dati**: nessun codice eseguibile scaricato. Il catalogo
  remoto è firmato.
- **SSRF**: solo `http`/`https`; loopback, link-local, metadata cloud e reti
  private bloccati salvo opt-in per indexer locale; redirect rivalidati;
  limiti di dimensione e timeout.
- **CAPTCHA**: nessuna risoluzione automatica; l'indexer va in stato `blocked`
  e si chiede intervento umano.
- **API e UI**: API key multiple e revocabili, CSRF, Basic auth opzionale,
  audit log delle modifiche a indexer e credenziali.

indexarr indicizza e non scarica contenuti; le definizioni rispettano i limiti
di richiesta dei siti.

---

## 10. Da indexarr a indexarr 2

Il codice attuale è la base: è in produzione e misurato (vedi
`docs/AUDIT-2026-10-23.md`).

| Pacchetto | Destino |
|---|---|
| `internal/store` | Resta; nuove tabelle (§8); `sources` migrata a `indexers` |
| `internal/torznabapi` | Resta come stadio `distribute`; routing per indexer, filtri, alias Jackett, errori Torznab completi |
| `internal/ingest` | Diventa scheduler + pipeline (§5.1) |
| `internal/feed` | Parser HTML attuali → prime definizioni native (primi casi di test del motore); client Torznab/RSS → connettori nativi; client Jackett/Prowlarr → ponte (§7.4) |
| `internal/flaresolverr` | Diventa un'implementazione di `ChallengeSolver` dentro `fetch` |
| `internal/downloadclient` | Resta; aggiunge Deluge, rTorrent, SABnzbd, NZBGet |
| `internal/backup`, `internal/web` | Restano ed estendono |
| nuovi | `internal/definition` (formato, validazione, upgrader), `internal/engine` (motore), `internal/family` (connettori di famiglia), `internal/appsync`, `internal/vault` |

Migrazione dei dati esistenti:

1. ogni `source` diventa un `indexer` con una definizione nativa o un
   connettore (Torznab, RSS, oppure ponte Jackett/Prowlarr);
2. credenziali spostate nel vault;
3. `release_sources` popolata dalle release esistenti;
4. API key e URL Torznab attuali continuano a funzionare;
5. test di migrazione su copia del DB reale, con rollback.

Jackett e Prowlarr restano utilizzabili come sorgenti durante la transizione.

---

## 11. Dipendenze

Oggi `go.mod` ha una sola dipendenza diretta (SQLite). Il motore richiede
almeno:

- selettori CSS su `x/net/html` (es. cascadia/goquery);
- parser YAML;
- selettori JSON (percorsi semplici, senza un linguaggio di query completo).

Regola: poche dipendenze, mature, senza CGO; ogni nuova dipendenza va
motivata.

---

## 12. Roadmap

Le fasi sono milestone, non date. La 1.0 coincide con la checklist del §3
tutta verde.

| Milestone | Contenuto | Uscita |
|---|---|---|
| **M0 — Fondamenta** | Modello dati §8, migrazione da `sources`, vault, pipeline a stadi, interfacce `Connector` e `Module` | indexarr funziona come oggi sul nuovo modello |
| **M1 — Motore** | Formato v1, motore HTML/JSON/XML, sessioni, login, paginazione, rate limit per host, circuit breaker | I tre parser HTML attuali sostituiti da definizioni |
| **M2 — Catalogo** | Inventario dei siti, connettori di famiglia, prime definizioni, studio delle definizioni, CI del catalogo, ponte | Siti propri tutti funzionanti (nativi o ponte) |
| **Alpha interna** | Inventario prioritario riscritto e verificato | Uso quotidiano su istanza propria |
| **M3 — Distribuzione** | Endpoint per indexer e filtrati, alias Jackett, errori Torznab, limiti query/grab, proxy | \*arr collegate senza Jackett/Prowlarr |
| **M4 — App sync** | Tutte le applicazioni del §3.2, livelli di sync, riconciliazione | Prowlarr rimovibile |
| **M5 — Copertura** | Famiglie e siti della coda lunga, rete di salute opt-in, riparazione assistita | Quota nativa in crescita, ponte in svuotamento |
| **M6 — Usenet** | Connettore Newznab e preset, endpoint Newznab, proxy `.nzb`, SABnzbd/NZBGet, sync Newznab verso le \*arr | Indexer Usenet gestiti come in Prowlarr |
| **Beta** | Health check opt-in; documentazione per chi scrive definizioni | Checklist §3 quasi completa |
| **1.0** | Checklist §3 verde | Rilascio pubblico |
| **Dopo la 1.0** | Funzioni del §4 e nuove, come moduli | Continuo |

Le funzioni del §4 già presenti (cache, FTS5) restano attive in tutte le
milestone.

---

## 13. Rischi

| Rischio | Mitigazione |
|---|---|
| Volume di definizioni da riscrivere | Famiglie prima dei siti, studio delle definizioni, contributi della community, ponte per i siti mancanti |
| Siti senza API e con layout instabile | Selettori con fallback, health check, riparazione assistita |
| Contaminazione di licenza | Regole di scrittura pulita (§6.6) e tracciamento dell'origine di ogni definizione |
| Cambi di API tra versioni delle \*arr | Test di integrazione per versione, matrice di compatibilità |
| Manutenzione da parte di una sola persona | Automazione (CI del catalogo, test), moduli indipendenti, contributi della community allo studio |
| Tempo fino alla 1.0 | Alpha interna presto; la 1.0 non si anticipa riducendo la checklist |

---

## 14. Cosa non fare

- Non usare Cardigann e non copiare definizioni o codice di Jackett/Prowlarr.
- Non scegliere lo scraping HTML quando il sito offre un'API o un feed.
- Non eseguire codice scaricato dalla rete.
- Non incorporare Chromium; solo servizi esterni opzionali per le challenge.
- Non esporre passkey o URL dei tracker nelle API.
- Non rompere Torznab, le API key o gli URL esistenti.
- Non trasformare indexarr in un client di download (né torrent né Usenet).

---

## Appendice — Glossario

- **Cardigann**: formato YAML delle definizioni di Jackett e Prowlarr;
  indexarr non lo usa.
- **Connettore di famiglia**: connettore unico per tutti i siti che usano lo
  stesso software di tracker.
- **Ponte**: uso temporaneo del Jackett/Prowlarr dell'utente come sorgente
  Torznab per i siti non ancora riscritti.
- **Torznab**: estensione del protocollo Newznab per i torrent; è il contratto
  con le \*arr.
- **Newznab**: protocollo di ricerca per indexer Usenet.
- **Provenance**: traccia di quali indexer hanno visto una release e quando.
- **Circuit breaker**: sospende temporaneamente un indexer che fallisce.
- **Token bucket**: algoritmo di rate limiting.
- **SSRF**: server-side request forgery; far fetchare a indexarr URL interni.

---

## 15. Diario di implementazione — 23 ottobre 2026

Questa sezione è il punto di ingresso per chi continua il lavoro. La visione
resta questo documento; i contratti Go, lo stato dettagliato e i prossimi passi
sono in [`docs/INDEXARR2-PLAN.md`](docs/INDEXARR2-PLAN.md). Il formato delle
definizioni è descritto in [`docs/DEFINITIONS.md`](docs/DEFINITIONS.md).

### 15.1 Cosa esiste oggi nel repository

| Package | Ruolo | Test |
|---|---|---|
| `internal/definition` | Formato nativo v1: parse YAML strict, validazione, `Fingerprint`, upgrader, JSON Schema incorporato | sì |
| `internal/engine` | Contratti `Connector`/`Module`, pipeline a 9 stadi, transport (rate limit, breaker, SSRF), sessioni, template, estrazione HTML/JSON/XML, `DefinitionConnector` | sì |
| `internal/family` | Connettori di famiglia UNIT3D, Newznab generico e Torznab generico, registrati nel registry | sì |
| `internal/vault` | AES-256-GCM, chiave da `INDEXARR_MASTER_KEY` o file separato `0600` | sì |
| `internal/store` | Tabelle §8, migrazioni numerate, backfill da `sources`, CRUD definizioni/indexer/segreti/stato/provenance/storico/app/proxy | sì |
| `internal/catalog` | Catalogo built-in incorporato (`catalog.yaml` + `definitions/*.yaml`), validato e sincronizzabile | sì |
| `internal/indexer` | Costruisce e mette in cache il connettore da definizione + config + segreti; esegue un indexer; salva release, provenance, storico e stato | sì |
| `internal/native` | Scheduler degli indexer nativi; salta il ponte legacy; worker limitati | sì |
| `internal/adminapi` | API `/api/v2`: definizioni, catalogo, indexer, applicazioni, segreti nel vault, test/search/run | sì |
| `internal/indexersvc` | Servizio delle pagine native: crea/aggiorna/cancella indexer, separa segreti e config pubblica | sì |
| `internal/syncjob` | Riconciliazione periodica con Sonarr/Radarr/Lidarr/Readarr/Whisparr; scrive `app_indexers` | sì |
| `internal/downloadproxy` | Risolve i `download_ref` cifrati e serve `.nzb`/`.torrent` su richiesta | sì |
| `internal/importers` | Piano di import da Prowlarr e Jackett (definizioni, indexer, applicazioni, proxy) | sì |
| `internal/legacyimport` | Anteprima, applicazione e rollback della migrazione delle `sources` legacy verso Torznab nativo | sì |
| `internal/simmock`, `cmd/simmock`, `sim/` | Simulazione end-to-end: finti Torznab/Newznab, Prowlarr, Sonarr, SABnzbd, NZBGet, Deluge, rTorrent e tracker HTML; stack locale e Docker | sì |
| `internal/torznabapi` | Distribuzione Torznab: alias Jackett, routing per indexer, filtri aggregati, errori 100–900 | sì |
| `internal/appsync` | Client Servarr (Sonarr/Radarr/Lidarr/Readarr/Whisparr), Mylar3/LazyLibrarian per il test, reconcile puro | sì |
| `internal/deftool`, `cmd/indexarr-def` | CLI `validate`, `schema`, `fmt`, `fingerprint`, `check-fixtures` | sì |
| `cmd/indexarr`, `internal/web` | Wiring nel binario, mount di `/api/v2`, health, QA | sì |

`indexarr` continua a funzionare come prima: il percorso legacy (`sources` →
`ingest` → Torznab) non è stato rimosso. Le novità si aggiungono.

### 15.2 Modello dati e migrazione

- `schema_migrations` registra le migrazioni numerate; la prima è
  `indexarr2 foundation`.
- Nuove tabelle: `definitions`, `indexers`, `indexer_secrets`,
  `indexer_state`, `release_sources`, `indexer_history`, `applications`,
  `app_indexers`, `proxies`.
- Ogni riga di `sources` viene collegata una volta sola a un indexer con
  `legacy_source_id` e a una definizione `legacy-<id>` con
  `connector: torznab|rss`. I segreti **non** vengono copiati.
- Il backfill è idempotente. Se un indexer ponte viene cancellato, non viene
  ricreato al riavvio: la definizione legacy resta come marcatore.
- Su una copia del database reale: 37 `sources` → 37 `indexers`, 2.175
  release preservate, `PRAGMA integrity_check` = `ok`.

### 15.3 Flusso nel binario

All'avvio `cmd/indexarr`:

1. apre il vault (`INDEXARR_MASTER_KEY` oppure `<db>.key`, creato `0600`);
2. sincronizza il catalogo built-in nel DB;
3. costruisce un transport condiviso, il registry delle famiglie e
   `indexer.Manager`;
4. avvia `native.Runner`: esegue subito gli indexer nativi e poi ogni
   `default_interval`; gli indexer ponte (`torznab`/`rss`) vengono saltati e
   restano allo scheduler legacy;
5. avvia `syncjob`: dopo 2 secondi riconcilia le applicazioni \*arr e poi ogni
   15 minuti;
6. monta `/api/v2` fuori dall'autenticazione Basic della UI, con auth a API key;
7. monta le pagine native `/definitions` e `/indexers`, con form generati dai
   `settings`, test per fase e run manuale; `/applications` e `/import`
   coprono il sync \*arr e l'anteprima di import;
8. applica i limiti query/grab per indexer e usa il proxy HTTP/SOCKS
   configurato per l'indexer;
9. avvia il server HTTP e lo scheduler legacy.

Il proxy `/dl/{id}.torrent` e `/dl/{id}.nzb` serve i payload salvati; se il
payload non c'è, `downloadproxy` apre il `download_ref` cifrato e lo scarica
dal connettore dell'indexer, poi lo salva in cache.

Un indexer nativo creato da `/api/v2` viene eseguito al giro successivo (o
subito con `POST /api/v2/indexers/{id}/run`). Non serve riavviare.

### 15.4 API amministrativa `/api/v2`

Autenticazione: `apikey` in query, `X-Api-Key` o `Authorization: Bearer`.
Nessun cookie, nessun CSRF. Errori JSON.

| Metodo | Percorso |
|---|---|
| GET | `/api/v2/health` |
| GET/POST | `/api/v2/definitions` |
| POST | `/api/v2/definitions/validate` |
| GET/PUT/DELETE | `/api/v2/definitions/{id}` |
| GET | `/api/v2/catalog` |
| POST | `/api/v2/catalog/sync` |
| GET/POST | `/api/v2/indexers` |
| GET/PUT/DELETE | `/api/v2/indexers/{id}` |
| POST | `/api/v2/indexers/{id}/test` |
| POST | `/api/v2/indexers/{id}/search` |
| POST | `/api/v2/indexers/{id}/run` |
| GET/POST | `/api/v2/applications` |
| GET/PUT/DELETE | `/api/v2/applications/{id}` |
| POST | `/api/v2/applications/{id}/sync` |
| POST | `/api/v2/applications/sync` |
| POST | `/api/v2/import/prowlarr` |
| POST | `/api/v2/import/jackett` |
| GET | `/api/v2/legacy/preview` |
| POST | `/api/v2/legacy/apply` |
| POST | `/api/v2/legacy/revert` |

Le risposte non contengono mai `api_key`, password, cookie o URL del tracker.
La ricerca restituisce solo titolo, GUID, dimensione, seeders/leechers,
categorie, data, infohash e magnet: `DownloadURL` e `DetailsURL` sono rimossi.

### 15.5 Invarianti da non rompere

- **Nessun Cardigann**: non si leggono né eseguono definizioni Jackett/Prowlarr.
  Il ponte usa solo il loro endpoint Torznab.
- **Nessun codice scaricato**: le definizioni sono dati; il catalogo remoto, in
  futuro, sarà firmato.
- **Nessun segreto nelle API**, nei log, negli errori o nelle fixture.
- **SSRF**: solo `http`/`https`; loopback, reti private, link-local e metadata
  cloud bloccati salvo `allow_private` esplicito per un indexer locale.
- **Torznab non si rompe**: alias, API key esistenti e URL continuano a
  funzionare; le aggiunte sono compatibili.
- **Il percorso legacy resta operativo** finché il ponte non è vuoto.
- **Le definizioni built-in sono `bozza`** finché non hanno fixture osservate
  da un sito reale e un health check live riuscito.
- **Un modulo spento non ha effetti**, nemmeno sullo schema delle risposte.

### 15.6 Verifiche eseguite

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./...
make def-validate          # 10 definizioni: 3 esempi + 7 built-in
./scripts/qa.sh            # HTTP, Torznab, /api/v2, vault create/delete
```

Risultato: tutto verde. Il QA reale crea e cancella un indexer Newznab via
`/api/v2`, verifica che il segreto non compaia nella risposta e che il vault
lo possa decifrare; crea e cancella un'applicazione Sonarr con sync
`disabled`; verifica le pagine `/definitions`, `/indexers`, `/newznab/api` e
`/api/v2/applications`. Il runner nativo, su una copia del DB reale, salta 37
indexer ponte su 37 e non tocca il percorso legacy.

Inoltre `./sim/run.sh` e `./sim/run-docker.sh` eseguono una simulazione
deterministica completa: definizione/indexer nativo, run, ricerca Torznab,
download `.torrent` e `.nzb`, sync verso un Sonarr finto e invio ai client
SABnzbd/NZBGet/Deluge/rTorrent. Lo stack Docker è stato costruito ed eseguito
sulle porte isolate 18484/19080, senza toccare il Jackett già presente.

### 15.7 Limiti noti e prossima ondata

Ancora da fare, in ordine di valore:

1. **Catalogo reale e copertura siti**: sostituire le definizioni `bozza` con
   definizioni `verificate` su fixture osservate e aumentare la copertura con
   connettori di famiglia e import delle definizioni Cardigann (vedi §15.9).
2. **App sync**: snapshot per il rollback in un clic, sync Mylar3/LazyLibrarian
   (oggi `ErrUnsupported`), filtri per tag.
3. **Import applicato**: la UI mostra il piano Prowlarr/Jackett ma non crea
   ancora definizioni/indexer; manca l'apply con anteprima e rollback.
4. **FlareSolverr per indexer** (i proxy HTTP/SOCKS sono fatti), stato
   `blocked` per CAPTCHA, firma del catalogo remoto, studio delle definizioni,
   riparazione assistita, rete di salute opt-in.
5. **Catalogo e stato**: health check per definizione, transizioni
   `verificata`/`degradata`, notifiche.
6. **Provenance**: il dato c'è e Torznab espone `indexers`/`provenance`;
   manca il job periodico che aggiorna i seeders con TTL usando
   `StaleProvenance`.

### 15.9 Copertura siti e Cardigann: stato e piano

**Risposta breve: non è stata avviata una riscrittura dei Cardigann.** Il
lavoro fatto finora costruisce le fondamenta per non dipendere da Cardigann e,
allo stesso tempo, per non perdere nessun sito durante la transizione.

Cosa esiste oggi:

- motore nativo v1 (HTML/JSON/XML, login, paginazione, tipi, fixture);
- connettori di famiglia `unit3d`, `newznab`, `torznab`, `gazelle`,
  `nexusphp`, `torrenttrader`, `xbtit`;
- **convertitore Cardigann locale** (`internal/cardigann` + comando
  `indexarr-def convert-cardigann`): legge le definizioni già installate
  dall'utente e produce definizioni native v1 per il sottoinsieme supportato,
  con warning espliciti per ciò che non è convertibile. Non è un runtime
  Cardigann e non distribuisce definizioni copiate;
- ponte legacy: indexarr interroga l'endpoint Torznab di Jackett/Prowlarr, che
  resta la copertura "giorno uno";
- `legacyimport`: converte le sorgenti Torznab dirette in indexer nativi, con
  anteprima, apply e revert;
- `importers`: legge Prowlarr/Jackett e produce un piano (non applicato);
- catalogo built-in: 7 definizioni `bozza` (esempio HTML, UNIT3D, Newznab,
  Gazelle, NexusPHP, TorrentTrader, Xbtit) + esempi. **Non sono ancora
  centinaia**: i connettori di famiglia coprono molti siti, ma mancano le
  definizioni/pacchetti che li istanziano.

Cosa serve per arrivare a centinaia di siti, in ordine di leva:

1. **Connettori di famiglia** (una correzione copre molti siti):
   - Gazelle e derivati (fatto, `bozza`);
   - NexusPHP (fatto, `bozza`);
   - TorrentTrader/TBDev (fatto, `bozza`);
   - Xbtit/BtiTracker (fatto, `bozza`);
   - restano AVISTAZ/AsianDVDClub e altri software minori.
   Ogni famiglia va verificata su almeno due siti/fixture reali prima di
   dichiararla `verificata`.
2. **Import Cardigann applicato**:
   - il convertitore esiste ed è usabile da CLI;
   - manca il collegamento a UI/API che, dopo l'anteprima, crei davvero
     definizioni e indexer e lasci il resto al ponte;
   - nessuna distribuzione di definizioni copiate: si convertono localmente i
     dati dell'utente, con tracciamento di autore/origine;
   - il runtime di indexarr continua a non eseguire Cardigann.
3. **Pacchetti di definizioni** con inventario, fixture, stato
   `bozza/verificata/degradata`, test CI e quote per famiglia.
4. **Ponte come rete di sicurezza**: finché una definizione nativa non è
   verificata, il sito continua a funzionare via Jackett/Prowlarr.
5. **Studio delle definizioni** nella UI: selettori guidati, anteprima,
   fixture generate, condivisione con il catalogo.
6. **Inventario**: misurare quali siti sono realmente usati (sources locali,
   import Prowlarr/Jackett, statistiche anonime opt-in) e ordinare il lavoro
   per copertura reale, non per numero di definizioni.

Priorità: prima le famiglie che coprono i siti realmente presenti nelle
istanze degli utenti (inventario da `sources`, import Prowlarr/Jackett e
statistiche anonime), poi la coda lunga. Non ha senso scrivere 500 definizioni
singole se 20 famiglie coprono l'80% dell'uso reale.

### 15.8 Come lavorare (per il prossimo agente)

- Leggi prima `docs/INDEXARR2-PLAN.md` (contratti e stato) e
  `docs/DEFINITIONS.md` (formato). Questo documento è la visione, non il
  contratto dei package.
- Un package per volta, dipendenze verso il basso, nessun ciclo:
  `definition` non importa nulla di indexarr; `engine` non importa `store`,
  `web` o `torznabapi`; `indexer` è il livello di collegamento.
- Ogni modifica al motore o al catalogo passa da fixture e test offline. Niente
  rete nei test: `httptest`, fixture su disco, clock iniettabile.
- `gofmt`, `go vet`, `go test` del package e `go test -race ./...` prima di
  dichiarare finito. `make def-validate` e `./scripts/qa.sh` per la verifica
  end-to-end.
- Non copiare codice o definizioni di Jackett/Prowlarr. Le definizioni si
  scrivono osservando il sito e registrando autore, data e fixture.
- Non fare commit di chiavi, database, backup o fixture con dati personali.
