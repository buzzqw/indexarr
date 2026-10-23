# API Torznab di indexarr

indexarr espone un server Torznab (newznab-compatible) sul percorso `/api`. Lo
stesso handler risponde su `/torznab/api`, quindi entrambi gli URL funzionano.

- Base: `http://<host>:8484`
- Endpoint di ricerca: `http://<host>:8484/api`
- Alias: `http://<host>:8484/torznab/api`
- Autenticazione: parametro `apikey` oppure header `X-Api-Key`
- Risposta: XML `rss` con namespace `torznab` e `newznab`

La chiave primaria si trova in **Settings → Torznab API** e può essere rigenerata
(tutti i client che la usano vanno aggiornati). In **Additional API keys** si
creano chiavi separate per client, revocabili singolarmente. I link `.torrent`
della risposta usano la chiave con cui è arrivata la richiesta, quindi una chiave
aggiuntiva funziona anche per il download.

`api_rate_limit_per_minute` impone un limite per chiave: superata la soglia la
risposta è `429` con `Retry-After: 60`.

## Funzioni supportate

| `t` | Parametri | Note |
| --- | --- | --- |
| `caps` | — | Capacità, categorie e limiti; non richiede la chiave |
| `search` | `q`, `cat`, `limit`, `offset` | Ricerca generica |
| `tvsearch` | `q`, `season`, `ep`, `cat`, `imdbid`, `tvdbid`, `tmdbid` | Filtro stagione/episodio anche sui season pack |
| `movie` | `q`, `cat`, `imdbid`, `tmdbid` | Film |
| `get` | `id` | Scarica il `.torrent` salvato (alternativa a `/dl/{id}`) |

`music` e `book` rispondono come `search`: la cache non distingue ancora quei
tipi con filtri propri, ma le categorie 3000 e 7000 sono disponibili.

### Parametri

| Nome | Default | Descrizione |
| --- | --- | --- |
| `apikey` | — | Obbligatorio per ricerca e download |
| `q` | vuoto | Testo cercato nel titolo normalizzato (tutti i termini in AND) |
| `cat` | tutte | Uno o più id Torznab separati da virgola; `5000` seleziona tutta la radice TV |
| `limit` | 50 | Massimo 100 |
| `offset` | 0 | Paginazione |
| `season` | 0 | Solo `tvsearch` |
| `ep` | 0 | Solo `tvsearch` |
| `imdbid` | vuoto | Con o senza prefisso `tt` |
| `tmdbid` | vuoto | |
| `tvdbid` | vuoto | |

`q` e gli id esterni sono combinati in OR: una release che corrisponde all'id viene
restituita anche se il titolo è diverso.

La sintassi `-termine` e la modalità regex appartengono alla pagina web **Releases**;
l'API Torznab usa il solo parametro `q` standard.

### Ricerca live

Con `torznab_live_search` attivo, se la cache restituisce `total="0"` e la
richiesta ha un `q`, indexarr interroga al momento le sorgenti Torznab abilitate,
salva i risultati e ripete la ricerca. La deadline complessiva è
`torznab_live_timeout_seconds` (default 20). Le sorgenti RSS/HTML sono escluse;
una richiesta con soli id esterni (senza `q`) non attiva la ricerca live. La
ricerca live è spenta per default perché può allungare la risposta del client.

## Esempi

```bash
# Capacità
curl 'http://localhost:8484/api?t=caps'

# Ricerca TV
curl 'http://localhost:8484/api?t=tvsearch&q=Show&season=1&ep=2&cat=5000&apikey=KEY'

# Film per IMDb id
curl 'http://localhost:8484/api?t=movie&imdbid=0133093&apikey=KEY'

# Download del .torrent
curl -OJ 'http://localhost:8484/dl/42.torrent?apikey=KEY'
```

## Risposta di ricerca

```xml
<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"
     xmlns:torznab="http://torznab.com/schemas/2015/feed"
     xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/">
  <channel>
    <title>indexarr</title>
    <link>http://localhost:8484</link>
    <description>indexarr Torznab index</description>
    <newznab:response offset="0" total="1"/>
    <item>
      <title>Show.S01E02.1080p</title>
      <guid isPermaLink="false">0123456789abcdef0123456789abcdef01234567</guid>
      <link>http://localhost:8484/dl/1.torrent?apikey=KEY</link>
      <comments>http://localhost:8484/releases/1</comments>
      <pubDate>Mon, 05 Oct 2026 10:17:44 +0000</pubDate>
      <size>1234567890</size>
      <category>5040</category>
      <enclosure url="http://localhost:8484/dl/1.torrent?apikey=KEY"
                 length="1234567890" type="application/x-bittorrent"/>
      <torznab:attr name="category" value="5040"/>
      <torznab:attr name="size" value="1234567890"/>
      <torznab:attr name="seeders" value="12"/>
      <torznab:attr name="leechers" value="2"/>
      <torznab:attr name="peers" value="14"/>
      <torznab:attr name="infohash" value="0123456789abcdef0123456789abcdef01234567"/>
      <torznab:attr name="magneturl" value="magnet:?xt=urn:btih:..."/>
      <torznab:attr name="publishdate" value="2026-10-05T10:17:44Z"/>
      <torznab:attr name="downloadvolumefactor" value="0"/>
      <torznab:attr name="uploadvolumefactor" value="1"/>
    </item>
  </channel>
</rss>
```

Se il `.torrent` non è stato salvato, `<link>` e `<enclosure>` contengono il magnet:
i client che supportano i magnet (Sonarr, Radarr, qBittorrent) lo usano direttamente.
`downloadvolumefactor` e `uploadvolumefactor` sono configurabili in Settings.
Se la richiesta usa una chiave aggiuntiva, i link `.torrent` riportano quella
chiave, non la primaria.

Se un indexer non riporta i seeder (per esempio MirCrew), gli attributi `seeders`,
`leechers` e `peers` vengono **omessi** invece di valere `0`: un valore assente
significa "sconosciuto", uno zero significherebbe "torrent morto".

### Base dei link

I link sono costruiti con `public_url` se impostato, altrimenti dall'host della
richiesta. Dietro un reverse proxy imposta `public_url` (per esempio
`https://indexarr.example.org`) e inoltra `X-Forwarded-Proto`/`X-Forwarded-Host`.

## Download

| Percorso | Autenticazione | Risposta |
| --- | --- | --- |
| `GET /dl/{id}.torrent?apikey=KEY` | sì | File `.torrent` con `Content-Type: application/x-bittorrent` |
| `GET /api?t=get&id={id}&apikey=KEY` | sì | Come sopra |

Se per la release esiste solo il magnet la risposta è `404` con una descrizione XML:
il client deve usare il link magnet ricevuto nella ricerca.

## Errori

La risposta di errore è XML:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<error code="401" description="invalid API key"/>
```

| HTTP | Causa |
| --- | --- |
| 400 | `t` sconosciuto o id non valido |
| 401 | API key mancante o errata |
| 404 | Release o `.torrent` inesistente |
| 429 | Rate limit per chiave superato (`Retry-After: 60`) |
| 500 | Errore interno/database |
| 503 | API key non configurata |

## Health check

`GET /healthz` non richiede autenticazione e restituisce JSON:

```json
{"status":"ok","version":"dev","uptime_seconds":42,"releases":1200,"sources":3,"db_bytes":1048576}
```

## Metriche

`GET /metrics` non richiede autenticazione (come `/healthz`) e restituisce il
formato testuale Prometheus. È pensato per uno scraper locale o per la rete
fidata: non esporre l'istanza su Internet senza un reverse proxy autenticato.

```text
# TYPE indexarr_releases gauge
indexarr_releases 1200
# TYPE indexarr_sources gauge
indexarr_sources 3
# TYPE indexarr_enabled_sources gauge
indexarr_enabled_sources 2
# TYPE indexarr_runs_24h gauge
indexarr_runs_24h 14
# TYPE indexarr_errors_24h gauge
indexarr_errors_24h 1
# TYPE indexarr_blocklist_rules gauge
indexarr_blocklist_rules 4
# TYPE indexarr_db_bytes gauge
indexarr_db_bytes 1048576
```

Sono presenti anche `indexarr_up`, `indexarr_uptime_seconds`,
`indexarr_torrents` e `indexarr_torrent_bytes`.

## Categorie

Sono quelle standard newznab/Torznab. Le radici emesse da `caps`:

| Id | Categoria |
| --- | --- |
| 1000 | Console |
| 2000 | Movies |
| 3000 | Audio |
| 4000 | PC |
| 5000 | TV |
| 6000 | XXX |
| 7000 | Books |
| 8000 | Other |

Le sottocategorie complete sono in `internal/categories/categories.go` e nella
risposta `t=caps`.

## API amministrativa `/api/v2`

`indexarr 2` espone un'API JSON versionata per definizioni, indexer nativi e
applicazioni \*arr. È pensata per script e automazione: usa le stesse API key
del server Torznab, senza cookie e senza CSRF.

- Autenticazione: `?apikey=...`, header `X-Api-Key` o `Authorization: Bearer ...`
- Errori: JSON `{"error":"..."}` con stato HTTP coerente
- Corpi: massimo 1 MiB; campi sconosciuti rifiutati
- Nessuna risposta contiene API key, password, cookie, URL del tracker o
  `download_ref`

| Metodo | Percorso | Uso |
|---|---|---|
| GET | `/api/v2/health` | stato e versione |
| GET/POST | `/api/v2/definitions` | elenco / crea definizione YAML |
| POST | `/api/v2/definitions/validate` | valida senza salvare |
| GET/PUT/DELETE | `/api/v2/definitions/{id}` | leggi (con body) / aggiorna / cancella |
| GET | `/api/v2/catalog` | catalogo built-in e stato installazione |
| POST | `/api/v2/catalog/sync` | sincronizza il catalogo nel database |
| GET/POST | `/api/v2/indexers` | elenco / crea indexer nativo |
| GET/PUT/DELETE | `/api/v2/indexers/{id}` | dettaglio / aggiorna / cancella |
| POST | `/api/v2/indexers/{id}/test` | test per fase (reach/auth/search) |
| POST | `/api/v2/indexers/{id}/search` | ricerca live, senza URL del tracker |
| POST | `/api/v2/indexers/{id}/run` | un run di ingestion |
| GET/POST | `/api/v2/applications` | elenco / collega una \*arr |
| GET/PUT/DELETE | `/api/v2/applications/{id}` | dettaglio / aggiorna / scollega |
| POST | `/api/v2/applications/{id}/sync` | riconcilia un'applicazione |
| POST | `/api/v2/applications/sync` | riconcilia tutte |

Esempio:

```sh
curl -X POST "http://localhost:8484/api/v2/indexers?apikey=$KEY" \
  -H 'Content-Type: application/json' \
  -d '{
        "definition_id": "newznab-generic",
        "name": "Il mio Usenet",
        "settings": {"url": "https://indexer.example/api", "apikey": "..."}
      }'
```

I valori con nome che contiene `api_key`, `apikey`, `token`, `password`,
`passkey`, `secret` o `cookie` vengono cifrati nel vault; gli altri finiscono
in `config_json`. Le definizioni built-in sono oggi in stato `bozza`: non sono
state verificate contro un sito live.
