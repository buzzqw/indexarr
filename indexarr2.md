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
   famiglia prima dello scraping (§6, §7). Unica eccezione ammessa: un
   convertitore locale che produce **bozze di lavoro** per chi scrive le
   definizioni; le bozze non entrano mai nel catalogo (§2.1).

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
| Convertitore Cardigann | Solo strumento CLI di scrittura: produce bozze locali marcate `derived_from: cardigann`, che il catalogo rifiuta. Nessun import Cardigann da UI/API, nessun runtime Cardigann (`docs/NEXT-STEPS.md` WP0) |
| Protocolli | Torrent **e Usenet** (Newznab) nella 1.0, come Prowlarr |
| Distribuzione | Un solo binario Go, SQLite, nessun runtime esterno obbligatorio |

### 2.2 Aperte

| Tema | Stato | Raccomandazione |
|---|---|---|
| **Licenza** | Libera, da scegliere (EUPL-1.2, MIT o GPL-3.0) | Codice e catalogo sono interamente nostri grazie alla scrittura pulita (§6.6). Il repository oggi non ha un file `LICENSE`: va aggiunto prima di accettare contributi esterni. La EUPL-1.2 può includere codice MIT e, per la sua clausola di compatibilità, può essere distribuita sotto GPL-3.0; non vale il contrario |

---

## 3. Checklist di parità (contratto della 1.0)

Ogni voce ha un criterio di verifica. La 1.0 esce quando sono tutte verdi.
La colonna "Stato" è l'unica fonte dello stato di parità: chi chiude un work
package di `docs/NEXT-STEPS.md` la aggiorna qui.

### 3.1 Distribuzione verso le \*arr

| Voce | Verifica | Stato (23/10) |
|---|---|---|
| Torznab `caps` aggregato e per indexer | Golden test sull'XML; le \*arr importano le caps senza errori | fatto |
| `search`, `tvsearch`, `movie`, `music`, `book` | Test per modalità con tutti i parametri | fatto |
| Parametri id: `imdbid`, `tvdbid`, `tmdbid`, `tvmazeid`, `rid`, `season`, `ep`, `artist`, `album`, `author`, `title` | Mappati verso le definizioni che li supportano; ignorati in modo pulito dove non supportati | parziale |
| `limit`/`offset`, `cat`, `extended`, attributi Torznab | Golden test | fatto |
| Download tramite proxy (`/dl/...`) con API key; nessun passkey esposto | Test: il link restituito non contiene mai URL del tracker | fatto |
| Endpoint aggregato, per indexer e **aggregato filtrato** (tag, lingua, tipo, stato), come i filtri di Jackett | Test di routing | fatto |
| Newznab per indexer Usenet: stesse modalità e parametri, `t=get` che restituisce `.nzb` tramite proxy | Golden test; le \*arr aggiungono l'indexer come Newznab | fatto |
| URL in stile Jackett (`/api/v2.0/indexers/{id}/results/torznab/api`) | Alias per chi migra da Jackett senza toccare le \*arr | fatto |
| Errori Torznab standard (codici 100–900) | Le \*arr mostrano l'errore giusto (API key errata, limite raggiunto, indexer giù) | fatto |

### 3.2 App sync (sostituzione di Prowlarr)

| Voce | Verifica | Stato (23/10) |
|---|---|---|
| Applicazioni: Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3, LazyLibrarian | Test di integrazione contro le versioni supportate di ciascuna | parziale: Mylar3/LazyLibrarian `ErrUnsupported` (WP13) |
| Livelli di sync: disabilitato, solo aggiunta/rimozione, sync completo | Test sul ciclo completo | fatto |
| Indexer sincronizzati con il protocollo giusto: Torznab per i torrent, Newznab per Usenet | Test con indexer misti | fatto |
| Campi sincronizzati: nome, URL, API key, categorie, priorità, tag, seeders minimi, seed ratio, seed time, season pack seed time, abilitazione RSS/ricerca automatica/interattiva | Confronto con ciò che Prowlarr scrive nell'app | parziale: verificati URL, categorie, protocollo, flag; seed da verificare |
| Riconciliazione: rileva modifiche manuali nell'app e indexer orfani | Test con stato divergente | parziale: manca snapshot/rollback (WP13) |

Nota: Readarr è stato ritirato dal team Servarr, ma ha ancora installazioni
attive; resta in lista finché esistono utenti.

### 3.3 Gestione degli indexer

| Voce | Verifica | Stato (23/10) |
|---|---|---|
| Aggiunta da catalogo con form generato dai `settings` della definizione | Test UI su definizioni di ciascun tipo di login | fatto; usabilità rifatta (UI fase 2) |
| Test indexer (login, ricerca, download) con esito strutturato | Esito per fase, non solo "ok/errore" | parziale |
| Proxy per indexer: HTTP, SOCKS4/5, FlareSolverr | Test con proxy simulati | parziale: FlareSolverr nativo da fare (WP11) |
| Limiti per indexer: query/giorno, grab/giorno | Le \*arr ricevono l'errore Torznab corretto al superamento | fatto |
| Priorità, tag, abilitazione, redirect del download | Test | parziale |
| Storico query e grab, statistiche per indexer | Visibili in UI e API | parziale: statistiche nel dettaglio (UI fase 2) |
| Health: indexer falliti, disabilitati temporaneamente, motivo | Stato visibile e notificabile | da fare (WP9) |
| Ricerca manuale dalla UI e invio ai client di download: qBittorrent e Transmission (esistenti), più Deluge e rTorrent; SABnzbd e NZBGet per Usenet | Test end-to-end con client simulato | parziale: client fatti, UI con un solo client (WP14) |
| Notifiche (health, grab, aggiornamenti) | Test sui canali supportati | parziale: lo scheduler nativo notifica nuove release, fallimenti e auto-disable; health e grab in WP13 |
| Backup/restore di DB, configurazione e definizioni | Restore su istanza pulita | parziale: backend WP1 fatto; riquadro UI in WP14 |

### 3.4 Motore e catalogo

| Voce | Verifica | Stato (23/10) |
|---|---|---|
| Motore: login di ogni tipo (form, CSRF, cookie, token, API key, Basic), flussi a più passi, paginazione, HTML/JSON/XML, categorie, errori del sito, download a più passaggi, mirror | Suite di test del motore con fixture per ogni funzione | fatto (WP5: `download.resolve`) |
| Connettori di famiglia per i software di tracker più diffusi | Ogni famiglia testata su fixture di almeno due siti | parziale: 8 famiglie (RSS inclusa), verifiche reali su 3 siti |
| Catalogo nativo che copre l'inventario prioritario (§7.3) | 100% dell'inventario prioritario `verificato` | parziale: 4 verificati (ExtTo, TorrentGalaxy, Corsaro, Knaben), TorrentLeech `bozza` |
| Siti senza definizione nativa tramite Jackett/Prowlarr (§7.4) | Ogni indexer importato funziona come indexer Torznab nativo | fatto (WP8; nessun percorso sorgenti separato dopo WP16) |
| Connettore Newznab generico con preset per gli indexer Usenet noti (come i preset di Prowlarr) | Caps importate dall'indexer; test su fixture Newznab | parziale: generico sì, preset no |

La parità del catalogo si misura così: **100% dei siti dell'utente
funzionanti al giorno uno** (nativi o tramite un indexer Torznab verso Jackett/Prowlarr) e **quota nativa** in
crescita continua fino al 100%.

---

## 4. Oltre la parità: cosa supera Jackett e Prowlarr

Queste sono le funzioni che si innestano sopra il nucleo. Alcune esistono già
in indexarr.

| Funzione | Limite superato | Stato |
|---|---|---|
| Cache persistente SQLite + FTS5 | Prowlarr interroga sempre live; Jackett ha solo una cache breve | Esiste |
| Ricerca `cache+live` con budget e risultati parziali | Latenza e carico sugli indexer | Parziale: live search solo su cache miss |
| Provenance multi-indexer ("visto da 3 indexer") con scelta del link migliore | Duplicati e perdita di informazione | Codice fatto; in produzione vuota (WP8) |
| Rate limit per host (token bucket) + circuit breaker + `Retry-After` | Comportamento eterogeneo tra definizioni | Codice fatto: rate limit per host attivo su ogni percorso (WP2, WP16) |
| Definizioni testate offline con fixture e record/replay | Test che richiedono il sito vivo | Parziale: fixture sì, record/replay no |
| Connettori di famiglia e API prima dello scraping (§6) | Definizioni fragili copiate sito per sito | Parziale: 8 famiglie, 4 definizioni verificate (WP6, WP7) |
| Definizioni tipizzate con fallback, mirror e health integrato (§6.4) | Definizioni che si rompono al primo cambio di layout | Formato fatto; health check non eseguito (WP9) |
| Seeders aggiornati: TTL e refresh dei risultati restituiti dalla cache | Seeders vecchi che falsano le scelte delle \*arr | Da fare (WP10) |
| Vault per credenziali e cookie | Credenziali in chiaro | Fatto lato backend: chiave fuori dai dati e segreti degli indexer cifrati (WP1) |
| Metriche per indexer e per richiesta (latenza, 429, challenge) | Diagnosi difficile | Parziale |
| Footprint: ~30 MiB RSS a riposo prima di indexarr 2 (audit) | Runtime .NET | Regressione: ~64–73 MiB dopo l'avvio (WP3) |

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

> **Stato al 23/10/2026:** i contratti esistono in `internal/engine`, ma la
> pipeline non è ancora invocata a runtime e i moduli non vengono registrati.
> Il collegamento è il WP4 di `docs/NEXT-STEPS.md`.

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
| Formato delle definizioni | Catena di upgrader v1 → vN (§6.5) |
| API Torznab | Solo aggiunte compatibili |
| API amministrativa | `/api/v2/...` versionata |

Questa è la condizione che permette di aggiungere funzioni di continuo senza
rompere installazioni esistenti.

---

## 6. Formato nativo e connettori di famiglia

### 6.1 Perché non Cardigann

indexarr **non usa Cardigann**, né come formato interno né come formato di
ingresso. Tutte le definizioni vengono riscritte. Il convertitore locale
(`indexarr-def convert-cardigann`) è solo un aiuto per chi scrive: genera una
bozza da cui partire, che va riscritta osservando il sito prima di entrare nel
catalogo. Il motivo della scelta è funzionale:

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

### 7.4 Jackett e Prowlarr per i siti senza definizione nativa

Riscrivere il catalogo richiede tempo. Perché l'utente abbia il 100% dei suoi
siti al giorno uno:

- i siti che non hanno ancora una definizione nativa funzionano come indexer
  Torznab nativi che puntano all'endpoint per-indexer del Jackett o del
  Prowlarr dell'utente;
- la UI e l'import mostrano per ogni sito se è nativo o passa dal manager;
- quando esce la definizione nativa, l'indexer si può sostituire senza toccare
  le applicazioni \*arr.

indexarr non legge né esegue definizioni Cardigann: interroga solo l'endpoint
Torznab di un servizio già installato dall'utente, in sola lettura.

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

La transizione è conclusa: indexarr 2 usa un solo modello di indexer. Il
database precedente è usa e getta; l'import da Jackett/Prowlarr ricrea i siti
come indexer nativi e non esiste più un secondo scheduler.

| Pacchetto | Esito |
|---|---|
| `internal/store` | Schema nativo (§8), migrazioni numerate, provenance in `release_sources`; la vecchia tabella delle sorgenti è stata eliminata dalla migrazione 3 |
| `internal/torznabapi` | Server Torznab con routing per indexer, filtri aggregati, alias Jackett ed errori completi; i filtri usano `release_sources` |
| `internal/native` | Scheduler unico: run periodici, run/test di tutti, backup, retention, notifiche e auto-disable |
| `internal/indexer` | Costruisce i connettori da definizione/famiglia e dal vault; esegue run, ricerca live e download |
| `internal/feed` | Parser e utility condivisi; i parser HTML storici restano solo come oracolo dei test di equivalenza |
| `internal/flaresolverr` | Diventa un'implementazione di `ChallengeSolver` nel transport (WP11) |
| `internal/downloadclient` | Resta; copre qBittorrent, Transmission, Deluge, rTorrent, SABnzbd e NZBGet |
| `internal/backup`, `internal/web` | Restano ed estendono |
| nuovi | `internal/definition`, `internal/engine`, `internal/family`, `internal/appsync`, `internal/vault`, `internal/importers` |

Jackett e Prowlarr restano utilizzabili in sola lettura: l'import crea un indexer
Torznab che punta al loro endpoint per-indexer. Non c'è migrazione di dati dal
vecchio database e non c'è rollback del vault: il DB di produzione si ricrea.

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

| Milestone | Contenuto | Uscita | Stato (23/10) |
|---|---|---|---|
| **M0 — Fondamenta** | Modello dati §8, schema nativo, vault, pipeline a stadi, interfacce `Connector` e `Module` | indexarr funziona sul nuovo modello | Fatto, salvo pipeline/moduli non collegati (WP4) |
| **M1 — Motore** | Formato v1, motore HTML/JSON/XML, sessioni, login, paginazione, rate limit per host, circuit breaker | I tre parser HTML storici sostituiti da definizioni | Fatto (WP5, WP7) |
| **M2 — Catalogo** | Inventario dei siti, connettori di famiglia, prime definizioni, studio delle definizioni, CI del catalogo | Siti propri tutti funzionanti | Fatto: 8 famiglie, 4 definizioni verificate, RSS nativa (WP6, WP7) |
| **Alpha interna** | Inventario prioritario riscritto e verificato | Uso quotidiano su istanza propria | Fatto: import da Jackett/Prowlarr in indexer nativi (WP8) |
| **M3 — Distribuzione** | Endpoint per indexer e filtrati, alias Jackett, errori Torznab, limiti query/grab, proxy | \*arr collegate senza Jackett/Prowlarr | Fatto |
| **M4 — App sync** | Tutte le applicazioni del §3.2, livelli di sync, riconciliazione | Prowlarr rimovibile | Quasi fatto: 5 app su 7, rollback mancante (WP13) |
| **M5 — Copertura** | Famiglie e siti della coda lunga, rete di salute opt-in, riparazione assistita | Quota nativa in crescita; siti mancanti via Jackett/Prowlarr | Da fare |
| **M6 — Usenet** | Connettore Newznab e preset, endpoint Newznab, proxy `.nzb`, SABnzbd/NZBGet, sync Newznab verso le \*arr | Indexer Usenet gestiti come in Prowlarr | Fatto lato codice; nessun indexer Usenet reale verificato |
| **Beta** | Health check opt-in; documentazione per chi scrive definizioni | Checklist §3 quasi completa | — |
| **1.0** | Checklist §3 verde | Rilascio pubblico | — |
| **Dopo la 1.0** | Funzioni del §4 e nuove, come moduli | Continuo | — |

Le funzioni del §4 già presenti (cache, FTS5) restano attive in tutte le
milestone. Il lavoro dettagliato per arrivare alla 1.0 è diviso in work package
in [`docs/NEXT-STEPS.md`](docs/NEXT-STEPS.md).

---

## 13. Rischi

| Rischio | Mitigazione |
|---|---|
| Volume di definizioni da riscrivere | Famiglie prima dei siti, studio delle definizioni, contributi della community, indexer Torznab per i siti mancanti |
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
- **Via Jackett/Prowlarr**: un indexer Torznab nativo che punta all'endpoint
  per-indexer del manager, per i siti senza definizione nativa.
- **Torznab**: estensione del protocollo Newznab per i torrent; è il contratto
  con le \*arr.
- **Newznab**: protocollo di ricerca per indexer Usenet.
- **Provenance**: traccia di quali indexer hanno visto una release e quando.
- **Circuit breaker**: sospende temporaneamente un indexer che fallisce.
- **Token bucket**: algoritmo di rate limiting.
- **SSRF**: server-side request forgery; far fetchare a indexarr URL interni.

---

## 15. Diario di implementazione — 23 ottobre 2026

### 15.1 Cosa esiste oggi nel repository

| Area | Stato |
|---|---|
| Formato nativo v1 | Validazione strict, JSON Schema, upgrader, `download.resolve`, `derived_from` per le bozze del convertitore |
| Motore | Login form/API key/Basic/token/cookie, paginazione, HTML/JSON/XML, categorie, errori del sito, download a più passaggi |
| Famiglie | `torznab`, `newznab`, `rss`, `unit3d`, `gazelle`, `nexusphp`, `torrenttrader`, `xbtit` |
| Catalogo | Definizioni incorporate validate e sincronizzate; ExtTo, TorrentGalaxy, Corsaro e Knaben verificate, TorrentLeech in bozza |
| Store | Schema nativo, migrazioni numerate, provenance, storico, stato, vault, applicazioni e proxy |
| Scheduler | `internal/native`: run periodici, run/test di tutti, backup, retention, notifiche e auto-disable |
| UI | Dashboard, Cerca, Indexer unificati, Definizioni, Applicazioni, Import, Attività, Regole, Impostazioni, Manutenzione |
| API | `/api/v2` per definizioni, catalogo, indexer, applicazioni, import e import Jackett/Prowlarr |
| Torznab/Newznab | Endpoint aggregato e per indexer, alias Jackett, filtri, limiti query/grab, `.torrent` e `.nzb` |
| Import | Prowlarr via HTTP e Jackett via copia della directory, solo `GET`/lettura; crea indexer nativi e segreti nel vault |
| Sicurezza | Vault AES-GCM, redazione di URL e segreti, guard SSRF, rate limit per host, circuit breaker |
| Simulazione | `simmock` e `sim/run.sh` coprono ricerca, run, Torznab, Sonarr e client di download |

### 15.2 Modello dati

Lo schema finale è descritto in §8 e in `docs/ARCHITECTURE.md`. Le migrazioni
numerate sono tre:

1. fondamenta di indexarr 2 (`definitions`, `indexers`, `indexer_secrets`,
   `indexer_state`, `release_sources`, `indexer_history`, `proxies`,
   `applications`, `app_indexers`);
2. payload Usenet in `releases` (`protocol`, `payload`, `payload_type`);
3. rimozione del vecchio modello a sorgenti: la tabella `sources`, la colonna
   `indexers.legacy_source_id`, la tabella `runs` e la denormalizzazione
   `releases.source_id`/`source_name` non esistono più. Su un database nuovo la
   migrazione è un no-op; su uno precedente ricrea `releases` e `release_sources`
   e cancella la cache non convertibile, coerentemente con il DB usa-e-getta.

### 15.3 Flusso nel binario

1. `main` risolve configurazione, vault e database, applica le migrazioni e
   sincronizza il catalogo.
2. Costruisce un solo transport educato, il registry delle famiglie, il manager
   degli indexer e il servizio delle pagine indexer.
3. Avvia lo scheduler `internal/native`, che esegue un giro subito e poi ogni
   `default_interval_seconds`; backup e retention sono nello stesso runner.
4. Monta la UI, il server Torznab/Newznab, `/healthz`, `/metrics` e l'API
   amministrativa.
5. Allo shutdown ferma il server, attende lo scheduler e il sync delle
   applicazioni.

### 15.4 API amministrativa `/api/v2`

| Metodo | Percorso |
|---|---|
| GET | `/api/v2/health` |
| GET/POST | `/api/v2/definitions`, `/api/v2/definitions/validate` |
| GET/PUT/DELETE | `/api/v2/definitions/{id}` |
| GET/POST | `/api/v2/catalog`, `/api/v2/catalog/sync` |
| GET/POST | `/api/v2/indexers` |
| GET/PUT/DELETE | `/api/v2/indexers/{id}` |
| POST | `/api/v2/indexers/{id}/test`, `/search`, `/run` |
| GET/POST | `/api/v2/applications` |
| GET/PUT/DELETE | `/api/v2/applications/{id}` |
| POST | `/api/v2/applications/sync`, `/api/v2/applications/{id}/sync` |
| POST | `/api/v2/import/prowlarr`, `/api/v2/import/jackett` |

Tutte le rotte sono autenticate con la stessa API key del server Torznab
(`apikey`, `X-Api-Key` o `Authorization: Bearer`). Non esistono più le rotte
`/api/v2/legacy/*`.

### 15.5 Invarianti da non rompere

- Il contratto Torznab/Newznab verso le \*arr non cambia: stessi endpoint,
  stessi codici di errore, stessi attributi.
- Il formato delle definizioni v1 resta retro-compatibile; i campi nuovi sono
  opzionali e gli upgrader sono espliciti.
- Nessun segreto in log, errori, export o risposta API. Gli export JSON
  contengono solo la configurazione pubblica.
- Jackett e Prowlarr si leggono soltanto: nessuna scrittura, nessun riavvio,
  nessuna modifica.
- Il DB di produzione è usa e getta; non si aggiunge codice di compatibilità
  per installazioni precedenti oltre alla migrazione numerata.

### 15.6 Verifiche

La verifica di ogni blocco è: `gofmt -l .`, `go vet ./...`, `go test ./...`,
`go test -race` sui package toccati, build incrociate Windows/Darwin/FreeBSD,
`make def-validate`, `./scripts/qa.sh`, `./scripts/ui-smoke.sh` (normale e
strict) e `./sim/run.sh`.

### 15.7 Work package completati

| WP | Stato | Contenuto |
|---|---|---|
| WP0 | fatto | convertitore Cardigann solo CLI, bozze `derived_from` rifiutate dal catalogo |
| WP1 | fatto (backend) | risoluzione chiave vault (env → env-file → config → file dati → user config → fallback), `export-key`/`move-key`, verifica restore, redazione rsskey |
| WP2 | fatto | `engine.Politeness` condiviso, rate limit 1/s burst 4, breaker, `Retry-After`, contatori host in Attività e Prometheus |
| WP3 | da fare | memoria sotto 40 MiB a riposo |
| WP4 | da fare | pipeline e moduli collegati a runtime |
| WP5 | fatto | `download.resolve`: magnet/torrent dalla pagina di dettaglio |
| WP6 | fatto | famiglia RSS/Atom nativa, cache feed, filtro client-side |
| WP7 | fatto | ExtTo, TorrentGalaxy, Corsaro e Knaben verificate; TorrentLeech in bozza |
| WP8 | passi 1–4 fatti | import in sola lettura da Jackett/Prowlarr in indexer nativi; passo 5 (scambio produzione) pronto |
| WP9–WP11 | da fare | health, refresh seeders, FlareSolverr nativo |
| WP12 | fatto | CI multipiattaforma e budget |
| WP13 | da fare | parità residua (import applicato completo, Mylar3/LazyLibrarian, rollback sync, notifiche health/grab) |
| WP14 | fasi 1, 1b, 1c fatte; 2–5 dopo WP16 | UI da pannello tecnico a prodotto semplice |
| WP-R | fatto | dettagli dopo limit/dedup, fixture senza symlink, build Windows, redazione, `example-html` |
| WP15 | da fare | parametri condizionali nelle definizioni (v1.1) |
| WP16 | fatto | rimosso il percorso legacy: `sources`, `internal/ingest`, `internal/legacyimport`, `SealLegacySecrets`, rotte `/sources*` (302), `legacy/apply|revert`, tab Nativi/Legacy, definizioni `legacy-*`, chiavi i18n orfane |

### 15.8 Come lavorare (per il prossimo agente)

- Leggi `docs/NEXT-STEPS.md` (decisione del coordinatore, §1–§3) e
  `docs/UI-GUIDE.md` prima di toccare la UI.
- Un work package per volta; aggiorna la riga WP e la checklist §3.
- Test offline obbligatori: `httptest`, fixture su disco, clock iniettabile.
- Non toccare `:8484`, `data/`, `bin/` o il servizio systemd senza il
  coordinatore; le prove girano su porte 18500–18599 con DB in `/tmp`.
- Non fare push.
