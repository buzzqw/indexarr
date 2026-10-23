# indexarr

Indicizzatore torrent self-hosted, compatibile **Torznab**. Interroga periodicamente
feed RSS, Jackett e Prowlarr, salva release, magnet e file `.torrent` in un database
SQLite e le espone a Sonarr, Radarr, Lidarr o a qualunque client Torznab. Include
un'interfaccia web per cercare nel database, gestire le sorgenti e fare manutenzione.

```
RSS / Jackett / Prowlarr ──► scheduler ──► normalizzazione ──► SQLite
                                                                 │
                        Sonarr / Radarr / Lidarr ◄── Torznab ────┘
                                     Browser ◄── UI web SSR ─────┘
```

## Cosa fa

- **Sorgenti multiple**: feed RSS/Atom, Jackett, Prowlarr (API JSON e feed Torznab
  per indice), MirCrew, qualunque endpoint Torznab diretto, elenchi HTML ExtTo,
  TorrentGalaxy e Corsaro; importazione massiva di elenchi RSS e OPML.
- **Quick add e scoperta feed**: incolla un URL e indexarr riconosce Torznab,
  RSS o listing HTML; “Discover feeds” estrae i feed dichiarati da una pagina.
- **Acquisizione periodica**: ogni sorgente ha il proprio intervallo; al primo
  avvio parte subito. Dopo un errore ritenta con backoff.
- **Cache SQLite**: titolo, dimensione, seeder/leecher, categoria, data, GUID,
  infohash, id esterni (IMDb/TMDB/TVDB), magnet e file `.torrent`.
- **Deduplica**: per infohash quando disponibile, altrimenti per GUID/URL/titolo;
  lo stesso torrent visto da più sorgenti non crea righe duplicate.
- **Server Torznab completo**: `caps`, `search`, `tvsearch`, `movie`, `get`,
  download `.torrent`, paginazione, filtri per categoria, stagione/episodio e id
  esterni. Compatibile con i client Sonarr/Radarr/Lidarr; ricerca live opzionale
  sugli indexer quando la cache è vuota.
- **Interfaccia web**: dashboard, ricerca live con filtri e ricerche salvate,
  indice full-text **FTS5** (tokenizer trigram) sulla cache, dettaglio release
  con preferiti/blocklist e invio al client torrent, CRUD
  sorgenti con test, clone, import/export, storico esecuzioni, log in-app,
  impostazioni e manutenzione.
- **Operatività**: API key multiple con revoca e rate limit, notifiche
  webhook/Telegram, auto-disable delle sorgenti, backup automatici con rotazione
  e ripristino dalla UI, metriche Prometheus.
- **Sicurezza**: API key per Torznab, HTTP Basic opzionale per la UI, protezione
  CSRF sui moduli, password con PBKDF2-SHA256.
- **Cloudflare**: fallback automatico via FlareSolverr per RSS e indexer diretti,
  con esclusione degli indirizzi LAN.
- **Un solo binario**: Go puro, SQLite senza CGO, nessun servizio esterno
  obbligatorio. L'interfaccia è installabile come PWA.

Non scarica il contenuto dei torrent: costruisce un **indice** interrogabile. È il
ruolo di un client torrent (qBittorrent, Transmission, gx-torrent…) scaricare i file.

## Compatibilità

indexarr parla **Torznab**, lo standard di fatto per gli indexer torrent. Questo è
il quadro reale, non un elenco di intenti:

| Sorgente | Supporto | Come |
| --- | --- | --- |
| Jackett | completo | base Jackett (percorso aggregato automatico) o URL del singolo indice |
| Prowlarr | completo | API JSON per le ricerche; feed Torznab dei singoli indici per le "ultime" |
| MirCrew (`mircrew-indexer`) | completo | base o `/api`, magnet in `<link>`/`<enclosure>`, seeder assenti, `/status` |
| Torznab diretto | completo (torrent) | endpoint completo o base: `/api` viene aggiunto se manca |
| ExtTo (`extto.org`) | completo (listing HTML) | parser dedicato: titolo e magnet dalla stessa riga, nessuna modifica all'URL |
| TorrentGalaxy | completo (listing HTML) | parser dedicato delle pagine `post-detail`, con cache dei magnet |
| Corsaro | completo (listing HTML) | parser dedicato di titoli `/torrent/` e magnet diretti, con cache dei dettagli |
| Newznab usenet | non supportato | solo torrent: le voci usenet vengono scartate |
| RSS / Atom | completo | feed normali, enclosure `.torrent` o magnet |
| Indexer privati con Basic | completo | campi *HTTP username/password* della sorgente |
| API key in query, header o URL | completo | `apikey` viene inviata come parametro e come `X-Api-Key` |
| Cloudflare / challenge | con FlareSolverr | retry automatico per RSS e indexer diretti; gli indirizzi LAN non passano dal solver |
| API proprietarie non Torznab | non gestite | esporle tramite Jackett/Prowlarr |

Per gli indexer che non parlano Torznab la strada è **Jackett o Prowlarr**: una
volta esposti lì, indexarr li usa come qualunque altro endpoint.

Dettagli, casi limite e diagnostica: [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md).

## Requisiti

- Go 1.26 o successivo per compilare (il binario non ha dipendenze runtime).
- Un filesystem scrivibile per il database.

## Avvio rapido

```bash
make build
./bin/indexarr
```

Al primo avvio vengono creati, nella directory corrente:

- `indexarr.json` — configurazione minima (porta, percorso del database, log);
- `indexarr.db` — database SQLite;
- una API key Torznab casuale, stampata nel log e visibile in **Settings**.

Apri <http://localhost:8484>, aggiungi una sorgente e premi **Run**. La stessa
pagina **Settings** mostra l'URL Torznab da consegnare ai client.

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
  "listen": ":8484",
  "db_path": "indexarr.db",
  "log_level": "info"
}
```

Un `db_path` relativo è risolto rispetto alla directory del file di configurazione.
Tutto il resto (API key, intervalli, retention, credenziali UI) è nel database e si
modifica dalla pagina **Settings**, senza riavviare.

| Impostazione | Default | Significato |
| --- | --- | --- |
| `default_interval_seconds` | 3600 | Intervallo delle sorgenti che non ne hanno uno proprio |
| `fetch_timeout_seconds` | 45 | Timeout per singola richiesta HTTP |
| `workers` | 2 | Sorgenti eseguite in parallelo (richiede riavvio) |
| `max_torrents_per_run` | 30 | Limite di `.torrent` scaricati per esecuzione |
| `max_torrent_bytes` | 8 MiB | Dimensione massima di un `.torrent` |
| `fetch_torrents` | on | Scarica i `.torrent` quando la sorgente offre solo un link |
| `retention_days` | 0 | Elimina le release non più viste dopo N giorni (0 = mai) |
| `public_url` | vuoto | URL pubblico usato nei link Torznab (dietro reverse proxy) |
| `notify_webhook_url` | vuoto | POST JSON per nuove release e fallimenti |
| `notify_telegram_token` / `notify_telegram_chat_id` | vuoto | Notifiche Telegram (entrambi o nessuno) |
| `notify_new_releases` | on | Invia una notifica per le nuove release |
| `notify_failures` | on | Invia una notifica quando un run fallisce |
| `auto_disable_after` | 0 | Disabilita una sorgente dopo N errori consecutivi (0 = mai) |
| `flaresolverr_url` | vuoto | Endpoint FlareSolverr per RSS/indexer dietro Cloudflare (`http://127.0.0.1:8191`) |
| `torznab_download_factor` | 0 | `downloadvolumefactor` (0 = freeleech) |
| `torznab_upload_factor` | 1 | `uploadvolumefactor` |
| `api_rate_limit_per_minute` | 0 | Richieste per chiave API al minuto (0 = illimitate) |
| `torznab_live_search` | off | Se la cache è vuota, interroga gli indexer Torznab al momento |
| `torznab_live_timeout_seconds` | 20 | Deadline complessiva della ricerca live |
| `download_client` | none | `qbittorrent` o `transmission` per il pulsante “Send to…” |
| `download_client_url` / `_user` / `_password` | vuoto | Endpoint e credenziali del client torrent |
| `download_client_category` / `_save_path` / `_paused` | vuoto/false | Categoria qBittorrent, percorso e avvio in pausa |
| `backup_enabled` | on | Backup automatico del database |
| `backup_interval_hours` | 24 | Intervallo dei backup automatici (6–168) |
| `backup_keep` | 7 | Numero di backup automatici conservati (1–60) |
| `log_level` | config | Livello di log runtime (`debug`, `info`, `warn`, `error`) |

## Sorgenti

Ogni sorgente può avere credenziali **HTTP Basic** (campi *HTTP username* e
*HTTP password*): servono per feed e indexer privati che non usano una API key.
Sono indipendenti dalla API key Torznab.

Accanto a ogni sorgente ci sono **Test** (prova quella sorgente e mostra l'esito
nel messaggio) e **Run**. **Test all** prova tutte le sorgenti abilitate in
background, con concorrenza limitata, e scrive ogni esito nel tab **Logs**: non
blocca il browser e si può seguire mentre gira.

Nel form di creazione/modifica ci sono **Save and test** e **Save and run**: il
risultato arriva subito nel messaggio, senza passare dalla lista. Il campo
**Default category** assegna una categoria Torznab alle release RSS/HTML che non
ne dichiarano una (per esempio una pagina Corsaro o un feed generico).

### Quick add, preset e scoperta feed

- **Quick add** in cima a Sources: incolla un URL; indexarr riconosce ExtTo,
  TorrentGalaxy, Corsaro, i feed RSS/Atom e gli endpoint Torznab. Nome e API key
  sono opzionali, il primo run viene accodato subito.
- **Preset** nel form sorgente: Prowlarr, Jackett, MirCrew, Torznab diretto, RSS,
  ExtTo, TorrentGalaxy, Corsaro. Riempiono tipo e manager; l'URL resta da
  incollare. **Detect type from URL** imposta tipo e manager dal valore scritto.
- **Discover feeds**: dato l'URL di una pagina, estrae `<link rel="alternate">`
  e i link RSS/Atom e li propone con un pulsante **Add as RSS**.
- Se **5 o più sorgenti condividono lo stesso host**, la pagina mostra un avviso:
  i siti possono rispondere `429`; usa le caselle di selezione per disabilitare,
  unire o eliminare i listing che non servono e allunga l'intervallo. Un URL già
  presente viene rifiutato dal quick add (la creazione manuale resta possibile).

### Aggiunta massiva di feed RSS/OPML

Da **Sources → Bulk add RSS** si incolla un elenco (una URL per riga) oppure un
file OPML completo:

```text
https://example.org/feed.xml
My feed | https://example.org/other.xml
# le righe che iniziano con # sono ignorate
https://another.example/rss | Nome alternativo
```

- `Nome | URL` assegna il nome; senza nome viene usato il dominio
  (`example.org`, senza `www.`).
- Usa URL di feed, non pagine del sito, per un RSS normale: una pagina funziona
  solo se espone un link RSS/Atom (spesso `/rss` o `/feed`); altrimenti il log
  dice chiaramente che è una pagina HTML. Fanno eccezione le pagine ExtTo,
  TorrentGalaxy e Corsaro, che hanno un parser dedicato.
- Un URL già presente viene saltato: reimportare la stessa lista è sicuro.
- I nomi duplicati ricevono un suffisso (`example.org (2)`).
- Un OPML viene letto ricorsivamente: ogni `xmlUrl`, anche negli outline
  annidati, diventa una sorgente; `text`/`title` diventano il nome.
- Intervallo, **Enabled** e **Run immediately** si applicano a tutti i feed
  creati.
- Gli URL non validi finiscono nel report e non bloccano gli altri.
- Dopo un'importazione parziale la pagina resta aperta con il report:
  correggi le righe e reinvia; i feed già creati vengono riconosciuti e saltati.

### Gestione massiva e import/export delle sorgenti

Nella pagina **Sources**:

- le caselle di selezione permettono di **abilitare, disabilitare, cambiare
  intervallo o categorie** e **eliminare** più sorgenti in un colpo;
- **Clone** duplica una sorgente (disabilitata, con nome univoco) per provare
  un'altra configurazione;
- **Export JSON** scarica tutte le sorgenti, comprese API key e credenziali HTTP:
  conserva il file in un posto sicuro;
- il pannello **Import / export** accetta lo stesso JSON e salta nome o URL già
  presenti; le voci non valide sono riportate in **Logs**.

### Liste HTML: ExtTo, TorrentGalaxy e Corsaro

Alcuni siti non espongono RSS né Torznab, ma solo pagine HTML. Per **ExtTo**
(`extto.org` / `ext.to`), **TorrentGalaxy** e **Corsaro** indexarr usa parser
dedicati, come Gextto:

- **ExtTo**: ogni riga della listing contiene il link al dettaglio e il magnet;
  vengono accoppiati senza scaricare le pagine dettaglio.
- **TorrentGalaxy**: la pagina utente contiene solo link `post-detail`; indexarr
  scarica i dettagli (6 alla volta), estrae il magnet e lo tiene in cache per
  6 ore, così i giri successivi non ripetono le stesse richieste.
- **Corsaro**: parser delle righe con link `/torrent/` o `/torrents/` e dei
  magnet diretti; i dettagli vengono scaricati in parallelo e memorizzati in
  cache. Il titolo viene dal testo dell'anchor o dal parametro `dn`.
- Vengono lette la prima pagina (fino a 50 release) e la categoria dal filtro
  ExtTo (`filter=c=TV` → 5000, ecc.); per le altre si può usare **Default
  category** nel form della sorgente.
- Le URL esistenti funzionano così come sono; non serve cambiarle.

Le altre pagine HTML non vengono scrapate: per quelle servono Jackett/Prowlarr.
Se il sito cambia layout o richiede il login, il test lo segnala.

### MirCrew (`mircrew-indexer`)

MirCrew è un indexer Torznab diretto. Configurazione tipica:

- **Type**: Torznab / indexer
- **URL**: base del servizio, per esempio `http://192.168.1.10:8090`; è accettato
  anche l'URL che termina in `/api`
- **Manager**: MirCrew (o Direct Torznab, o Auto)
- **API key**: quella del servizio, se richiesta; può restare vuota
- **Categorie**: per esempio `5000`

Particolarità gestite:

- il magnet può arrivare in `<link>` o in `<enclosure>` invece che in un attributo
  `magneturl`;
- i seeder possono mancare: restano **sconosciuti**, non vengono inventati a zero;
- il pulsante **Test** interroga anche `/status` e segnala login al forum fallito,
  ancora in corso o perso;
- il servizio può impiegare decine di secondi (cerca sul forum): il timeout
  predefinito è 45 secondi;
- XML con `&` grezzi o entità HTML viene ripulito prima del parsing.

Se il test dice «forum login failed», il problema è nel servizio MirCrew
(credenziali del forum), non in indexarr: l'API Torznab continua a rispondere
dall'indice locale.

### Cloudflare / FlareSolverr

L'endpoint si imposta in **Settings → Cloudflare / FlareSolverr → FlareSolverr URL**,
per esempio `http://127.0.0.1:8191`.

Quando un **feed RSS** o un **indexer diretto** risponde con una challenge
Cloudflare, indexarr ripete la stessa richiesta tramite FlareSolverr e usa la
pagina risolta:

- vale per RSS, Torznab diretto, Prowlarr (JSON e feed per indice) e per il
  pulsante **Test**;
- riconosce l'header `cf-mitigated: challenge` e le pagine HTML con i marcatori
  Cloudflare (`Just a moment...`, `cf-chl`, `Attention Required`), anche su 403,
  503 o 200;
- un errore XML/JSON dell'indexer (per esempio API key errata) **non** viene
  confuso con una challenge;
- gli indirizzi LAN (`localhost`, IP privati, `.local`, `.lan`) non passano mai
  dal solver: un 403 locale è un errore reale, non Cloudflare;
- Jackett e Prowlarr spesso risolvono Cloudflare al loro interno: non serve
  passarli dal solver.

Accanto al campo c'è **Test FlareSolverr**: risolve `https://example.com/`
attraverso il servizio e riporta OK o l'errore; non salva l'URL (serve **Save
settings**) e può richiedere fino a un minuto. Anche l'esito del test finisce nel
tab **Logs**.

Limiti: FlareSolverr riceve l'URL completo dell'indexer, **API key compresa**, e
una singola risoluzione può durare fino a ~60 secondi. I download `.torrent` non
passano dal solver (risposta binaria): se un indexer richiede il Cloudflare
clearance anche per il file, usare il magnet o Jackett/Prowlarr.

### Jackett

- **Type**: Torznab / indexer
- **URL**: base di Jackett, per esempio `http://192.168.1.10:9117`
- **Manager**: Jackett (oppure Auto)
- **API key**: quella mostrata da Jackett

indexarr aggiunge da solo `/api/v2.0/indexers/all/results/torznab/api`.
Per interrogare un singolo indice Jackett, incolla l'URL Torznab completo di
quell'indice: qualunque URL che contiene `/api` o `torznab` viene usato così com'è.

### Prowlarr

- **URL**: base di Prowlarr, per esempio `http://192.168.1.10:9696`
- **Manager**: Prowlarr (oppure Auto, riconosciuto dalla porta 9696)
- **API key**: quella di Prowlarr

Con un termine di ricerca usa l'API JSON aggregata. **Senza termine** (modalità
"ultime release") elenca gli indice torrent abilitati e interroga il feed Torznab
di ciascuno: è l'unico modo per popolare la cache senza una query.
Per un singolo indice Prowlarr incolla il suo URL Torznab completo
(`http://prowlarr:9696/<id>/api`).

### Torznab diretto

Qualunque servizio che parla Torznab e non è Jackett/Prowlarr (per esempio un
indexer locale):

- **URL**: endpoint completo, per esempio `http://indexer.local:8080/api`
- **Manager**: Direct Torznab (o Auto)
- **Categorie**: id Torznab separati da virgola (`5000, 2000`)

### RSS / Atom

- **Type**: RSS / Atom feed
- **URL**: il feed
- Non servono manager, categorie o query.

Vengono letti titolo, magnet, enclosure `.torrent`, dimensione, categoria numerica,
data ed eventuali attributi `torznab:attr`. Le voci che non contengono né magnet né
link di download (pagine web, articoli) vengono ignorate.

## Collegare Sonarr / Radarr / Lidarr

Nella pagina **Settings** copia l'endpoint completo:

```text
http://<host>:8484/api?apikey=<API_KEY>
```

Nel client aggiungi un indexer di tipo **Torznab**:

| Campo | Valore |
| --- | --- |
| URL | `http://<host>:8484/api` |
| API Key | la chiave mostrata in Settings |
| Categories | `5000` TV, `2000` Film, `3000` Audio, `7000` Libri |

Sono supportati anche l'alias `/torznab/api` e il parametro `apikey` passato
nell'header `X-Api-Key`. Dettagli completi in [docs/API.md](docs/API.md).

### API key multiple e rate limit

In **Settings → Torznab API** la chiave primaria resta quella mostrata
nell'endpoint. Sotto, **Additional API keys** permette di crearne una per client
(Sonarr, Radarr, Lidarr, uno script) con nome, data di creazione, ultimo uso e
numero di richieste. Ogni chiave aggiuntiva è revocabile senza toccare le altre;
i link `.torrent` della risposta usano la chiave con cui è stata fatta la
richiesta. **API requests per key per minute** applica un limite per chiave
(0 = illimitato): oltre la soglia la risposta è HTTP 429 con `Retry-After`.

### Ricerca live Torznab

Con **Search indexers live on a cache miss** attivo, una ricerca Torznab che non
trova nulla nella cache interroga al momento le sorgenti Torznab abilitate
(Jackett, Prowlarr, MirCrew, diretti), salva i risultati e ripete la query. Le
sorgenti RSS/HTML sono escluse perché non accettano un termine di ricerca.
`Live search timeout` limita l'attesa complessiva; le sorgenti lente vengono
ignorate. È spenta per default perché può aggiungere secondi alla risposta del
client. La query deve essere presente: una richiesta solo per IMDb/TMDB/TVDB id
non attiva la ricerca live.

### Invio ai client torrent

In **Settings → Download client** si sceglie **qBittorrent** o **Transmission**,
si inseriscono URL e credenziali e, per qBittorrent, categoria e percorso. Sul
dettaglio di ogni release compare **Send to qBittorrent/Transmission**: viene
inviato il `.torrent` salvato, oppure il magnet se il file non è in cache.
qBittorrent usa la Web API v2; Transmission il RPC con negoziazione della sessione.
L'operazione è registrata nei log.

## Interfaccia web

| Pagina | Contenuto |
| --- | --- |
| **Dashboard** | Totali, stato delle sorgenti, ultime release ed esecuzioni |
| **Releases** | Ricerca live, esclusione `-xxx`, regex, filtri avanzati (qualità, lingua, seeders, dimensione), ricerche salvate, preferiti/nascosti; dettaglio con magnet e `.torrent` |
| **Sources** | Stato, Run/Test/Clone/Edit, selezione multipla, import/export JSON, import massivo RSS/OPML, quick add, preset, scoperta feed e categoria predefinita |
| **Runs** | Storico con esito, conteggi (inclusi i bloccati), durata, errore, dettaglio e annullamento |
| **Rules** | Blocklist per infohash, titolo, regex, sorgente o categoria; le release bloccate non vengono più inserite |
| **Logs** | Ultimi 2000 messaggi, filtro per livello/testo, limite righe, auto-refresh, download, pulizia e livello di log runtime |
| **Settings** | API key primaria e multiple con rate limit, endpoint Torznab, ricerca live, intervalli, limiti, retention, credenziali UI, notifiche, auto-disable, client torrent e backup |
| **Maintenance** | Integrity check, vacuum, optimize/checkpoint, reindex, deduplica, pulizia `.torrent`, retention, salute sorgenti, backup automatici, download/delete e ripristino |

L'interfaccia è una **PWA**: manifest, icona e service worker che mette in cache
solo gli asset statici; le pagine HTML restano sempre di rete perché contengono
CSRF e dati privati. Si può installare dal browser senza un server aggiuntivo.

## Ricerca nelle release

La pagina **Releases** cerca mentre scrivi: la richiesta parte dopo 250 ms, quella
precedente viene annullata e viene aggiornata solo la tabella, senza ricaricare la
pagina. L'URL resta condivisibile e il modulo funziona anche senza JavaScript.

- **Esclusione**: `-xxx` toglie i risultati che contengono `xxx`. Si combinano più
  termini: `show -1080p -cam`.
- **Full-text**: i termini di almeno tre caratteri sono cercati con FTS5 e
  tokenizer trigram, quindi anche come sottostringhe (`how` trova `show`); i
  termini più corti (`4k`, `tv`) usano il fallback `LIKE`.
- **Regex**: spunta **Regex**; ogni termine è un'espressione regolare RE2,
  case-insensitive per default. I termini sono in AND, `-pattern` esclude, le
  virgolette mantengono gli spazi (`"the show"`) e `/pattern/` è accettato.
  Esempi: `S0[12]E0[12]`, `1080p|2160p`, `^The Show`.
- Un'espressione non valida mostra l'errore senza svuotare la pagina.
- Sorgente, categoria e ordinamento si combinano con il testo; la paginazione
  resta sulla pagina completa.

La sintassi `-termine` e la modalità regex sono della pagina Releases: l'endpoint
Torznab continua a usare i parametri standard per i client (Sonarr/Radarr).

### Filtri avanzati e ricerche salvate

Sotto la ricerca ci sono i filtri strutturati:

- risoluzione (`2160p/4K`, `1080p`, `720p`, `480p`);
- fonte (`WEB-DL`, `BluRay`, `HDTV`);
- lingua (`Italian`, `English`);
- seeders minimi, dimensione minima e massima in MB;
- **Favorites** e **Show hidden**.

Si combinano con il testo e con la modalità regex; la ricerca live li applica
subito. **Save this search as…** salva la vista con un nome; le ricerche salvate
compaiono sopra i risultati come link pronti, con una × per eliminarle. La
pallina ★ indica le release preferite.

### Regole, preferiti e nascosti

Il tab **Rules** gestisce la blocklist:

- per **infohash** (preciso), **titolo** (sottostringa), **regex** (Go/RE2,
  case-insensitive), **nome sorgente** o **categoria** (`5000` copre `5xxx`);
- le release che corrispondono non vengono più inserite nei run successivi;
- sul dettaglio di una release ci sono **Favorite**, **Hide** e **Block release**:
  Block aggiunge la regola (infohash se disponibile, altrimenti titolo) e la
  nasconde subito.

Nascondere una release la toglie dalle viste normali ma non impedisce a un nuovo
run di reinserirla: per questo esiste la blocklist. Le release bloccate sono
contate nella colonna **Blocked** dei run.

## Notifiche e auto-disable

In **Settings → Notifications**:

- **Webhook URL**: riceve un POST JSON con `event`, `source`, `count`, `titles`,
  `error` e `at`;
- **Telegram bot token** e **chat ID**: messaggio leggibile; vanno impostati
  entrambi o nessuno;
- **Notify new releases** e **Notify failures** scelgono gli eventi;
- **Auto-disable after N consecutive failures**: dopo N run falliti la sorgente
  viene disabilitata; un run riuscito azzera il contatore. Il token Telegram è
  oscurato negli errori di log.

## Metriche e diagnostica

- `GET /metrics`: formato Prometheus (`indexarr_releases`, `indexarr_sources`,
  `indexarr_runs_24h`, `indexarr_errors_24h`, `indexarr_blocklist_rules`,
  `indexarr_db_bytes`, uptime…), senza autenticazione come `/healthz`;
- **Runs → dettaglio**: conteggi, durata ed errore completo di un singolo run;
- **Cancel** nella riga di una sorgente in esecuzione annulla il run;
- **Logs** mostra gli esiti di Test/Test all e gli errori di notifica.

## Log

Il tab **Logs** mostra gli ultimi **2000** messaggi del processo, dal più recente:

- filtro per livello (`debug`, `info`, `warning`, `error`), per **origine**
  (`indexarr`, `indexer`/Torznab, RSS/HTML, notifiche) e ricerca nel messaggio
  e negli attributi;
- pulsante **Problems** con il conteggio di warning ed errori che corrispondono
  ai filtri correnti;
- limite righe (100/300/1000) e **auto-refresh** ogni 10 secondi;
- **Download .txt** e **Clear** del buffer;
- qui arrivano anche gli esiti di **Test all** e del **Test FlareSolverr**.

Ogni errore di una sorgente riporta `source`, `kind`, `host` e il messaggio
originale dell'indexer o del feed: un run fallito è a livello `error`, mentre i
problemi parziali (singolo item scartato, download `.torrent` fallito, live
search) sono a livello `warning`. Gli errori interni usano `component=indexarr`,
quelli degli indexer `component=indexer` e quelli dei feed `component=rss`.

Il buffer è in memoria e si svuota al riavvio. Le stesse righe vanno su stdout e,
con il servizio systemd, nel journal:

```bash
journalctl --user -u indexarr -f
```

La verbosità si cambia da **Logs → Runtime verbosity** (`debug`, `info`, `warn`,
`error`): viene applicata subito, salvata nel database e mantenuta al riavvio.
`log_level` in `indexarr.json` è solo il valore iniziale. Le API key, le password
e i token sono oscurati nel buffer mostrato dalla UI.

## Manutenzione

- **Database report**: dimensioni, pagine libere, WAL/SHM, conteggio righe per
  tabella e ripartizione delle release (torrent, magnet, senza link, nascoste,
  preferite).
- **Integrity check**: `PRAGMA integrity_check`, riporta `ok` o il problema.
- **Quick check / Foreign key check**: controlli rapidi su integrità e
  violazioni di chiave esterna.
- **Vacuum**: ricompatta il file SQLite.
- **Deduplica per infohash**: unisce righe duplicate conservando magnet, torrent e
  il maggior numero di osservazioni.
- **Pulizia `.torrent`**: elimina i blob conservando metadati e magnet (utile per
  ridurre il database).
- **Retention**: elimina le release non più viste da N giorni; gira anche da sola
  ogni sei ore. La pulizia manuale può usare una finestra diversa o eliminare
  tutte le release di una singola sorgente.
- **Pulizia mirata**: elimina le release senza magnet né `.torrent` (inutilizzabili
  dai client) e quelle nascoste; la cronologia run può essere svuotata oltre che
  per età anche con un numero di giorni personalizzato.
- **Optimize / checkpoint**: `PRAGMA optimize` + `wal_checkpoint(TRUNCATE)`, sicuro
  a caldo; il checkpoint WAL è disponibile anche da solo.
- **Analyze**: `ANALYZE` per rigenerare le statistiche del query planner.
- **Rebuild indexes**: `REINDEX`.
- **Clear source errors**: azzera `last_error` e il contatore di errori
  consecutivi; non riabilita una sorgente disabilitata.
- **Reset API usage**: azzera i contatori di richieste per chiave, senza
  revocare le chiavi.
- **Recount source items**: ricalcola `total_items` dal numero di release
  attualmente in cache per ogni sorgente.
- **Salute sorgenti**: la pagina elenca le sorgenti con errori, il numero di
  fallimenti e i pulsanti Test/Run.
- **Backup automatici**: crea un backup consistente ogni N ore (default 24,
  conserva gli ultimi 7) in `data/backups`; **Backup now**, elenco, download ed
  eliminazione dalla pagina Maintenance. I file `pre-restore-*.db` non vengono
  ruotati.
- **Ripristino dalla UI**: carica un `.db` validato; viene copiato in
  `indexarr.db.restore`, il servizio si riavvia e il file viene applicato prima
  di aprire il database. Il database corrente viene copiato in
  `data/backups/pre-restore-*.db`. L'unità systemd usa `Restart=always` per
  completare il riavvio; senza systemd basta riavviare a mano.
- **Backup manuale**: `VACUUM INTO` su file temporaneo e download dal browser.

## Sicurezza

- La **API key Torznab** è obbligatoria per ricerca e download. `t=caps` risponde
  anche senza chiave, come fanno Jackett e Prowlarr. Le chiavi aggiuntive sono
  revocabili singolarmente e il rate limit è per chiave.
- La UI accetta HTTP Basic se imposti utente e password in Settings. Senza
  credenziali è aperta a chiunque raggiunga la porta: usala su rete fidata o
  mettila dietro un reverse proxy con TLS.
- `GET /healthz`, `GET /metrics` e `/static/*` non passano dalla Basic auth:
  proteggi la rete o il reverse proxy. `/metrics` non contiene dati delle release,
  solo conteggi.
- Tutti i moduli POST richiedono un token CSRF legato al browser; l'upload di un
  backup multipart usa lo stesso token nella query, senza leggere il file prima
  del controllo.
- Le password sono salvate come `pbkdf2-sha256$iterazioni$salt$hash`.
- Il server non esegue codice caricato dagli indexer: XML e JSON sono solo dati.

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
./scripts/install-user-service.sh --listen 127.0.0.1:8484   # solo locale
./scripts/install-user-service.sh --no-build                # usa bin/indexarr
./scripts/install-user-service.sh --linger                  # resta attivo dopo il logout
./scripts/install-user-service.sh --status
./scripts/install-user-service.sh --uninstall               # i dati restano
```

Il default è `:8484` su tutte le interfacce: sulla LAN l'indirizzo è
`http://<IP-del-server>:8484/` (per esempio `http://192.168.1.10:8484/`). Lo
script stampa anche l'indirizzo LAN rilevato e verifica che la porta risponda lì,
non solo su `127.0.0.1`. L'indirizzo passato con `--listen` viene scritto
nell'unità e prevale sul `listen` del file di configurazione. Se vuoi limitarlo
alla sola macchina locale usa `--listen 127.0.0.1:8484` e riesegui lo script.
La UI è senza autenticazione finché non imposti utente e password in **Settings**:
fallo prima di esporla alla LAN.

Log e stato:

```bash
systemctl --user status indexarr
journalctl --user -u indexarr -f
```

Senza `--linger` il servizio parte con la sessione utente e si ferma al logout;
`--linger` lo mantiene attivo (su molte distribuzioni richiede autorizzazione una
volta sola).

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
docker run -d --name indexarr -p 8484:8484 \
  -v indexarr-data:/data \
  indexarr
```

## Sviluppo

```bash
make test    # go test ./...
make vet     # go vet ./...
make qa      # vet + test + smoke test HTTP sul binario reale
make build   # bin/indexarr
make release # binari linux/amd64, linux/arm64, windows/amd64, darwin/arm64
```

Per diagnosi di memoria/goroutine si può esporre pprof solo su localhost:

```bash
INDEXARR_PPROF=127.0.0.1:6060 ./bin/indexarr -config data/indexarr.json
go tool pprof -top http://127.0.0.1:6060/debug/pprof/heap
```

Struttura dei pacchetti e scelte di progetto: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
Suite di test e smoke test: [docs/QA.md](docs/QA.md).
Audit operativo del 23/10/2026: [docs/AUDIT-2026-10-23.md](docs/AUDIT-2026-10-23.md).

## Prossimi passi

Funzioni ancora aperte, in ordine di valore:

- interfaccia multilingua (almeno it/en) con catalogo completo; oggi la UI è in
  italiano e i messaggi del server sono in inglese;
- motore di selettori CSS configurabili per siti HTML arbitrari (ExtTo,
  TorrentGalaxy e Corsaro hanno già parser dedicati);
- parser dedicati per altri siti non RSS, da aggiungere quando servono.

## Limiti

- Un solo processo per database (SQLite con una connessione serializzata).
- Non è un client BitTorrent: non scarica né semina il contenuto.
- Il filtro per id esterni funziona sulle release che espongono quegli id; in loro
  assenza si ricade sul titolo.
- Le categorie RSS sono solo numeriche: le categorie testuali dei feed non
  producono un id Torznab valido e vengono ignorate; **Default category** copre i
  feed e i listing che non ne dichiarano una.
- La ricerca live Torznab interroga solo sorgenti Torznab e richiede un termine
  `q`; non traduce in ricerca gli id esterni da soli.
- Il ripristino di un backup richiede il riavvio del processo; con systemd
  `Restart=always` è automatico, altrimenti va riavviato a mano.
- I seeder non riportati dall'indexer (per esempio MirCrew) restano sconosciuti:
  l'attributo `seeders` viene omesso nella risposta Torznab invece di valere 0.
- Cloudflare è gestito con FlareSolverr per RSS e indexer diretti; le API
  proprietarie restano da esporre tramite Jackett/Prowlarr, e indexarr non
  include un browser headless.
