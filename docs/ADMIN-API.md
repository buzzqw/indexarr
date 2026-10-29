# API amministrativa JSON di indexarr

indexarr espone due superfici diverse:

| Superficie | Percorso | Formato | Uso |
| --- | --- | --- | --- |
| **Torznab/Newznab** | `/api`, `/torznab/api`, `/newznab/api`, `/torznab/{slug}/api` | XML | Sonarr, Radarr, gextto, client Torznab |
| **API amministrativa** | `/api/v2/...` | JSON | script, automazioni, integrazioni |

Questa pagina documenta l'**API amministrativa JSON**. Per il protocollo
Torznab vedi [API.md](API.md).

---

## 1. Nozioni di base

- **Base URL**: `http://<host>:8788`
- **Prefisso**: `/api/v2/`
- **Content-Type**: `application/json` per ogni corpo inviato
- **Nessun cookie e nessun CSRF**: pensata per script e servizi
- **Corpo massimo**: 1 MiB per richiesta
- **Campi sconosciuti**: rifiutati con `400` (`DisallowUnknownFields`)
- **Segreti**: password, API key, cookie e token sono **solo in scrittura**.
  Non vengono mai restituiti in chiaro; le risposte indicano solo
  `has_secrets`, `has_api_key` o `has_auth`.

### Autenticazione

La stessa API key del Torznab, in uno dei tre modi:

```http
GET /api/v2/health?apikey=KEY
GET /api/v2/health
X-Api-Key: KEY
Authorization: Bearer KEY
```

Le chiavi aggiuntive (Impostazioni → API aggiuntive) funzionano come la
primaria. Una chiave revocata restituisce `401`.

### Errori

Tutti gli errori sono JSON:

```json
{"error": "indexer not found"}
```

| HTTP | Significato |
| --- | --- |
| `400` | corpo o parametro non valido |
| `401` | API key mancante, errata o revocata |
| `404` | risorsa inesistente |
| `409` | conflitto (slug o definizione già esistenti) |
| `413`/`400` | corpo oltre 1 MiB |
| `422` | definizione non valida (con `errors[]`) |
| `503` | motore o vault non disponibili |
| `500` | errore interno (il dettaglio resta nel log del server) |

---

## 2. Avvio rapido

```bash
export INDEXARR=http://127.0.0.1:8788
export KEY='la-tua-api-key'

curl -s "$INDEXARR/api/v2/health?apikey=$KEY" | jq
curl -s "$INDEXARR/api/v2/indexers?apikey=$KEY" | jq
curl -s "$INDEXARR/api/v2/catalog?apikey=$KEY" | jq '.catalog[:3]'
```

---

## 3. Health

### `GET /api/v2/health`

```json
{"status":"ok","version":"v2.1.0-rc1-33-g59a082b"}
```

Non richiede il motore: serve a verificare processo, chiave e versione.

---

## 4. Definizioni

Una definizione è il file YAML che descrive un sito (connettore, login,
ricerca, download). Il formato è in [DEFINITIONS.md](DEFINITIONS.md).

### `GET /api/v2/definitions`

```json
{
  "definitions": [
    {
      "id": "extto",
      "schema_version": 1,
      "family": "html",
      "state": "verificata",
      "origin": "builtin",
      "author": "indexarr",
      "hash": "…",
      "updated_at": "2026-10-26T08:00:00Z"
    }
  ]
}
```

### `GET /api/v2/definitions/{id}`

Come sopra, più `"body"` con lo YAML completo.

### `POST /api/v2/definitions`

```json
{"body": "schema: 1\nmeta:\n  id: mio-sito\n…"}
```

- `201` con i metadati della definizione creata;
- `422` con `{"error":"invalid definition","errors":["…"]}` se lo YAML non è
  valido;
- `409` se l'id esiste già, salvo `?overwrite=1` (o `true`/`yes`).

Le definizioni create via API hanno `origin: "local"`: il catalogo non le
sovrascrive.

### `PUT /api/v2/definitions/{id}`

Stesso corpo della creazione. L'id nel corpo deve coincidere con il percorso.
La definizione diventa `origin: "local"` anche se era builtin.

### `DELETE /api/v2/definitions/{id}`

`204` se eliminata. `409` se almeno un indexer la usa.

### `POST /api/v2/definitions/validate`

Non salva nulla:

```json
{"valid": true, "id": "mio-sito", "name": "Mio Sito"}
```

In caso di errore: `422` con `{"valid":false,"errors":[…]}`.

---

## 5. Catalogo

### `GET /api/v2/catalog`

Elenca le definizioni incluse nel binario:

```json
{
  "catalog": [
    {"id":"1337x","state":"stabile","author":"indexarr","hash":"…","installed":true}
  ]
}
```

`installed` indica se la definizione è già nel database.

### `POST /api/v2/catalog/sync`

Allinea il database al catalogo incluso nel binario. Le definizioni
`origin: "local"` non vengono toccate.

```json
{"written": 12}
```

---

## 6. Indexer

### Modello

```json
{
  "id": 15,
  "definition_id": "mircrew",
  "protocol": "torrent",
  "slug": "mircrew",
  "name": "MirCrew",
  "enabled": true,
  "priority": 25,
  "tags": [],
  "query_limit": 0,
  "grab_limit": 0,
  "proxy_id": null,
  "min_seeders": 0,
  "seed_ratio": 0,
  "seed_time_minutes": 0,
  "season_pack_seed_time_minutes": 0,
  "interval_seconds": 3600,
  "has_secrets": true,
  "created_at": "2026-10-24T00:38:16Z",
  "updated_at": "2026-10-26T10:56:53Z"
}
```

### `GET /api/v2/indexers`

```json
{"indexers": [ … ]}
```

### `GET /api/v2/indexers/{id}`

Un solo oggetto indexer. `404` se non esiste.

### `POST /api/v2/indexers`

```json
{
  "definition_id": "torrentleech",
  "name": "TorrentLeech",
  "slug": "torrentleech",
  "protocol": "torrent",
  "enabled": true,
  "priority": 25,
  "tags": ["tv"],
  "interval_seconds": 1800,
  "settings": {
    "username": "utente",
    "password": "segreta",
    "categories": "2000,5000"
  }
}
```

- `definition_id` e `name` sono obbligatori;
- `slug` è normalizzato dal nome se assente;
- `settings` accetta stringhe, numeri e booleani; le chiavi segrete
  (`password`, `apikey`, `token`, `cookie`, …) finiscono nel vault;
- i setting obbligatori della definizione devono essere presenti, altrimenti
  `400 missing required setting: <nome>`;
- `201` con il modello creato.

### `PUT /api/v2/indexers/{id}`

Stessi campi. I campi omessi restano invariati; una chiave segreta assente o
vuota mantiene il valore salvato. Per cancellare un segreto usare
`"settings": {"password": ""}`.

### `DELETE /api/v2/indexers/{id}`

`204`. Il motore invalida la connessione in cache.

### `POST /api/v2/indexers/{id}/test`

Esegue le fasi di prova (raggiungibilità, capacità, login, ricerca):

```json
{
  "ok": true,
  "phases": [
    {"name":"reach","ok":true,"message":"","duration_ms":120},
    {"name":"caps","ok":true,"message":"","duration_ms":80},
    {"name":"search","ok":true,"message":"","duration_ms":900}
  ]
}
```

Una prova riuscita registra anche un controllo di salute e pulisce l'ultimo
errore dello stato.

### `POST /api/v2/indexers/{id}/search`

```json
{
  "mode": "search",
  "query": "Reacher",
  "season": 0,
  "episode": 0,
  "imdbid": "",
  "tmdbid": "",
  "tvdbid": "",
  "limit": 50,
  "offset": 0,
  "categories": [5000]
}
```

`mode`: `search`, `tvsearch`, `movie`, `music`, `book`.

```json
{
  "items": [
    {
      "title": "Reacher S03E01 1080p",
      "guid": "…",
      "size": 1234567890,
      "seeders": 12,
      "leechers": 2,
      "categories": [5040],
      "published": "2026-10-25T10:00:00Z",
      "infohash": "…",
      "magnet": "magnet:?xt=urn:btih:…"
    }
  ]
}
```

### `POST /api/v2/indexers/{id}/run`

Esegue un aggiornamento (lista ultime release o ricerca fissa):

```json
{"seen":500,"new":494,"updated":0,"blocked":0,"skipped":0,"torrents":12}
```

---

## 7. Proxy

### Modello

```json
{"id": 1, "kind": "http", "url": "http://proxy.example:8080", "has_auth": true}
```

`kind`: `http`, `https`, `socks5`, `socks5h`.

### Endpoint

| Metodo | Percorso | Note |
| --- | --- | --- |
| `GET` | `/api/v2/proxies` | elenco |
| `POST` | `/api/v2/proxies` | crea (`201`) |
| `GET` | `/api/v2/proxies/{id}` | dettaglio |
| `PUT` | `/api/v2/proxies/{id}` | aggiorna |
| `DELETE` | `/api/v2/proxies/{id}` | `204` |

Corpo di creazione/aggiornamento:

```json
{
  "kind": "http",
  "url": "http://proxy.example:8080",
  "username": "utente",
  "password": "segreta",
  "clear_auth": false
}
```

`password` vuota mantiene quella salvata; `clear_auth: true` la rimuove.

---

## 8. Applicazioni

Le applicazioni sono Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3,
LazyLibrarian.

### Modello

```json
{
  "id": 1,
  "kind": "sonarr",
  "name": "Sonarr",
  "url": "http://192.168.1.10:8989",
  "sync_level": "full",
  "enable_rss": true,
  "enable_automatic_search": true,
  "enable_interactive_search": true,
  "min_seeders": 0,
  "categories": [5000, 2000],
  "last_sync_at": "2026-10-26T10:00:00Z",
  "last_error": "",
  "has_api_key": true
}
```

`kind` ammessi: `sonarr`, `radarr`, `lidarr`, `readarr`, `whisparr`,
`mylar3`, `lazylibrarian`.

`sync_level`: `disabled`, `add-remove`, `full`.

`enable_rss`, `enable_automatic_search` e `enable_interactive_search` sono i
flag con cui l'indexer viene creato nell'app. `min_seeders` (0 = valore
dell'indexer) e `categories` (id Torznab standard, array o stringa CSV; vuoto =
tutte le categorie supportate dall'app) completano il profilo della singola
applicazione. In aggiornamento i campi omessi mantengono il valore corrente.

### Endpoint

| Metodo | Percorso | Note |
| --- | --- | --- |
| `GET` | `/api/v2/applications` | `{"applications":[…]}` |
| `POST` | `/api/v2/applications` | crea (`201`) |
| `GET` | `/api/v2/applications/{id}` | dettaglio |
| `PUT` | `/api/v2/applications/{id}` | aggiorna |
| `DELETE` | `/api/v2/applications/{id}` | `204` |
| `POST` | `/api/v2/applications/{id}/sync` | sincronizza una app |
| `POST` | `/api/v2/applications/sync` | sincronizza tutte |

Creazione:

```json
{
  "kind": "sonarr",
  "name": "Sonarr",
  "url": "http://192.168.1.10:8989",
  "api_key": "chiave-di-sonarr",
  "sync_level": "full",
  "enable_rss": true,
  "enable_automatic_search": true,
  "enable_interactive_search": true,
  "min_seeders": 0,
  "categories": [5000]
}
```

`api_key` è obbligatoria alla creazione; in aggiornamento una stringa vuota
mantiene quella salvata.

Risposta di sincronizzazione:

```json
{"added": 12, "updated": 3, "deleted": 0, "skipped": 1}
```

`POST /api/v2/applications/sync` restituisce un array con il risultato di ogni
app, incluso `error` quando una fallisce.

---

## 9. Compatibilità Prowlarr (sola lettura)

Per le dashboard (Homepage, Organizr, …) indexarr espone la stessa superficie
in lettura di Prowlarr:

| Metodo | Percorso | Risposta |
| --- | --- | --- |
| `GET` | `/api/v1/system/status` | `appName`, `version`, `branch`, `authentication`, `startTime` |
| `GET` | `/api/v1/health` | array vuoto (nessun problema) |
| `GET` | `/api/v1/indexer` | elenco in formato Prowlarr |
| `GET` | `/api/v1/indexer/{id}` | un indexer |

Queste rotte non accettano scritture e non inoltrano ricerche. Ogni indexer
riporta `baseUrl` = `<base>/torznab/<slug>` e `apiPath` = `/api`, così un
client che aggiunge `/api` ottiene l'endpoint Torznab per-indexer.

---

## 10. Esempi utili

### Creare un indexer ExtTo e provarlo

```bash
KEY='…'
BASE='http://127.0.0.1:8788'

curl -s -X POST "$BASE/api/v2/indexers?apikey=$KEY" \
  -H 'Content-Type: application/json' \
  -d '{
    "definition_id":"extto",
    "name":"ExtTo – MIRCrew",
    "protocol":"torrent",
    "enabled":true,
    "settings":{"filter":"c=TV","query":"MIRCrew"}
  }' | jq

curl -s -X POST "$BASE/api/v2/indexers/48/test?apikey=$KEY" | jq
```

### Cercare un episodio

```bash
curl -s -X POST "$BASE/api/v2/indexers/15/search?apikey=$KEY" \
  -H 'Content-Type: application/json' \
  -d '{"mode":"tvsearch","query":"Reacher","season":3,"episode":1,"limit":10}' | jq
```

### Sincronizzare tutte le app

```bash
curl -s -X POST "$BASE/api/v2/applications/sync?apikey=$KEY" | jq
```

### Validare una definizione prima di salvarla

```bash
jq -Rs '{body: .}' mio-sito.yaml > body.json
curl -s -X POST "$BASE/api/v2/definitions/validate?apikey=$KEY" \
  -H 'Content-Type: application/json' \
  --data @body.json | jq
```

---

## 11. Note di sicurezza

- Non mettere la API key negli URL condivisi o nei log: preferisci
  `X-Api-Key` o `Authorization: Bearer`.
- L'API non restituisce mai un segreto. Per ruotare una password, invia il
  nuovo valore; per rimuoverla, invia una stringa vuota.
- Le chiamate di test, ricerca e aggiornamento possono contattare i siti
  reali: rispettano i limiti per host e i proxy configurati, ma un test su un
  sito con CAPTCHA può fallire.
- Le definizioni create via API sono `local` e non vengono sovrascritte dal
  catalogo.
