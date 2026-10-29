# indexarr

**Indexer torrent e Usenet self-hosted, compatibile Torznab/Newznab.**
indexarr costruisce un indice locale delle release, lo tiene aggiornato e lo
espone a Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3, LazyLibrarian o a
qualunque client Torznab. Nasce per sostituire Jackett e Prowlarr nel flusso
quotidiano: fonti native, cache SQLite, ricerca live, sincronizzazione delle
app e un'interfaccia web completa, senza servizi esterni obbligatori.

Non scarica il contenuto dei torrent o degli NZB: costruisce un **indice**
interrogabile. Il download resta al client scelto — qBittorrent, Transmission,
Deluge, rTorrent, SABnzbd, NZBGet, aMule e altri.

```
Fonti native / RSS / Newznab ──► scheduler ──► normalizzazione ──► SQLite
                                                                      │
       Sonarr / Radarr / Lidarr / Readarr ◄── Torznab / Newznab ──────┤
                          Browser ◄── UI web SSR + API JSON ──────────┘
```

## Perché indexarr

- **Un solo binario Go**, SQLite senza CGO, nessun database o servizio
  obbligatorio.
- **Catalogo nativo** di definizioni e connettori di famiglia; nessun proxy
  verso Jackett o Prowlarr.
- **Cache reale**: le release restano disponibili anche quando un sito è lento
  o irraggiungibile, con deduplica, ricerca full-text e retention.
- **Torznab e Newznab** sullo stesso endpoint, per torrent, Usenet ed eD2k.
- **Interfaccia web** per cercare, gestire le fonti, sincronizzare le app,
  inviare ai client e fare manutenzione.
- **Sicurezza operativa**: API key multiple, rate limit, vault per i segreti,
  CSRF, backup e ripristino dalla UI.

## Funzioni

### Acquisizione

- Catalogo di **definizioni native** e connettori di famiglia (UNIT3D,
  NexusPHP, XBTIT, Gazelle, TorrentTrader, Newznab, Torznab, RSS).
- Feed **RSS/Atom**, import massivo di elenchi RSS e OPML, **Quick add** e
  **Discover feeds**.
- Parser dedicati per ExtTo, TorrentGalaxy e Corsaro.
- **MirCrew** con definizione nativa e sorgente Smartfeed.
- Qualunque endpoint **Torznab** o **Newznab** diretto.
- Sorgenti **eD2k** (`protocol: ed2k`) con link `ed2k://`.
- Ogni sorgente ha il proprio intervallo, parte subito al primo avvio,
  ritenta con backoff e può essere rallentata con **Riduci la frequenza**
  dopo un HTTP 429.

### Cache e ricerca

- Titolo, dimensione, seeder/leecher, categoria, data, GUID, infohash, id
  esterni (IMDb/TMDB/TVDB), magnet, `.torrent`, `.nzb` e link eD2k.
- **Deduplica** per infohash quando disponibile, altrimenti per GUID/URL/titolo.
- **FTS5** con tokenizer trigram: trova anche sottostringhe; i termini corti
  usano un fallback `LIKE`.
- Ricerca per **regex RE2**, esclusione `-termine`, ricerca nei nomi dei file
  dei torrent, filtri per qualità, fonte, lingua, seeder e dimensione.
- **Ricerche salvate**, preferiti, release nascoste e blocklist.
- **Retention** automatica e pulizia manuale.

### Torznab / Newznab

- Endpoint aggregato `/api` e per-indexer `/torznab/{slug}/api`.
- Funzioni `caps`, `search`, `tvsearch`, `movie`, `music`, `book` e `get`.
- Download `.torrent` e `.nzb`, paginazione, categorie standard, stagione,
  episodio e id esterni.
- **Ricerca live opzionale** quando la cache non ha risultati: interroga le
  sorgenti Torznab/Newznab abilitate, salva ciò che arriva e ripete la query.
- API key multiple, revoca per chiave e rate limit per chiave.

### Interfaccia web

- **Dashboard** con stato delle fonti, ultime release ed esecuzioni.
- **Releases** con ricerca live manuale: testo libero, TV, film, musica e
  libri, id IMDb/TMDb/TVDb, selezione di più indexer, categorie, filtri,
  ricerche salvate e **invio multiplo** ai client.
- **Indexer** con elenco unico, Run/Test/Run all/Test all, dettaglio, modifica,
  clone, export/import JSON, scoperta feed e import cookie.
- **App** per collegare e sincronizzare Sonarr, Radarr, Lidarr, Readarr,
  Whisparr, Mylar3 e LazyLibrarian.
- **Attività** con storico dei run, nuove release e ricerche delle app.
- **Log** con filtri, livello runtime, richieste ricevute e statistiche.
- **Rules** per blocklist, **Settings** per la configurazione e **Maintenance**
  per integrità, vacuum, deduplica, retention, backup e ripristino.

### Gestione per indexer

- Proxy **HTTP/HTTPS/SOCKS4/SOCKS4A/SOCKS5**, con credenziali nel vault.
- Limiti query/grab, seeder minimi, seed ratio e seed time.
- Priorità, tag, intervallo e User-Agent per sorgente.
- **Solo freeleech** quando la definizione lo supporta.
- **Importa cookie** dal profilo Firefox per i siti che legano la sessione al
  browser, come IPTorrents.

### App \*arr

- Sync verso Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3 e
  LazyLibrarian.
- Livelli **disattivata**, **solo aggiunta/rimozione** e **completa**.
- Profilo per applicazione: RSS, ricerca automatica, ricerca interattiva,
  seeder minimi e categorie Torznab da inviare.
- Filtro per tag, snapshot prima delle modifiche e ripristino dello stato
  precedente dalla UI.

### Client di download

Supportati:

| Client | Protocollo | Note |
| --- | --- | --- |
| qBittorrent | torrent | Web API v2, categoria, percorso, pausa, seed ratio/time |
| Transmission | torrent | RPC, magnet e `.torrent` |
| Deluge | torrent | JSON-RPC |
| rTorrent | torrent | XML-RPC |
| Aria2 | torrent | JSON-RPC, token, `dir`, pausa |
| uTorrent | torrent | Web API, token, magnet e file |
| Flood | torrent | REST, tag e destinazione |
| Download Station | torrent | Synology `auth.cgi` + `task.cgi` |
| Hadouken | torrent | JSON-RPC, etichetta |
| Torrent Blackhole | torrent | scrive `.torrent` o `.magnet` in una cartella |
| SABnzbd | Usenet | API, `.nzb`, categoria, pausa |
| NZBGet | Usenet | JSON-RPC, `.nzb`, categoria, pausa |
| NZBVortex | Usenet | API, `.nzb`, gruppo |
| Usenet Blackhole | Usenet | scrive `.nzb` in una cartella |
| aMule | eD2k | Web API, link `ed2k://` |

Il client predefinito riceve i grab; dalla pagina Releases si possono inviare
più release con **Invia selezionati**. Il seed ratio/time della sorgente viaggia
verso qBittorrent e verso le app \*arr; gli altri client mantengono i propri
valori.

### Notifiche

Canali indipendenti, ognuno con i propri segreti cifrati:

- webhook JSON, Telegram, Apprise, Discord, Slack, Gotify, ntfy, Pushover,
  email SMTP e script personalizzato.

Eventi disponibili: nuove release, cambi di stato degli indexer, grab fallito,
grab riuscito, catalogo aggiornato e backup fallito. C'è un intervallo
anti-spam per evento e un pulsante di prova che verifica tutti i canali
configurati.

### Operatività

- API key multiple con revoca e rate limit.
- Auto-disable dopo N errori consecutivi.
- Backup automatici con rotazione e ripristino dalla UI.
- Metriche Prometheus su `/metrics`.
- Log in-app, file di log a rotazione, `journalctl` con systemd.
- PWA installabile; le pagine HTML restano sempre di rete.

## Compatibilità

indexarr parla **Torznab** per i torrent e **Newznab** per Usenet. Questo è il
quadro reale, non un elenco di intenti:

| Sorgente | Supporto | Come |
| --- | --- | --- |
| MirCrew | completo | definizione nativa, login, ricerca, magnet al grab con ringraziamento; Smartfeed per i topic nuovi |
| Torznab diretto | completo (torrent) | endpoint completo o base; `/api` viene aggiunto se manca |
| ExtTo | completo | parser HTML dedicato: titolo e magnet dalla stessa riga |
| TorrentGalaxy | completo | parser delle pagine `post-detail`, con cache dei magnet |
| Corsaro | completo | parser di titoli `/torrent/` e magnet diretti |
| Newznab / Usenet | completo (generico) | `newznab-generic`: caps, ricerca `search`/`tv`/`movie`/`music`/`book`, `.nzb` e invio a SABnzbd/NZBGet. Definizioni native per singoli siti Usenet non ancora in catalogo |
| eD2k / aMule | completo | definizioni `protocol: ed2k`, link `ed2k://` archiviati, espansi per file, inviati ad aMule |
| RSS / Atom | completo | feed normali, enclosure `.torrent`, `.nzb` o magnet |
| Indexer privati con Basic | completo | campi HTTP username/password per sorgente |
| API key in query, header o URL | completo | `apikey` come parametro e come `X-Api-Key` |
| Cloudflare / challenge | con FlareSolverr | retry automatico per RSS e indexer diretti; gli indirizzi LAN non passano dal solver |
| API proprietarie non Torznab | non automatico | la fonte resta in coda finché non esiste una definizione nativa |

**Jackett e Prowlarr non sono sorgenti**: indexarr li sostituisce. Non c'è un
import che li legga e non viene creato un proxy verso di loro. Le fonti si
aggiungono dal catalogo nativo; una fonte senza via nativa resta «da scrivere»
e non compare nell'elenco utente.

**Cardigann**: il convertitore `indexarr-def convert-cardigann` aiuta a
studiare una definizione, ma il formato non viene eseguito a runtime e le
definizioni derivate non entrano nel catalogo.

Dettagli, casi limite e diagnostica: [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md).

## Requisiti

- **Go 1.26 o successivo** per compilare. Il binario non ha dipendenze runtime.
- Un filesystem scrivibile per il database.
- Facoltativi: FlareSolverr per Cloudflare, un client torrent/Usenet/eD2k per
  l'invio delle release.

## Avvio rapido

```bash
make build
./bin/indexarr
```

Al primo avvio, nella directory corrente, vengono creati:

- `indexarr.json` — configurazione minima (porta, database, log);
- `indexarr.db` — database SQLite;
- una API key Torznab casuale, stampata nel log e visibile in **Settings**.

Apri <http://localhost:8788>, aggiungi una sorgente e premi **Run**. In
**Settings** trovi l'URL Torznab da consegnare alle app.

Flag disponibili:

```text
-config string   file JSON di configurazione (default "indexarr.json")
-listen string   indirizzo HTTP, sovrascrive il file
-db string       percorso SQLite, sovrascrive il file
-version         stampa la versione
```

## Configurazione

`indexarr.json` contiene solo l'avvio:

```json
{
  "listen": ":8788",
  "db_path": "indexarr.db",
  "log_level": "info",
  "vault_key_path": "",
  "memory_limit_mb": 64,
  "log_file": "logs/indexarr.log",
  "log_max_mb": 10,
  "log_max_files": 5
}
```

Un `db_path` relativo è risolto rispetto alla directory del file di
configurazione. `vault_key_path` è opzionale: se vuoto la chiave del vault viene
cercata in `INDEXARR_MASTER_KEY`, `INDEXARR_MASTER_KEY_FILE`, `<db>.key` legacy
(se esiste) e poi `os.UserConfigDir()/indexarr/master.key`. Un percorso relativo
è risolto rispetto alla directory di `indexarr.json`. Dettagli, comandi
`vault export-key` / `vault move-key`, backup e Docker secrets in
[docs/VAULT.md](docs/VAULT.md).

`log_file` è il file di log a rotazione (relativo come `db_path`; `"off"` lo
spegne): quando supera `log_max_mb` diventa `indexarr.log.1`, i più vecchi
scorrono e si tengono `log_max_files` file in tutto. Un file di configurazione
vecchio senza questi campi usa i predefiniti.

Tutto il resto — API key, intervalli, retention, credenziali UI, notifiche,
client, backup — è nel database e si modifica dalla pagina **Settings**, senza
riavviare.

### Impostazioni principali

| Impostazione | Default | Significato |
| --- | --- | --- |
| `default_interval_seconds` | 3600 | Intervallo delle sorgenti senza uno proprio |
| `fetch_timeout_seconds` | 45 | Timeout per singola richiesta HTTP |
| `workers` | 2 | Sorgenti eseguite in parallelo (richiede riavvio) |
| `max_torrents_per_run` | 30 | Limite di `.torrent` scaricati per esecuzione |
| `max_torrent_bytes` | 8 MiB | Dimensione massima di un `.torrent` |
| `fetch_torrents` | on | Scarica i `.torrent` quando la sorgente offre solo un link |
| `retention_days` | 0 | Elimina le release non più viste dopo N giorni (0 = mai) |
| `public_url` | vuoto | URL pubblico per i link Torznab dietro reverse proxy |
| `notify_*` | — | Canali e trigger delle notifiche |
| `auto_disable_after` | 0 | Disabilita una sorgente dopo N errori consecutivi (0 = mai) |
| `flaresolverr_url` | vuoto | Endpoint FlareSolverr per RSS e indexer diretti |
| `torznab_download_factor` | 0 | `downloadvolumefactor` (0 = freeleech) |
| `torznab_upload_factor` | 1 | `uploadvolumefactor` |
| `api_rate_limit_per_minute` | 0 | Richieste per chiave API al minuto (0 = illimitate) |
| `torznab_live_search` | on | Interroga gli indexer quando la cache è vuota |
| `torznab_live_timeout_seconds` | 20 | Deadline complessiva della ricerca live |
| `download_client` | none | Client legacy; i client moderni sono nella pagina App |
| `backup_enabled` | on | Backup automatico del database |
| `backup_interval_hours` | 24 | Intervallo dei backup automatici (6–168) |
| `backup_keep` | 7 | Numero di backup automatici conservati (1–60) |
| `log_level` | config | Livello runtime (`debug`, `info`, `warn`, `error`) |
| `memory_limit_mb` | config | Limite morbido di memoria Go in MiB (0 lo disattiva) |

## Sorgenti

Ogni sorgente può avere credenziali **HTTP Basic** (campi *HTTP username* e
*HTTP password*), indipendenti dalla API key Torznab. Accanto a ogni sorgente
ci sono **Test** e **Run**; **Test all** e **Run all** lavorano su tutte le
sorgenti abilitate e scrivono gli esiti in **Attività** e nei log.

### Catalogo e aggiunta

- **Aggiungi indexer**: scegli una definizione del catalogo (ExtTo,
  TorrentGalaxy, Corsaro, Knaben, TorrentLeech, una generica) oppure incolla un
  endpoint Torznab.
- **Quick add** riconosce Torznab, Newznab, RSS e listing HTML.
- **Discover feeds** estrae i feed dichiarati da una pagina.
- **Export/import JSON** salva e ricarica la configurazione pubblica degli
  indexer; i segreti non vengono mai esportati.
- Le fonti non ancora pronte restano in coda e non vengono proposte
  nell'elenco utente; la vista di manutenzione `/indexers/new?queue=1` mostra
  anche la composizione della coda.

Al **2026-10-29** il catalogo contiene **627 voci**: 111 definizioni native,
85 verificate (84 torrent + 1 eD2k), 92 host pubblici verificati e 495 voci
ancora da provare, in gran parte private. La copertura dei singoli siti cresce
con il catalogo; il percorso Usenet generico è già completo.

### RSS / Atom

- **Type**: RSS / Atom feed
- **URL**: il feed
- Vengono letti titolo, magnet, enclosure `.torrent`/`.nzb`, dimensione,
  categoria numerica, data e gli eventuali attributi `torznab:attr`.
- Le voci senza magnet o link di download vengono ignorate.
- **Default category** assegna una categoria Torznab ai feed e ai listing che
  non ne dichiarano una.

### Solo freeleech

Quando la definizione lo prevede, il form dell'indexer mostra la casella
**Solo freeleech** (`setting.freeleech`). Se il sito accetta un filtro nella
ricerca, la definizione lo passa come parametro (per esempio `free=1`); se il
sito marca ogni riga, il motore scarta le righe non gratuite prima di salvarle
e di rispondere alle app. L'opzione compare solo per le definizioni che la
dichiarano e vale anche per l'elenco delle novità e per la ricerca live.

### Torznab diretto

- **URL**: endpoint completo, per esempio `http://indexer.local:8080/api`
- **Manager**: Direct Torznab (o Auto)
- **Categorie**: id Torznab separati da virgola (`5000, 2000`)

### Usenet / Newznab

- **Type**: Newznab / Usenet
- **URL** e **API key** del servizio
- **Categorie**: id Newznab standard
- Il protocollo è `usenet`; il payload è un `.nzb` e l'invio va a SABnzbd o
  NZBGet.
- Le definizioni native per singoli siti Usenet non sono ancora in catalogo:
  una fonte che espone Newznab funziona subito, le altre restano in coda.

### eD2k / aMule

Le definizioni con `protocol: ed2k` pubblicano link
`ed2k://|file|nome|dimensione|hash|/`. indexarr li archivia, li espande per
file e li invia ad aMule tramite la Web API. Il link può arrivare dal corpo
della pagina o da un campo eD2k; i topic con più file producono una release per
file. eD2k resta escluso dalla sincronizzazione con le app \*arr.

### Siti HTML: ExtTo, TorrentGalaxy, Corsaro

I tre siti hanno definizioni native con fixture reali e test di equivalenza.
Il motore segue i link di dettaglio per ottenere il magnet, con concorrenza
limitata e cache. Limiti: prima pagina, massimo 50 release per sorgente; se il
layout cambia o il sito richiede il login, il test riporta l'errore.

### Sorgenti private, cookie e MirCrew

- **HTTP Basic** per feed e indexer privati.
- **Importa cookie** per i siti con login a form protetto da Cloudflare
  Turnstile, come IPTorrents: legge il profilo Firefox, invia il cookie a
  indexarr e lo salva cifrato nel vault. Serve lo stesso User-Agent del browser.
- **MirCrew** è un indexer Torznab diretto: accetta base o `/api`, magnet in
  `<link>`/`<enclosure>`, seeder assenti e `/status` per il login al forum. Il
  timeout predefinito è 45 secondi; un login al forum fallito non ferma
  l'indice locale.

### Cloudflare / FlareSolverr

- Endpoint in **Settings → Cloudflare / FlareSolverr**.
- Riconosce `cf-mitigated: challenge` e le pagine Cloudflare su 403/503/200.
- Ripete la richiesta tramite FlareSolverr per RSS e indexer diretti.
- Gli indirizzi LAN non passano mai dal solver.
- I `.torrent` non passano dal solver: se il sito richiede il clearance anche
  per il file, usa il magnet.

### Jackett e Prowlarr

indexarr li **sostituisce**: non sono sorgenti e non c'è un import che li
legga. Un URL che appartiene a un manager viene rifiutato con un messaggio
esplicito. Vedi [docs/MIGRATION-MANAGERS.md](docs/MIGRATION-MANAGERS.md).

## Collegare le app

Nella pagina **Settings** copia l'endpoint completo:

```text
http://<host>:8788/api?apikey=<API_KEY>
```

Nel client aggiungi un indexer **Torznab**:

| Campo | Valore |
| --- | --- |
| URL | `http://<host>:8788/api` |
| API Key | la chiave mostrata in Settings |
| Categories | `5000` TV, `2000` Film, `3000` Audio, `7000` Libri |

Sono supportati anche l'alias `/torznab/api` e il parametro `apikey`
nell'header `X-Api-Key`. Dettagli completi in [docs/API.md](docs/API.md).

### Sync delle app

In **App** colleghi Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3 e
LazyLibrarian. Scegli il livello:

- **Disattivata**: nessun contatto;
- **Solo aggiunta/rimozione**: crea e rimuove gli indexer;
- **Completa**: aggiorna anche gli indexer esistenti.

In **Avanzate** imposti il profilo della singola app: RSS, ricerca automatica,
ricerca interattiva, seeder minimi (0 = valore dell'indexer) e categorie
Torznab da inviare (vuoto = tutte quelle supportate). Un indexer senza
categorie in comune non viene sincronizzato con quell'app. Il filtro per tag
decide quali sorgenti mandare a quale app; prima di ogni modifica viene salvato
uno snapshot, ripristinabile dalla stessa pagina.

indexarr crea nell'app solo gli indexer il cui nome inizia con `indexarr · `.
Il resto non viene toccato.

### API key multiple e rate limit

In **Settings → Torznab API** la chiave primaria resta quella mostrata
nell'endpoint. Sotto, **Additional API keys** permette di crearne una per
client (Sonarr, Radarr, Lidarr, uno script) con nome, data di creazione, ultimo
uso e numero di richieste. Ogni chiave è revocabile senza toccare le altre; i
link `.torrent`/`.nzb` usano la chiave con cui è stata fatta la richiesta.
**API requests per key per minute** applica un limite per chiave
(0 = illimitato): oltre la soglia la risposta è HTTP 429 con `Retry-After`.

### Ricerca live

Con **Search indexers live on a cache miss** attivo, una ricerca Torznab che
non trova nulla nella cache interroga al momento le sorgenti Torznab/Newznab
abilitate, salva i risultati e ripete la query. Le sorgenti RSS/HTML sono
escluse perché non accettano un termine di ricerca. `Live search timeout`
limita l'attesa complessiva; le sorgenti lente vengono ignorate. È attiva per
default perché di solito evita un "nessun risultato" falso quando la cache è
ancora vuota; puoi spegnerla se preferisci la risposta più rapida possibile. La
query deve essere presente o deve esserci un id esterno; una richiesta solo per
id non attiva una ricerca live sui siti che non supportano gli id.

## Interfaccia web

| Pagina | Contenuto |
| --- | --- |
| **Dashboard** | Totali, stato degli indexer, ultime release ed esecuzioni |
| **Releases** | Ricerca live manuale (testo, TV, film, musica, libri, ID esterni), esclusione `-xxx`, regex, filtri avanzati, ricerche salvate, preferiti/nascosti, grab multiplo; dettaglio con magnet, `.torrent` e `.nzb` |
| **Indexer** | Elenco unico, Run/Test/Run all/Test all, dettaglio, modifica, export/import JSON e scoperta feed |
| **App** | Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3, LazyLibrarian; sync, profilo per app, snapshot e client di download |
| **Attività** | Storico aggiornamenti, nuove release, ricerche delle app |
| **Log** | Log interni con filtri e livello runtime, richieste ricevute, statistiche |
| **Rules** | Blocklist per infohash, titolo, regex, sorgente o categoria |
| **Settings** | API key, endpoint Torznab, ricerca live, intervalli, retention, credenziali UI, notifiche, auto-disable, backup |
| **Maintenance** | Integrità, vacuum, optimize/checkpoint, reindex, deduplica, pulizia `.torrent`, retention, purge per indexer, backup e ripristino |

L'interfaccia è una **PWA**: manifest, icona e service worker che mette in cache
solo gli asset statici; le pagine HTML restano sempre di rete perché contengono
CSRF e dati privati.

## Ricerca nelle release

La pagina **Releases** cerca mentre scrivi: la richiesta parte dopo 250 ms,
quella precedente viene annullata e viene aggiornata solo la tabella, senza
ricaricare la pagina. L'URL resta condivisibile e il modulo funziona anche
senza JavaScript.

- **Esclusione**: `-xxx` toglie i risultati che contengono `xxx`. Si combinano
  più termini: `show -1080p -cam`.
- **Full-text**: i termini di almeno tre caratteri usano FTS5 con tokenizer
  trigram; anche le sottostringhe funzionano. I termini più corti usano
  `LIKE`.
- **Regex**: spunta **Regex**; ogni termine è un'espressione regolare RE2,
  case-insensitive per default. I termini sono in AND, `-pattern` esclude, le
  virgolette mantengono gli spazi (`"the show"`) e `/pattern/` è accettato.
- Un'espressione non valida mostra l'errore senza svuotare la pagina.
- Sorgente, categoria e ordinamento si combinano con il testo; la paginazione
  resta sulla pagina completa.

### Ricerca live manuale

Il pannello **Ricerca live** interroga subito i siti selezionati, salva ciò che
restituiscono e lo mostra sotto. Modalità disponibili:

- **Ricerca libera**: qualunque titolo.
- **TV**: query, stagione, episodio, anno, TVDb/IMDb/TMDb id.
- **Film**: query, anno, IMDb/TMDb id.
- **Musica**: artista, album, anno.
- **Libri**: autore, titolo.

Puoi scegliere uno o più indexer e una categoria. I siti che rispondono in tempo
mostrano i risultati subito; quelli in timeout continuano in background e
possono comparire dopo un ricaricamento. La vista live mostra al massimo 50
righe e non usa la paginazione normale.

### Filtri avanzati e ricerche salvate

Sotto la ricerca ci sono i filtri strutturati:

- risoluzione (`2160p/4K`, `1080p`, `720p`, `480p`);
- fonte (`WEB-DL`, `BluRay`, `HDTV`);
- lingua (`Italian`, `English`);
- seeder minimi, dimensione minima e massima in MB;
- **Favorites** e **Show hidden**.

Si combinano con il testo e con la modalità regex. **Save this search as…**
salva la vista con un nome; le ricerche salvate compaiono sopra i risultati come
link pronti, con una × per eliminarle. La pallina ★ indica le release preferite.

### Regole, preferiti e nascosti

Il tab **Rules** gestisce la blocklist:

- per **infohash** (preciso), **titolo** (sottostringa), **regex** (Go/RE2,
  case-insensitive), **nome sorgente** o **categoria** (`5000` copre `5xxx`);
- le release che corrispondono non vengono più inserite nei run successivi;
- sul dettaglio di una release ci sono **Favorite**, **Hide** e **Block
  release**: Block aggiunge la regola (infohash se disponibile, altrimenti
  titolo) e la nasconde subito.

Nascondere una release la toglie dalle viste normali ma non impedisce a un nuovo
run di reinserirla: per questo esiste la blocklist. Le release bloccate sono
contate nella colonna **Blocked** dei run.

### Invio ai client

In **Settings → Download client** o dalla pagina **App** si sceglie il client,
si inseriscono URL e credenziali e, per qBittorrent, categoria e percorso. Sul
dettaglio di ogni release compare **Invia**: viene inviato il `.torrent`/`.nzb`
salvato, oppure il magnet se il file non è in cache. Per aMule viene inviato il
link `ed2k://` alla Web API (`/api/v1/downloads`); il protocollo della release
sceglie il client.

qBittorrent usa la Web API v2; Transmission il RPC con negoziazione della
sessione. Quando l'indexer che ha fornito la release ha un **seed ratio** o un
**seed time**, qBittorrent li riceve già all'aggiunta
(`ratioLimit`/`seedingTimeLimit`), così il torrent si ferma al limite della
fonte; gli altri client mantengono i propri valori. Lo stesso `seed_ratio`
viaggia verso le \*arr con la sincronizzazione delle applicazioni.

## Notifiche e auto-disable

In **Settings → Notifications**:

- **Webhook URL**: riceve un POST JSON con `event`, `source`, `count`, `titles`,
  `error` e `at`;
- **Telegram bot token** e **chat ID**: messaggio leggibile; vanno impostati
  entrambi o nessuno;
- **Apprise**, **Discord**, **Slack**, **Gotify**, **ntfy**, **Pushover**,
  **email SMTP** e **script personalizzato**: canali indipendenti, con i propri
  segreti cifrati nel vault;
- **Notify new releases**, **Notify failures**, **Grab failed**, **A grab
  succeeded**, **Catalog updated** e **Backup failed** scelgono gli eventi;
- **Auto-disable after N consecutive failures**: dopo N run falliti la sorgente
  viene disabilitata; un run riuscito azzera il contatore.

L'**anti-spam** evita che lo stesso evento (tipo e sorgente) venga inviato più
volte nello stesso intervallo. Il pulsante **Invia notifica di prova** verifica
tutti i canali configurati. I messaggi sono nella lingua scelta in
**Settings → Notifications → Message language**: «Come l'interfaccia» segue il
selettore in alto, una lingua fissa resta anche cambiando interfaccia.

## Backup e ripristino

Il backup di indexarr contiene il **database SQLite**, non i file scaricati.
Il database sta in `data/indexarr.db`; i backup coerenti sono in
`data/backups/pre-upgrade-*.db`.

La **chiave del vault non è nel database**: va conservata separatamente
(`INDEXARR_MASTER_KEY`, `vault_key_path` o il file utente
`~/.config/indexarr/master.key`). Senza quella chiave i segreti salvati non si
aprono. Dettagli in [docs/VAULT.md](docs/VAULT.md).

Dalla pagina **Maintenance** puoi creare un backup, scaricarlo, eliminarlo e
ripristinarlo. Il ripristino carica un `.db` validato, lo copia in
`indexarr.db.restore`, il servizio si riavvia e il file viene applicato prima di
aprire il database. Il database corrente viene copiato in
`data/backups/pre-restore-*.db`. L'unità systemd usa `Restart=always` per
completare il riavvio; senza systemd basta riavviare a mano.

## Manutenzione e diagnostica

- **Database report**: dimensioni, pagine libere, WAL/SHM, conteggio righe e
  ripartizione delle release.
- **Integrity check**, **quick check**, **foreign key check**.
- **Vacuum**, **optimize/checkpoint**, **analyze**, **rebuild indexes**.
- **Deduplica per infohash**, pulizia `.torrent`, retention, pulizia delle
  release senza link o nascoste, purge per indexer.
- **Backup automatici** con rotazione e ripristino dalla UI.
- **Salute sorgenti** con errori, fallimenti e pulsanti Test/Run.
- **Clear source errors**, **reset API usage**, **recount source items**.
- **Metriche Prometheus** su `GET /metrics` (`indexarr_releases`,
  `indexarr_indexers`, `indexarr_runs_24h`, `indexarr_errors_24h`,
  `indexarr_blocklist_rules`, `indexarr_db_bytes`, uptime…), senza
  autenticazione come `/healthz`.
- **Runs → dettaglio**: conteggi, durata ed errore completo di un singolo run.
- **Cancel** nella riga di una sorgente in esecuzione annulla il run.

### Log

Il tab **Logs** mostra gli ultimi **2000** messaggi del processo, dal più
recente:

- filtro per livello (`debug`, `info`, `warning`, `error`), per **origine**
  (`indexarr`, `indexer`/Torznab, RSS/HTML, notifiche) e ricerca nel messaggio
  e negli attributi;
- pulsante **Problems** con il conteggio di warning ed errori che corrispondono
  ai filtri correnti;
- limite righe (100/300/1000) e **auto-refresh** ogni 10 secondi;
- **Download .txt** e **Clear** del buffer;
- qui arrivano anche gli esiti di **Test all** e del **Test FlareSolverr**.

Ogni errore di una sorgente riporta `source`, `kind`, `host` e il messaggio
originale. Gli errori interni usano `component=indexarr`, quelli degli indexer
`component=indexer` e quelli dei feed `component=rss`. Le stesse righe vanno su
stdout (journal con systemd) e nel file di log a rotazione
`logs/indexarr.log` accanto al database: 5 file da 10 MB al massimo.

```bash
tail -f data/logs/indexarr.log          # file a rotazione
journalctl --user -u indexarr -f        # journal di systemd
```

La verbosità si cambia da **Logs → Runtime verbosity** (`debug`, `info`, `warn`,
`error`): viene applicata subito, salvata nel database e mantenuta al riavvio.
`log_level` in `indexarr.json` è solo il valore iniziale. Le API key, le
password e i token sono oscurati nel buffer mostrato dalla UI.

## Sicurezza

- La **API key Torznab** è obbligatoria per ricerca e download. `t=caps`
  risponde anche senza chiave, come altri client Torznab. Le chiavi aggiuntive
  sono revocabili singolarmente e il rate limit è per chiave.
- La UI accetta **HTTP Basic** se imposti utente e password in Settings. Senza
  credenziali è aperta a chiunque raggiunga la porta: usala su rete fidata o
  mettila dietro un reverse proxy con TLS.
- `GET /healthz`, `GET /metrics` e `/static/*` non passano dalla Basic auth:
  proteggi la rete o il reverse proxy. `/metrics` non contiene dati delle
  release, solo conteggi.
- Tutti i moduli POST richiedono un token CSRF legato al browser; l'upload di un
  backup multipart usa lo stesso token nella query, senza leggere il file prima
  del controllo.
- Le password sono salvate come `pbkdf2-sha256$iterazioni$salt$hash`.
- I segreti di indexer, proxy, app, client e notifiche sono nel vault, non in
  chiaro nel database.
- Il server non esegue codice caricato dagli indexer: XML e JSON sono solo dati.
- FlareSolverr riceve l'URL completo dell'indexer, **API key compresa**, e una
  singola risoluzione può durare fino a ~60 secondi.

## systemd

### Servizio systemd utente (consigliato)

Lo script crea e avvia il servizio per l'utente corrente, senza `sudo`:

```bash
./scripts/install-user-service.sh
```

Crea:

- `~/.config/systemd/user/indexarr.service`, abilitato al login;
- `data/indexarr.json` e `data/indexarr.db` con permessi privati (700/600);
- avvia il servizio e ne verifica `/healthz`.

```bash
./scripts/install-user-service.sh --listen 127.0.0.1:8788   # solo locale
./scripts/install-user-service.sh --no-build                # usa bin/indexarr
./scripts/install-user-service.sh --linger                  # resta attivo dopo il logout
./scripts/install-user-service.sh --status
./scripts/install-user-service.sh --uninstall               # i dati restano
```

Il default è `:8788` su tutte le interfacce: sulla LAN l'indirizzo è
`http://<IP-del-server>:8788/`. Lo script stampa anche l'indirizzo LAN rilevato
e verifica che la porta risponda lì, non solo su `127.0.0.1`. L'indirizzo passato
con `--listen` viene scritto nell'unità e prevale sul `listen` del file di
configurazione. Se vuoi limitarlo alla sola macchina locale usa
`--listen 127.0.0.1:8788` e riesegui lo script.

La UI è senza autenticazione finché non imposti utente e password in
**Settings**: fallo prima di esporla alla LAN.

Log e stato:

```bash
systemctl --user status indexarr
journalctl --user -u indexarr -f
```

Senza `--linger` il servizio parte con la sessione utente e si ferma al logout;
`--linger` lo mantiene attivo.

### Aggiornamento della produzione (dati conservati)

`scripts/upgrade.sh` aggiorna il binario **senza azzerare il database**. Di
default è un dry run: valida i prerequisiti, compila `cmd/indexarr` in una
directory temporanea e stampa il piano, senza fermare il servizio.

```bash
./scripts/upgrade.sh                       # dry run
./scripts/upgrade.sh --apply --yes         # aggiornamento della produzione
./scripts/upgrade.sh --rollback --yes      # ripristina l'ultimo backup
```

Con `--apply` il build avviene **prima** di fermare il servizio. Poi crea un
backup coerente con `VACUUM INTO` in `data/backups/pre-upgrade-<timestamp>.db`,
salva il binario precedente come `bin/indexarr.old-<timestamp>`, ferma il
servizio, installa il nuovo binario e riparte. Verifica `/healthz` con la
versione nuova, `t=caps` e una ricerca Torznab con la API key letta dal DB (mai
stampata). Se un controllo fallisce ripristina automaticamente DB e binario e
riavvia la versione precedente.

La prova generale su una copia usa `--data-dir`, `--port 18510`, `--no-systemd`
e binari temporanei; non tocca `:8788`, `data/`, `bin/indexarr` o systemd:

```bash
./scripts/upgrade_test.sh
```

Gli esiti delle tre prove (aggiornamento riuscito, build rotto, health fallito
con rollback automatico) sono in [scripts/README.md](scripts/README.md).

### Servizio systemd di sistema

Se preferisci un servizio globale, usa l'esempio
[examples/indexarr.service](examples/indexarr.service). Crea l'utente e la
directory, copia binario e unit:

```bash
sudo useradd --system --home /var/lib/indexarr --shell /usr/sbin/nologin indexarr
sudo install -d -o indexarr -g indexarr /var/lib/indexarr
sudo install -m 0755 bin/indexarr /usr/local/bin/indexarr
sudo install -m 0644 examples/indexarr.service /etc/systemd/system/indexarr.service
sudo systemctl daemon-reload
sudo systemctl enable --now indexarr
journalctl -u indexarr -f
```

Il file di configurazione e il database vengono creati in `/var/lib/indexarr`.

## Docker

```bash
docker compose up -d
```

Il volume nominato `indexarr-data` contiene `indexarr.json` e il database. In
alternativa:

```bash
docker build -t indexarr .
docker run -d --name indexarr -p 8788:8788 \
  -v indexarr-data:/data \
  indexarr
```

La porta predefinita è **8788**, scelta per evitare la 8484 usata da qBittorrent
in molte installazioni. Il default vale per le installazioni nuove: un
`indexarr.json` già esistente conserva il valore `listen` scritto al primo
avvio. Se aggiorni un container creato con la vecchia immagine, aggiorna il
mapping a `8788:8788` oppure aggiungi `-listen :8484` al comando per restare
sulla porta precedente.

Il volume **non** contiene la chiave del vault: in Docker usa
`INDEXARR_MASTER_KEY_FILE` con un secret montato (esempio in
[docs/VAULT.md](docs/VAULT.md#docker)) oppure `INDEXARR_MASTER_KEY`. Senza chiave
il servizio parte, ma gli indexer con segreti cifrati non sono leggibili.

## Sviluppo

```bash
make test    # go test ./...
make vet     # go vet ./...
make qa      # vet + test + smoke test HTTP sul binario reale
make build   # bin/indexarr
make release # binari linux/amd64, linux/arm64, windows/amd64, darwin/arm64
make ci      # CI locale, log in ci-logs/<commit>.log
```

Per diagnosi di memoria/goroutine si può esporre pprof solo su localhost:

```bash
INDEXARR_PPROF=127.0.0.1:6060 ./bin/indexarr -config data/indexarr.json
go tool pprof -top http://127.0.0.1:6060/debug/pprof/heap
```

Il CLI delle definizioni è `indexarr-def`:

```bash
go run ./cmd/indexarr-def validate examples/definitions
go run ./cmd/indexarr-def check-fixtures examples/definitions/html-forum.yaml
go run ./cmd/indexarr-def convert-cardigann -o ./native ./definizioni-da-studiare
go run ./cmd/indexarr-def fmt -w examples/definitions/html-forum.yaml
go run ./cmd/indexarr-def fingerprint examples/definitions/html-forum.yaml
go run ./cmd/indexarr-def schema
make def-validate   # definizioni di esempio e catalogo, se presente
```

Struttura dei pacchetti e scelte di progetto:
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Guida all'uso:
[docs/USAGE.md](docs/USAGE.md). API Torznab: [docs/API.md](docs/API.md);
API amministrativa JSON: [docs/ADMIN-API.md](docs/ADMIN-API.md). Suite di test e
smoke test: [docs/QA.md](docs/QA.md).

## Limiti

- Un solo processo per database (SQLite con una connessione serializzata).
- Non è un client BitTorrent, Usenet o eD2k: non scarica e non semina il
  contenuto.
- Il filtro per id esterni funziona sulle release che espongono quegli id; in
  loro assenza si ricade sul titolo.
- Le categorie RSS sono solo numeriche: le categorie testuali dei feed non
  producono un id Torznab valido e vengono ignorate; **Default category** copre
  i feed e i listing che non ne dichiarano una.
- La ricerca live interroga solo sorgenti che espongono un protocollo di ricerca
  (Torznab/Newznab); le fonti RSS/HTML alimentano la cache ma non rispondono a
  una query manuale. La ricerca manuale accetta id esterni e campi
  musicali/libri, ma non traduce da sola un id in un titolo per i siti che non
  lo supportano.
- La vista live manuale mostra al massimo 50 righe e non usa la paginazione
  normale; gli indexer in timeout continuano in background e richiedono un
  ricaricamento per mostrare i risultati.
- Il ripristino di un backup richiede il riavvio del processo; con systemd
  `Restart=always` è automatico, altrimenti va riavviato a mano.
- I seeder non riportati dall'indexer (per esempio MirCrew) restano sconosciuti:
  l'attributo `seeders` viene omesso nella risposta Torznab invece di valere 0.
- Cloudflare è gestito con FlareSolverr per RSS e indexer diretti; le API
  proprietarie restano in coda finché non esiste una definizione nativa, e
  indexarr non include un browser headless.
- Il catalogo non ha ancora definizioni native per singoli siti Usenet: il
  percorso Newznab generico è completo, la copertura dei siti cresce con il
  catalogo.
- I client di download non hanno ancora una selezione per singolo indexer né
  una priorità/round-robin tra client dello stesso protocollo.
- Le notifiche hanno un solo insieme globale di eventi: non c'è ancora una
  matrice evento × canale.
- SMTP supporta STARTTLS, non TLS implicito sulla porta 465.

## Documentazione

- [Guida alle definizioni](docs/DEFINITIONS.md) — formato v1, fixture, sicurezza
- [MirCrew](docs/MIRCREW.md) — definizione nativa, Smartfeed, sessione
  FlareSolverr
- [Compatibilità](docs/COMPATIBILITY.md) — fonti, casi limite, diagnostica
- [Architettura](docs/ARCHITECTURE.md) — pacchetti e scelte di progetto
- [Uso](docs/USAGE.md) — guida passo passo
- [API Torznab](docs/API.md) e [API amministrativa](docs/ADMIN-API.md)
- [Vault](docs/VAULT.md) — chiave, backup, Docker secrets
- [QA](docs/QA.md) — test e smoke test
- [Migrazione da Jackett/Prowlarr](docs/MIGRATION-MANAGERS.md)
