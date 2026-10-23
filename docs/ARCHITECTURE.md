# Architettura

indexarr è un singolo processo Go con un database SQLite. Non ci sono servizi
esterni obbligatori: l'unica dipendenza è il driver SQLite puro Go
`modernc.org/sqlite`, quindi il binario è statico (`CGO_ENABLED=0`).

## Flusso

```
                 ┌────────────── scheduler (30s tick) ──────────────┐
                 │                                                  │
   Sources ──► dispatch ──► jobs (canale) ──► worker x N ──► RunSource
                 │                                                  │
                 │                        ┌─────────────────────────┘
                 │                        ▼
                 │                 feed.Client / FetchRSS
                 │                        │
                 │                        ▼
                 │              normalizzazione (Item)
                 │                        │
                 │                        ▼
                 │            .torrent? ──► FetchTorrent + InfoHash
                 │                        │
                 │                        ▼
                 └──────────────────► store.UpsertRelease ──► SQLite
                                                   ▲
                        Torznab /api ◄─────────────┤
                        UI web SSR ◄───────────────┘
```

1. Lo **scheduler** gira ogni 30 secondi, legge le sorgenti e accoda quelle la cui
   ultima esecuzione è più vecchia dell'intervallo (o del backoff dopo un errore).
2. I **worker** (impostazione `workers`) prelevano l'id dal canale e chiamano
   `RunSource`. Una mappa di sorgenti in esecuzione impedisce doppioni anche se un
   run manuale arriva mentre lo scheduler ha già accodato la stessa sorgente.
3. `RunSource` registra l'inizio (`runs` + `last_run_at`), esegue l'ingestione con
   un timeout complessivo e registra l'esito anche in caso di annullamento
   (`context.WithoutCancel` per la scrittura finale).
4. **fetch**: Torznab XML (Jackett, diretto), Prowlarr JSON, Prowlarr per indice
   via Torznab, RSS/Atom.
5. **normalizzazione**: titolo, GUID, magnet, link `.torrent`, infohash, dimensione,
   seeders/leecher, categoria numerica, data, id IMDb/TMDB/TVDB. Il magnet può
   arrivare da un attributo dedicato, da `<link>` o dall'enclosure (MirCrew usa
   quest'ultima forma). Un indexer che non riporta i seeder lascia il valore a
   "sconosciuto", non a zero.
6. **download `.torrent`**: solo quando manca il magnet e la sorgente offre un link
   di download. Limite per run e per dimensione; il file viene analizzato per
   ricavare l'infohash e salvato come BLOB.
7. **upsert**: la chiave è l'infohash, altrimenti `guid:`, `url:` o `title:` con hash
   SHA-256. Se l'infohash è già presente sotto un'altra chiave, quella chiave viene
   riusata. Lo stesso torrent da più sorgenti aggiorna una sola riga.
8. **uscita**: la UI SSR legge lo stesso store; il server Torznab converte le righe
   in XML e serve i `.torrent` salvati.

## Pacchetti

| Percorso | Responsabilità |
| --- | --- |
| `cmd/indexarr` | Flag, avvio, segnali, shutdown ordinato |
| `internal/config` | File JSON minimo, creazione al primo avvio, path del DB |
| `internal/store` | Schema, migrazioni, impostazioni, sorgenti, release, run, blocklist, ricerche salvate, manutenzione |
| `internal/feed` | Parser RSS/Atom/Torznab (tollerante su XML non perfetto), listing HTML ExtTo/TorrentGalaxy/Corsaro, scoperta feed, client Jackett/Prowlarr/MirCrew, bencode e infohash |
| `internal/flaresolverr` | Client minimo per risolvere le challenge Cloudflare |
| `internal/logbuffer` | Ring buffer in memoria e handler slog con redazione dei segreti |
| `internal/notify` | Notifiche webhook JSON e Telegram, con redazione del token |
| `internal/backup` | Backup consistenti, elenco/rotazione, validazione e ripristino al riavvio |
| `internal/downloadclient` | Invio di magnet/.torrent a qBittorrent Web API v2 e Transmission RPC |
| `internal/ingest` | Scheduler, worker, normalizzazione e upsert, blocklist, ricerca live, notifiche, auto-disable e backup automatici |
| `internal/torznabapi` | Server Torznab, API key multiple, rate limit, ricerca live e download `.torrent` |
| `internal/web` | UI SSR, autenticazione, CSRF, form, PWA, manutenzione e ripristino |
| `internal/categories` | Albero categorie newznab/Torznab |
| `internal/textutil` | Normalizzazione titoli/id e escaping LIKE |

Le dipendenze puntano verso il basso: `feed` non conosce il database, `store` non
conosce l'HTTP, `torznabapi` e `web` dipendono da `store` e dalle impostazioni.

## Database

```
sources ──1:N── runs
   │
   └──1:N── releases
blocklist (rules)
saved_searches (named queries)
api_keys (additional Torznab keys)
settings (key/value)
```

- `sources`: tipo (`torznab`/`rss`), manager, URL, API key, categorie,
  `default_category` per i feed senza categoria, query, intervallo, stato
  dell'ultimo run, contatori, `consecutive_errors` (azzerato da un run riuscito o
  da un riabilitamento).
- `releases`: identità (`dedup_key` univoca), infohash, id esterni, titolo e
  `search_title` normalizzato, metadati, magnet, BLOB del `.torrent`,
  `first_seen_at`/`last_seen_at`/`times_seen`, `favorite` e `hidden`.
- `releases_fts`: tabella virtuale FTS5 external-content su `releases`, con
  tokenizer trigram e trigger di sincronizzazione; non duplica i titoli.
- `runs`: storico con stato, conteggi (`items_seen/new/updated/blocked`), durata
  ed errore.
- `blocklist`: regole `infohash`, `title`, `regex`, `source`, `category`; il
  matcher compila le regex una volta per run e scarta le release prima
  dell'upsert.
- `saved_searches`: nome univoco e query string della pagina Releases.
- `api_keys`: chiavi aggiuntive revocabili con nome, ultimo uso e contatore; la
  chiave primaria resta in `settings`.
- `settings`: una riga per opzione, così aggiungere un'opzione non richiede una
  migrazione.

Indici su infohash, data di pubblicazione, sorgente, categoria, titolo normalizzato,
ultima osservazione e id esterni. Il database usa WAL, `busy_timeout`, foreign key e
una sola connessione: il driver puro Go è serializzato per connessione e il carico
è modesto.

### Identità e deduplica

| Priorità | Chiave | Quando |
| --- | --- | --- |
| 1 | infohash (40 hex) | magnet, attributo Torznab, `.torrent` scaricato |
| 2 | `guid:<sha256>` | GUID stabile dell'indexer |
| 3 | `url:<sha256>` | link di download |
| 4 | `title:<sha256>` | titolo + dimensione |

L'infohash è anche la chiave di fusione tra sorgenti diverse. La pagina Maintenance
offre una deduplica esplicita che conserva la riga più vecchia e i dati migliori
(magnet, torrent, seeders, ultima osservazione, `times_seen`).

## Scheduler e affidabilità

- Tick ogni 30 secondi; intervallo minimo 60 secondi per sorgente.
- Dopo un errore il ritentativo è `min(intervallo, 15 min)` con un minimo di 2
  minuti, così una sorgente momentaneamente giù recupera senza martellare.
- Coda limitata (128 job): se piena, il tick successivo riprova.
- Un run ha timeout `3 × timeout HTTP + 30 s`; la scrittura dell'esito usa un
  contesto separato per non perdersi se il run viene annullato.
- I run rimasti `running` per un arresto precedente vengono marcati interrotti
  all'avvio.
- Ogni run ha un `context.CancelFunc` registrato: il pulsante **Cancel** della
  sorgente lo annulla; l'esito viene comunque scritto e il contatore degli errori
  non viene usato per l'auto-disable di un annullamento volontario.
- Dopo il run: notifica webhook/Telegram per nuove release o fallimenti; se
  `auto_disable_after` è impostato e gli errori consecutivi raggiungono la soglia,
  la sorgente viene disabilitata. Un run riuscito azzera il contatore.
- La ricerca Torznab live (`torznab_live_search`) usa lo stesso motore: quando la
  cache è vuota interroga le sorgenti Torznab in parallelo (max 3), salva i
  risultati e lascia che il server ripeta la query.
- I backup automatici (`backup_enabled`) vengono creati ogni
  `backup_interval_hours` con `VACUUM INTO`, ruotati a `backup_keep` e non
  bloccano i worker. Il ripristino viene preparato come `<db>.restore` e applicato
  da `main` prima di aprire SQLite; il file corrente viene copiato in
  `backups/pre-restore-*.db`.
- La retention gira ogni sei ore e dalla pagina Maintenance; la cronologia dei run
  viene ripulita dopo 90 giorni e viene eseguito `PRAGMA optimize` +
  `wal_checkpoint(TRUNCATE)`.

## Server HTTP

- UI: SSR con `html/template`, CSS/JS incorporati (`go:embed`), nessuna build Node.
- Torznab: XML costruito con escaping esplicito (`xml.EscapeText`), nessuna
  dipendenza da librerie Torznab.
- Autenticazione: chiave primaria con confronto a tempo costante, chiavi
  aggiuntive nel database, rate limit per chiave; Basic con PBKDF2.
- CSRF: cookie `indexarr_csrf` HttpOnly + campo nascosto, doppio invio; l'upload
  multipart del backup usa lo stesso token nella query.
- Download: `/dl/{id}.torrent` richiede la stessa API key della ricerca e i link
  generati usano la chiave della richiesta.
- Errori XML per i client Torznab, pagine HTML con stato corretto per la UI.
- `GET /metrics` e `GET /sw.js` sono sul mux esterno; `/metrics` non richiede
  autenticazione come `/healthz` e non va pubblicato senza reverse proxy.
- PWA: manifest e icona negli asset, service worker che mette in cache solo
  `/static/*`; l'HTML resta di rete perché contiene CSRF e dati privati.

## indexarr 2

indexarr 2 è la generazione in sviluppo: motore a definizioni native, connettori
di famiglia, vault dei segreti e catalogo. Convive con il percorso legacy
descritto sopra: le sorgenti esistenti continuano a funzionare e vengono
collegate automaticamente al nuovo modello dati.

### Pipeline e connettori

`internal/engine` definisce gli stadi:

```text
plan → request → fetch → extract → normalize → enrich → dedup → store → distribute
```

Ogni stadio è una catena di middleware (`Registry.Use`): il primo registrato è
il più esterno, `Invoke` esegue il terminale dello stadio. Un `Job` trasporta
`IndexerRef`, connettore, richiesta `Search`/`Latest`, risposta, `Item` e
payload. Il contratto `Connector` espone `Capabilities`, `Search`, `Latest`,
`Download` e `Test`.

Il kernel definisce il contratto della pipeline; `internal/indexer.Run` oggi
esegue direttamente il percorso fetch → normalizzazione → deduplica → store e
usa il registry per costruire i connettori. L'orchestrazione completa degli
altri stadi è il passo successivo.

Il transport è unico per processo e condiviso dai connettori: limite della
risposta (16 MiB di default), timeout, rate limit per host, rispetto di
`Retry-After`, circuit breaker per indexer, blocco SSRF (loopback, link-local,
reti private e metadata cloud salvo `allow_private`) e redirect rivalidati.

### Package

| Percorso | Responsabilità |
|---|---|
| `internal/definition` | Formato nativo v1: parsing YAML strict, validazione, `Fingerprint` SHA-256, catena di upgrader, JSON Schema incorporato |
| `internal/engine` | Contratti connettore/pipeline, transport con guardie e proxy, sessioni cookie, template, estrazione HTML/JSON/XML, normalizzazione e `DefinitionConnector` |
| `internal/family` | Connettori di famiglia `unit3d`, `newznab` e `torznab`, registrati come factory |
| `internal/vault` | AES-256-GCM; chiave da `INDEXARR_MASTER_KEY`, `INDEXARR_MASTER_KEY_FILE`, `vault_key_path`, `<db>.key` legacy o directory utente, mai nel database (vedi [VAULT.md](VAULT.md)) |
| `internal/catalog` | Catalogo incorporato (`catalog.yaml` + `definitions/*.yaml`): manifest validato, definizioni parsate, sincronizzazione verso lo store |
| `internal/indexer` | Costruisce e mette in cache il connettore da definizione + configurazione + segreti, esegue un indexer torrent/Usenet, registra storico e provenance |
| `internal/indexersvc` | Servizio delle pagine native: validazione, creazione/aggiornamento/cancellazione indexer, separazione tra config pubblica e segreti nel vault |
| `internal/native` | Scheduler degli indexer nativi con worker limitati; salta il ponte legacy |
| `internal/adminapi` | API JSON `/api/v2` autenticata con API key: definizioni, catalogo, indexer, applicazioni, import e legacy preview, test/search/run |
| `internal/syncjob` | Riconciliazione periodica delle applicazioni \*arr con `appsync`, persistenza di `app_indexers` e gestione dei livelli di sync |
| `internal/downloadproxy` | Risolve i `download_ref` cifrati e serve `.torrent`/`.nzb` non ancora in cache |
| `internal/importers` | Piano di import da Prowlarr e Jackett (indexer, applicazioni, proxy, categorie) |
| `internal/legacyimport` | Anteprima, apply e revert della migrazione delle `sources` legacy verso un indexer Torznab nativo |
| `internal/cardigann` | Convertitore locale Cardigann → nativo v1, con family detection e warning; non è un runtime |
| `cmd/indexarr-import` | Import di Jackett/Prowlarr in un DB indexarr usa-e-getta, senza modificare le istanze |
| `internal/simmock`, `cmd/simmock`, `sim/` | Simulazione end-to-end deterministica: finti Torznab/Newznab, Prowlarr, Sonarr, client di download e tracker HTML; esecuzione locale, Docker e \*arr reali |
| `internal/i18n` | Cataloghi UI incorporati (en/it/fr/de/es/zh/pl), negoziazione lingua, fallback inglese |
| `internal/perf` | Generatore dataset e test di leak per benchmark; non linkato dal binario di produzione |
| `internal/appsync` | Client Sonarr/Radarr/Lidarr/Readarr/Whisparr (API Servarr) e Mylar3/LazyLibrarian, con livelli di sync |
| `internal/downloadclient` | Invio a qBittorrent, Transmission, Deluge, rTorrent, SABnzbd e NZBGet |
| `internal/deftool` | Logica testabile del CLI: validazione, formattazione, controllo fixture |
| `cmd/indexarr-def` | CLI `validate`, `schema`, `fmt`, `fingerprint`, `check-fixtures` |

Dipendenze: `definition` non importa nulla di indexarr; `engine` non importa
`store`, `web` o `torznabapi`; `indexer` è il livello che collega definizioni,
store, vault e connettori. `indexer.Run` non tocca mai la tabella legacy
`sources`: un indice ancora ponteggiato verso `torznab`/`rss` restituisce
`ErrLegacyConnector` e resta al percorso `ingest` finché non viene migrato.

### Tabelle

La migrazione numerata `1` (`indexarr2 foundation`) aggiunge:

| Tabella | Contenuto |
|---|---|
| `definitions` | `id`, `schema_version`, `family`, stato (`bozza`/`verificata`/`stabile`/`degradata`), origine (`builtin`/`catalog`/`local`), autore, YAML, hash, `updated_at` |
| `indexers` | Istanza configurata: `definition_id`, protocollo (`torrent`/`usenet`), slug unico, nome, priorità, `config_json` non segreto, proxy, tag, limiti, `legacy_source_id` |
| `indexer_secrets` | Segreti cifrati per indexer (`key`, `nonce`, `ciphertext`) |
| `indexer_state` | Cookie di sessione cifrati, ultimo login, stato del circuit breaker, `disabled_until`, ultimo errore |
| `release_sources` | Provenance: quale indexer ha visto una release, prima/ultima osservazione, seeder/leecher, `download_ref` |
| `indexer_history` | Storico di ricerche, latest, download e sync: query, stato, latenza, byte, errore |
| `proxies` | Proxy in uscita e relativi segreti cifrati |
| `applications` | Istanze \*arr collegate: tipo, URL, API key cifrata, livello di sync, esito |
| `app_indexers` | Associazione application ↔ indexer con `remote_id` e hash sincronizzato |

`schema_migrations` registra versione, nome e data di ogni migrazione numerata.
La cache `releases` e `releases_fts` restano condivise: il protocollo vive
sull'indexer, `release_sources` aggiunge la provenienza senza duplicare le
release. La migrazione `2` (`usenet payload`) aggiunge a `releases` il campo
`protocol`, il BLOB `payload` e `payload_type`: i torrent continuano a usare
`torrent`, mentre i `.nzb` vivono in `payload` con tipo `application/x-nzb`.
`download_ref` in `release_sources` è cifrato e non espone mai l'URL del
tracker.

### Migrazioni e backfill

1. All'apertura lo schema legacy viene creato o aggiornato in modo idempotente
   (non è numerato); `ensureSourceColumns`, `ensureReleaseColumns`,
   `ensureRunColumns` e FTS5 restano invariati.
2. `applyMigration` esegue la migrazione `1` in una transazione: se fallisce non
   viene registrata e lo schema non resta a metà.
3. `BackfillLegacySources` viene chiamato a ogni apertura ed è idempotente.
   Per ogni riga di `sources` senza indexer ponteggiato crea, nella stessa
   transazione, una definizione `legacy-<id>` e un indexer con
   `legacy_source_id` univoco. Copia nome, tipo, manager, URL, categorie,
   categoria predefinita, query e intervallo in `config_json`; **non copia mai**
   `api_key`, username o password. Se la definizione esiste ma l'indexer è stato
   cancellato non lo ricrea, per non resuscitare una rimozione volontaria.
4. `catalog.Sync` scrive le definizioni incorporate solo come `builtin` e salta
   quelle con origine `local`; riscrive una voce solo quando cambia l'hash.
   Tutte le definizioni incorporate sono oggi in stato `bozza`.

## Scelte

- **SQLite con FTS5 invece di un motore di ricerca esterno**: il volume tipico
  (decine di migliaia di release) non giustifica un servizio separato. La tabella
  `releases_fts` è external-content su `releases`, usa il tokenizer **trigram**
  per mantenere la ricerca a sottostringa e viene sincronizzata da trigger su
  insert/update/delete. I termini sotto i tre caratteri ricadono su `LIKE`.
- **Nessun download del contenuto**: indexarr è un indice; il download dei file è
  responsabilità del client torrent.
- **Un processo**: niente lock distribuiti; il database è il punto di verità.
- **Prowlarr senza query**: l'API JSON richiede un termine, quindi la modalità
  "ultime release" interroga i feed Torznab dei singoli indaci. È più lento ma
  corretto.
- **Magnet prima del file**: scaricare il `.torrent` per ogni risultato sarebbe
  inutile e gravoso; si scarica solo quando è l'unico modo per ottenere la release.
- **XML tollerante**: i feed scraped contengono `&` grezzi ed entità HTML; una
  sanitizzazione prima del decoder evita che un carattere renda illeggibile
  l'intera risposta. Vale per tutti gli indexer, non solo MirCrew.
- **Stato del servizio**: quando un Torznab diretto espone `/status` (MirCrew),
  il test di connessione lo legge e distingue un login al forum fallito da un
  servizio sano. Non blocca l'acquisizione: l'indice locale può essere aggiornato
  anche con il forum irraggiungibile.
- **FlareSolverr su challenge**: il solver viene chiamato solo quando la risposta
  è riconoscibile come challenge Cloudflare, mai per un errore di autenticazione
  o per un indirizzo LAN. È stateless: una richiesta risolta alla volta, senza
  sessioni persistenti.
- **Quick add e Corsaro**: l'host decide il parser (ExtTo/TorrentGalaxy/Corsaro),
  altrimenti si prova RSS/Torznab; `default_category` copre i listing che non
  dichiarano una categoria. Il form usa preset e “Detect type” solo come aiuto,
  i valori restano modificabili.
- **Ricerca live opt-in**: costa tempo al client, quindi è spenta per default e
  limitata alle sorgenti Torznab; una ricerca con soli id esterni non la attiva.
- **Backup e ripristino**: il backup è un file SQLite autonomo; il ripristino non
  apre il file caricato nel processo vivo ma lo applica al riavvio, dopo aver
  conservato il database corrente.
