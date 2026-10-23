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

## Scelte

- **SQLite invece di un motore di ricerca**: il volume tipico (decine di migliaia di
  release) non giustifica un servizio esterno; `LIKE` su titolo normalizzato e gli
  indici bastano. La ricerca FTS5 potrà essere aggiunta senza cambiare l'API.
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
