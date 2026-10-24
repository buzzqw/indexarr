# Architettura

indexarr è un singolo processo Go con un database SQLite. Non ci sono servizi
esterni obbligatori: l'unica dipendenza è il driver SQLite puro Go
`modernc.org/sqlite`, quindi il binario è statico (`CGO_ENABLED=0`).

## Flusso

```
             ┌──────── scheduler nativo (tick) ────────┐
             │                                          │
   indexers ─┤ RunOnce ─► worker x N ─► indexer.Manager.Run
             │                                          │
             └──────────────────────────────────────────┘
                                    │
                                    ▼
                    connettore (definizione o famiglia)
                                    │
                                    ▼
                      normalizzazione (engine.Item)
                                    │
                                    ▼
                    .torrent? ─► Download + InfoHash
                                    │
                                    ▼
                 store.UpsertRelease + release_sources ─► SQLite
                                    ▲
                    Torznab /api ◄──┤
                    UI web SSR ◄────┘
```

1. Lo **scheduler nativo** (`internal/native`) esegue un giro all'avvio e poi
   ogni `default_interval_seconds`, con un minimo di 60 secondi. Ogni indexer
   abilitato passa da `indexer.Manager.Run`; l'ordine degli esiti è deterministico
   per id.
2. I **worker** sono limitati (impostazione `workers`, default 2). Un indexer
   disabilitato viene saltato e riportato come tale.
3. `Manager.Run` carica la definizione, costruisce o riusa il connettore in cache,
   esegue `Search` o `Latest`, registra `indexer_history` (`kind = run`), aggiorna
   `indexer_state` e scrive le release.
4. **fetch**: definizione nativa (HTML/JSON/XML), famiglia Torznab/Newznab/RSS o
   connettore di famiglia (Unit3D, Gazelle, NexusPHP, …). Jackett e Prowlarr sono
   normali indexer Torznab che puntano all'endpoint per-indexer del manager.
5. **normalizzazione**: titolo, GUID, magnet, link `.torrent`, infohash, dimensione,
   seeders/leecher, categoria numerica, data, id IMDb/TMDB/TVDB.
6. **download `.torrent`**: solo quando manca il magnet e l'indexer offre un link di
   download. Limite per run e per dimensione; il file viene analizzato per ricavare
   l'infohash e salvato come BLOB.
7. **upsert**: la chiave è l'infohash, altrimenti `guid:`, `url:` o `title:` con hash
   SHA-256. Se l'infohash è già presente sotto un'altra chiave, quella chiave viene
   riusata. Lo stesso torrent visto da più indexer aggiorna una sola riga e aggiunge
   una riga in `release_sources` per ogni indexer.
8. **uscita**: la UI SSR legge lo stesso store; il server Torznab converte le righe
   in XML e serve i `.torrent` salvati. I filtri per indexer usano
   `release_sources`, non una colonna denormalizzata.

## Pacchetti

| Percorso | Responsabilità |
| --- | --- |
| `cmd/indexarr` | Flag, avvio, segnali, shutdown ordinato |
| `internal/config` | File JSON minimo, creazione al primo avvio, path del DB |
| `internal/store` | Schema, migrazioni numerate, impostazioni, indexer, release, provenance, storico, blocklist, ricerche salvate, manutenzione |
| `internal/definition` | Formato nativo v1: parsing YAML strict, validazione, `Fingerprint` SHA-256, catena di upgrader, JSON Schema incorporato |
| `internal/engine` | Contratti connettore/pipeline, transport con guardie e proxy, sessioni cookie, sessione FlareSolverr persistente per definizione (`session`), template, estrazione HTML/JSON/XML, normalizzazione, `download.before` e scelta magnet per stagione/episodio |
| `internal/family` | Connettori di famiglia `torznab`, `newznab`, `rss`, `unit3d`, `gazelle`, `nexusphp`, `torrenttrader`, `xbtit` |
| `internal/indexer` | Costruisce e mette in cache il connettore da definizione + configurazione + segreti, esegue un indexer torrent/Usenet, registra storico e provenance, offre la ricerca live |
| `internal/indexersvc` | Servizio delle pagine indexer: validazione, creazione/aggiornamento/cancellazione, separazione tra config pubblica e segreti nel vault |
| `internal/native` | Scheduler periodico, run/test di tutti gli indexer, backup automatici, retention, notifiche e auto-disable |
| `internal/catalog` | Catalogo incorporato (`catalog.yaml` + `definitions/*.yaml`): manifest validato, definizioni parsate, sincronizzazione verso lo store |
| `internal/torznabapi` | Server Torznab, API key multiple, rate limit, ricerca live e download `.torrent` |
| `internal/adminapi` | API JSON `/api/v2` autenticata con API key: definizioni, catalogo, indexer, applicazioni, import, test/search/run |
| `internal/web` | UI SSR, autenticazione, CSRF, form, PWA, manutenzione e ripristino |
| `internal/syncjob` | Riconciliazione periodica delle applicazioni \*arr con `appsync`, persistenza di `app_indexers` e gestione dei livelli di sync |
| `internal/downloadproxy` | Risolve i `download_ref` cifrati e serve `.torrent`/`.nzb` non ancora in cache |
| `internal/importers` | Piano e apply di import da Prowlarr e Jackett (indexer, applicazioni, proxy, categorie) |
| `internal/vault` | AES-256-GCM; chiave da `INDEXARR_MASTER_KEY`, `INDEXARR_MASTER_KEY_FILE`, `vault_key_path`, file `<db>.key` o directory utente, mai nel database (vedi [VAULT.md](VAULT.md)) |
| `internal/cardigann` | Convertitore locale Cardigann → nativo v1, con family detection e warning; non è un runtime |
| `cmd/indexarr-import` | Import di Jackett/Prowlarr in un DB indexarr usa-e-getta, senza modificare le istanze |
| `internal/feed` | Parser RSS/Atom/Torznab e utility di normalizzazione; i parser HTML storici restano solo come oracolo dei test di equivalenza |
| `internal/flaresolverr` | Client minimo per risolvere le challenge Cloudflare |
| `internal/logbuffer` | Ring buffer in memoria e handler slog con redazione dei segreti |
| `internal/notify` | Notifiche webhook JSON e Telegram, con redazione del token |
| `internal/backup` | Backup consistenti, elenco/rotazione, validazione e ripristino al riavvio |
| `internal/downloadclient` | Invio di magnet/.torrent a qBittorrent, Transmission, Deluge, rTorrent, SABnzbd e NZBGet |
| `internal/simmock`, `cmd/simmock`, `sim/` | Simulazione end-to-end deterministica: finti Torznab/Newznab, Prowlarr, Sonarr, client di download e tracker HTML |
| `internal/i18n` | Cataloghi UI incorporati (en/it/fr/de/es/zh/pl), negoziazione lingua, fallback inglese |
| `internal/perf` | Generatore dataset e test di leak per benchmark; non linkato dal binario di produzione |
| `internal/appsync` | Client Sonarr/Radarr/Lidarr/Readarr/Whisparr (API Servarr) e Mylar3/LazyLibrarian, con livelli di sync |
| `internal/deftool` | Logica testabile del CLI: validazione, formattazione, controllo fixture |
| `cmd/indexarr-def` | CLI `validate`, `schema`, `fmt`, `fingerprint`, `check-fixtures` |

Dipendenze: `definition` non importa nulla di indexarr; `engine` non importa
`store`, `web` o `torznabapi`; `indexer` è il livello che collega definizioni,
store, vault e connettori. Ogni indexer passa da un connettore nativo: non esiste
un secondo scheduler o un secondo client HTTP.

## Database

```
definitions ──1:N── indexers ──1:N── indexer_secrets
                         │──1:N── indexer_state
                         │──1:N── indexer_history
                         └──1:N── release_sources ──N:1── releases
proxies ──1:N── indexers
applications ──N:M── indexers (app_indexers)
blocklist (rules)
saved_searches (named queries)
api_keys (additional Torznab keys)
download_clients (torrent/usenet, one default per protocol rule)
settings (key/value)
```

- `definitions`: YAML nativo, famiglia, stato (`bozza`/`verificata`/`stabile`/
  `degradata`), origine (`builtin`/`catalog`/`local`), autore, hash.
- `indexers`: istanza configurata con `definition_id`, protocollo, slug unico,
  nome, priorità, `config_json` non segreto, proxy, tag e limiti di query/grab.
- `indexer_secrets`: segreti cifrati nel vault per indexer.
- `indexer_state`: cookie cifrati, ultimo login, circuit breaker, `disabled_until`
  e ultimo errore.
- `indexer_history`: storico di `run`, `search`, `latest`, `test`, `grab`,
  `download` e sync, con latenza, byte ed errore redatto.
- `release_sources`: provenienza (quale indexer ha visto una release, prima/ultima
  osservazione, seeder/leecher, `download_ref`).
- `releases`: identità (`dedup_key` univoca), infohash, id esterni, titolo e
  `search_title` normalizzato, metadati, magnet, BLOB del `.torrent` o payload
  Usenet, `first_seen_at`/`last_seen_at`/`times_seen`, `favorite` e `hidden`.
- `releases_fts`: tabella virtuale FTS5 external-content su `releases`, con
  tokenizer trigram e trigger di sincronizzazione; non duplica i titoli.
- `blocklist`: regole `infohash`, `title`, `regex`, `source`, `category`; il
  matcher compila le regex una volta per run e scarta le release prima dell'upsert.
- `saved_searches`, `api_keys`, `settings`: ricerca salvata, chiavi aggiuntive
  revocabili e opzioni key/value.
- `download_clients`: client torrent e Usenet con segreti sigillati nel vault,
  protocollo, categoria, percorso, flag predefinito. La migrazione 6 crea la
  tabella; le vecchie impostazioni `download_client*` restano come fallback e
  vengono importate al primo avvio quando il vault è disponibile.

Indici su infohash, data di pubblicazione, categoria, ultima osservazione, id
esterni e provenance. Il database usa WAL, `busy_timeout`, foreign key e un pool
limitato a quattro connessioni.

### Identità e deduplica

| Priorità | Chiave | Quando |
| --- | --- | --- |
| 1 | infohash (40 hex) | magnet, attributo Torznab, `.torrent` scaricato |
| 2 | `guid:<sha256>` | GUID stabile dell'indexer |
| 3 | `url:<sha256>` | link di download |
| 4 | `title:<sha256>` | titolo + dimensione |

L'infohash è anche la chiave di fusione tra indexer diversi. La pagina Maintenance
offre una deduplica esplicita che conserva la riga più vecchia e i dati migliori
(magnet, torrent, seeders, ultima osservazione, `times_seen`).

## Scheduler e affidabilità

- Giro completo all'avvio e poi ogni `default_interval_seconds`; minimo 60 secondi.
- Worker limitati (`workers`); un run manuale e uno periodico dello stesso indexer
  non si sovrappongono grazie al manager e allo storico.
- Timeout per richiesta dal transport; un errore viene registrato in
  `indexer_history` e in `indexer_state.last_error`.
- Notifiche webhook/Telegram per nuove release o fallimenti. Se
  `auto_disable_after` è impostato e gli ultimi run sono tutti falliti, l'indexer
  viene disabilitato e l'evento viene notificato.
- La ricerca Torznab live (`torznab_live_search`) usa `indexer.SearchLive` a ogni
  richiesta con una query: fan-out su tutti gli indexer capaci, concorrenza
  globale 8, deadline `torznab_live_timeout_seconds`; chi è in ritardo continua
  in background e cachea il risultato. Gli indexer `meta.latest: false` non
  hanno run periodici e partecipano solo alla ricerca dal vivo.
- I backup automatici (`backup_enabled`) vengono creati ogni
  `backup_interval_hours` con `VACUUM INTO`, ruotati a `backup_keep` e non
  bloccano i worker. Il ripristino viene preparato come `<db>.restore` e applicato
  da `main` prima di aprire SQLite; il file corrente viene copiato in
  `backups/pre-restore-*.db`.
- La retention gira ogni sei ore e dalla pagina Maintenance; la cronologia dei run
  viene ripulita dopo 90 giorni e viene eseguito `PRAGMA optimize` +
  `wal_checkpoint(TRUNCATE)`.

## Migrazioni

`internal/store` crea lo schema finale in modo idempotente e applica le migrazioni
numerate registrate in `schema_migrations`:

1. `indexarr2 foundation`: `definitions`, `indexers`, `indexer_secrets`,
   `indexer_state`, `release_sources`, `indexer_history`, `proxies`,
   `applications`, `app_indexers`.
2. `usenet payload`: `releases.protocol`, `payload`, `payload_type`.
3. `drop legacy sources`: per un database precedente elimina la vecchia tabella
   `sources`, la colonna `indexers.legacy_source_id`, la tabella `runs` e la
   denormalizzazione `releases.source_id`/`source_name`, poi ricrea `releases` e
   `release_sources` sul modello a provenance. Su un database nuovo è un no-op.

`catalog.Sync` scrive le definizioni incorporate solo come `builtin` e salta
quelle con origine `local`; riscrive una voce solo quando cambia l'hash.

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
- `/sources*` e `/indexers?tab=legacy` rispondono 302 verso `/indexers`;
  `/definitions`, `/rules` e `/maintenance` verso la sezione di Impostazioni.
- `GET /metrics` e `GET /sw.js` sono sul mux esterno; `/metrics` non richiede
  autenticazione come `/healthz` e non va pubblicato senza reverse proxy.
- PWA: manifest e icona negli asset, service worker che mette in cache solo
  `/static/*`; l'HTML resta di rete perché contiene CSRF e dati privati.

## Scelte

- **SQLite con FTS5 invece di un motore di ricerca esterno**: il volume tipico
  (decine di migliaia di release) non giustifica un servizio separato. La tabella
  `releases_fts` è external-content su `releases`, usa il tokenizer **trigram**
  per mantenere la ricerca a sottostringa e viene sincronizzata da trigger su
  insert/update/delete. I termini sotto i tre caratteri ricadono su `LIKE`.
- **Un solo modello di indexer**: un sito pubblico, un feed RSS o un endpoint
  Torznab di Jackett/Prowlarr sono tutti righe di `indexers`. La provenienza vive
  in `release_sources`; non c'è una colonna denormalizzata da tenere allineata.
- **Nessun download del contenuto**: indexarr è un indice; il download dei file è
  responsabilità del client torrent.
- **Un processo**: niente lock distribuiti; il database è il punto di verità.
- **Magnet prima del file**: scaricare il `.torrent` per ogni risultato sarebbe
  inutile e gravoso; si scarica solo quando è l'unico modo per ottenere la release.
- **XML tollerante**: i feed scraped contengono `&` grezzi ed entità HTML; una
  sanitizzazione prima del decoder evita che un carattere renda illeggibile
  l'intera risposta.
- **FlareSolverr su challenge**: il solver a una richiesta viene chiamato solo
  quando la risposta è riconoscibile come challenge Cloudflare, mai per un errore
  di autenticazione o per un indirizzo LAN. È stateless: una richiesta risolta
  alla volta, senza sessioni persistenti. Un indexer che protegge **tutte** le
  pagine (per esempio MirCrew) dichiara invece `session` e usa una sessione
  browser persistente, con login, riciclo a 12 ore e pulizia per prefisso; vedi
  [DEFINITIONS.md](DEFINITIONS.md#session) e [MIRCREW.md](MIRCREW.md).
- **Ricerca live opt-in**: costa tempo al client, quindi è spenta per default e
  limitata agli indexer Torznab; una ricerca con soli id esterni non la attiva.
- **Backup e ripristino**: il backup è un file SQLite autonomo; il ripristino non
  apre il file caricato nel processo vivo ma lo applica al riavvio, dopo aver
  conservato il database corrente.

## Cambiamenti 2.0.0-rc1

- La fusione delle release avviene sull’insieme completo prima di `limit`/`offset`; lo snapshot breve mantiene `total` costante tra le pagine.
- Il fan-out live condivide un worker per `(indexer, query effettiva)`, limita la coda e ritira i worker senza richieste.
- `LiveExcluded` esclude uploader TGx/ExtTo dal live; le liste continuano ad alimentare la cache.
- Le sorgenti memorizzate hanno avviso misurato, contatore e retention esplicita.
- Migrazioni 10 (dedup) e 11 (pulizia TGx hashless).

## Memoria

`indexarr` imposta un limite morbido Go di 64 MiB (`memory_limit_mb` in
`indexarr.json`, 0 per disattivarlo). Il budget `scripts/budget.sh` resta
avviso a 64 MiB ed errore a 128 MiB, misurato su `RssAnon`; l'RSS totale
include anche il binario e non è il valore del budget.
