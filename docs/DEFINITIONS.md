# Definizioni indexarr (formato v1)

Una definizione è un documento YAML che descrive **un indexer**: metadati,
impostazioni utente, mappa delle categorie, autenticazione, ricerca e test
offline. Il motore la interpreta come dati: non esiste codice eseguibile dentro
una definizione.

Questo documento descrive il formato `schema: 1` accettato da
`internal/definition`, il connettore `internal/engine` che lo esegue e il CLI
`indexarr-def`. Per il contesto e le scelte di prodotto:
[`indexarr2.md`](../indexarr2.md) §6 e [`INDEXARR2-PLAN.md`](INDEXARR2-PLAN.md).

## Struttura di un file

```yaml
schema: 1
connector: ""          # opzionale: nome di un connettore nativo
meta: {}               # obbligatorio
settings: []
categories: []
capabilities: {}
auth: {}               # opzionale
search: {}             # obbligatorio se connector è assente
download: {}           # opzionale: magnet/torrent dalla pagina di dettaglio
health: {}             # opzionale
tests: []              # opzionale, ma richiesto per il catalogo
```

Le chiavi sconosciute sono **rifiutate**: un refuso non viene ignorato. Il file
deve contenere un solo documento YAML.

## `meta`

| Campo | Obbligatorio | Regole |
|---|---|---|
| `id` | sì | slug `^[a-z0-9][a-z0-9-]*$`, stabile e univoco |
| `name` | sì | nome leggibile, non vuoto |
| `type` | sì | `public`, `semi-private`, `private` |
| `protocol` | sì | `torrent` oppure `usenet` |
| `urls` | sì | almeno un URL assoluto `http`/`https`; il primo è il principale, gli altri sono mirror |
| `language` | no | tag BCP 47, per esempio `it-IT` |
| `author` | no | autore o manutentore |
| `description` | no | descrizione breve |
| `updated` | no | data dell'ultima revisione, per esempio `"2026-10-23"` |
| `derived_from` | no | slug dello strumento che ha prodotto una bozza, per esempio `cardigann`; una bozza derivata resta locale e il catalogo la rifiuta |
| `interval` | no | durata Go, per esempio `15m`; è un suggerimento per l'intervallo di aggiornamento dell'indexer. Vuoto = nessun suggerimento, vale il valore globale. Il minimo è 60s |

### Bozze derivate

`derived_from` marca una definizione prodotta da uno strumento di conversione,
oggi solo il convertitore Cardigann (`derived_from: cardigann`). Non è un
percorso di migrazione né un'importazione: la bozza resta **locale**, la UI e
l'API non hanno una rotta per importarla, e il catalogo built-in la rifiuta.

L'autore `cardigann-import` è un marcatore equivalente a `derived_from`,
confrontato senza distinzione di maiuscole. Per entrare nel catalogo una bozza
va riscritta osservando il sito: selettori verificati su fixture reali, autore e
data nostri, `derived_from` rimosso. `make def-validate` esegue questa policy
sulla directory del catalogo con `validate -catalog`.

### Intervallo suggerito

`meta.interval` è additivo e facoltativo. Un feed "ultime 24 h" ha bisogno di
aggiornamenti più frequenti di un sito con la ricerca, quindi la definizione
può suggerire una durata Go (`15m`, `2h`). Alla creazione dell'indexer il campo
"Intervallo" in Avanzate è precompilato con questo valore; l'utente può
cambiarlo o impostarlo a `0` per usare l'intervallo globale. Un valore inferiore
a 60s è rifiutato. Il formato resta `schema: 1`.

## `settings`

Ogni impostazione diventa un campo del form dell'indexer.

| Campo | Obbligatorio | Note |
|---|---|---|
| `name` | sì | slug; è la chiave usata nei template |
| `type` | no | tipo del form: `text`, `password`, `select`, `checkbox`, `number`… |
| `label`, `help` | no | testo mostrato nella UI |
| `default` | no | valore applicato quando l'utente non ne inserisce uno |
| `required` | no | se vero il connettore rifiuta l'indexer senza valore |
| `options` | no | valori ammessi per `select` |

I nomi devono essere slug: lettere minuscole, cifre e trattini. Per questo la
grafia valida di una API key è `apikey` (non `api_key`); il motore sincronizza
`apikey` e `api_key` quando serve.

Le impostazioni il cui nome o tipo suggerisce un segreto (`password`, `token`,
`apikey`, `secret`, `cookie`, `passkey`) sono trattate come sensibili e
redatte nei messaggi di errore.

## `categories`

Mappa le categorie del sito sulle categorie standard Torznab/Newznab.

```yaml
categories:
  - { site: "5",  standard: 5040, desc: "TV HD" }
  - { site: "12", standard: 2040, desc: "Film HD" }
```

- `site` è l'id o la stringa usata dal sito;
- `standard` deve essere tra `1000` e `8999`;
- `desc` è facoltativo.

Quando una ricerca chiede categorie standard, il motore le traduce negli id del
sito tramite questa tabella. Una categoria non riconosciuta diventa `8000`
(Other).

## `capabilities`

Dichiarano quali modalità e parametri l'indexer supporta. I valori ammessi sono
`q`, `season`, `ep`, `imdbid`, `tvdbid`, `tmdbid`, `tvmazeid`, `rid`, `artist`,
`album`, `author`, `title`, `year`, `genre`.

```yaml
capabilities:
  search: [q]
  tv-search: [q, season, ep, imdbid]
  movie-search: [q, imdbid]
```

Le modalità senza parametri non vengono pubblicate nelle capability del
connettore. Le categorie pubblicate sono gli id `standard` dichiarati in
`categories`.

## `auth`

```yaml
auth:
  strategy: form
  steps:
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
```

### Strategie

| Strategia | Comportamento |
|---|---|
| `none` | nessuna autenticazione |
| `api_key` | aggiunge `apikey` alla query se la definizione non usa già `settings.apikey`/`settings.api_key`; altrimenti il valore va inserito esplicitamente nei template |
| `basic` | HTTP Basic con `settings.username` e `settings.password` |
| `token` | header `Authorization: Bearer <settings.token>` |
| `form` | esegue `steps` al primo uso e conserva cookie e valori estratti |
| `cookie` | come `form`; utile quando il flusso è già descritto da `steps` |

`refresh_after` è una durata Go (`30m`, `6h`, `1h30m`). Se assente, il motore
riusa la sessione per 6 ore. Con `form`/`cookie` il login avviene in modo lazy e
viene ripetuto su `401`/`403` una volta per ricerca.

### `steps`

Ogni passo ha **`get` oppure `post`** (mai entrambi) e può avere:

- `form`: campi del body, solo per `post`;
- `headers`: header aggiuntivi;
- `extract`: valori da estrarre dalla risposta e rendere disponibili ai passi
  successivi e alla ricerca.

Gli estrattori sono valutati in quest'ordine:

```yaml
extract:
  csrf: { selector: 'input[name="csrf"]', attr: value }  # attr vuoto = testo
  token: { json: "$.token" }
  nonce: { regex: 'nonce=([a-f0-9]+)' }                  # serve il gruppo 1
```

I cookie `Set-Cookie` sono catturati automaticamente; non vanno dichiarati.

### Condizioni

`success` e ogni voce di `failure` possono avere `selector` e/o `status`:

- con entrambi, devono valere entrambi;
- `message: text` usa il testo del nodo selezionato come messaggio d'errore;
- senza `success`, il login riesce con uno status `2xx`/`3xx`;
- i `failure` sono valutati prima di `success`.

I valori estratti e le impostazioni sono disponibili nei template; le password
non compaiono mai nei messaggi restituiti.

## Template

I template sono deliberatamente minimi:

```yaml
"{{ query.keywords }}"
"{{ settings.apikey }}"
"{{ csrf }}"
"{{ categories | join(',') }}"
"{{ page }}"
```

Variabili disponibili:

- `settings.<nome>` — solo impostazioni dichiarate;
- `query.keywords`, `query.season`, `query.ep`, `query.imdbid`, `query.tvdbid`,
  `query.tmdbid`, `query.tvmazeid`, `query.rid`, `query.artist`,
  `query.album`, `query.author`, `query.title`, `query.year`;
- i valori estratti durante il login (`{{ csrf }}`, `{{ token }}`, …);
- `categories` — id del sito corrispondenti alle categorie richieste;
- `page`, `offset`, `limit` — valori scalari della paginazione.

L'unico filtro è `join(<separatore>)`, applicabile a `categories`. Un nome
sconosciuto o un filtro sconosciuto è un errore, non una stringa vuota.

## `search`

Obbligatorio quando `connector` è assente. Se `connector` è presente e `search`
è omesso, il connettore nativo gestisce la ricerca; se `search` è presente viene
comunque validato.

### `request`

```yaml
search:
  request:
    get: /torrents.php          # oppure post, mai entrambi
    params:                     # query string
      search: "{{ query.keywords }}"
      cat: "{{ categories | join(',') }}"
      page: "{{ page }}"
    form:                       # body, solo con post
      sort: "{{ settings.sort }}"
    headers:
      Accept: text/html
```

`params` vengono aggiunti alla query anche per le richieste `POST`. I percorsi
relativi (anche quelli che iniziano con `/`) sono accodati al path del primo URL
di `meta.urls`; un URL assoluto è usato solo se lo schema è `http`/`https`. I
redirect sono rivalidati dal transport.

### `pagination`

```yaml
pagination: { type: page, start: 1, step: 1, max_pages: 5,
              param: page, stop_when: empty_rows }
```

| Tipo | Significato |
|---|---|
| `none` (default) | una sola richiesta |
| `page` | pagine numerate; `start` default 1, `step` default 1 |
| `offset` | offset crescente; `start` default 0, `step` default `limit` della richiesta |
| `cursor` | accettato dalla validazione, **non ancora implementato** dal motore: la ricerca restituisce un errore esplicito |

`max_pages` default 1 (la prima pagina). `stop_when` ammette solo
`empty_rows`: la paginazione si ferma quando una pagina non produce righe.
`param`, se presente, imposta il parametro di query indicato (senza
sovrascrivere un valore già prodotto da `params`).

### `response`

```yaml
response:
  type: html                 # html | json | xml
  rows: { selector: "table.torrents > tbody > tr" }
  fields:
    title: { selector: ["a.title", "td.name a"], filters: [trim] }
    size: { selector: "td.size", type: size }
  required: [title, size]
```

#### Righe

- **HTML**: `rows.selector` è un selettore CSS, oppure una lista di fallback;
  si usa il primo che trova nodi. È obbligatorio.
- **JSON**: `rows.path` è un percorso puntato; i segmenti numerici indicizzano
  gli array (`data.items`, `files.0.url`). Se vuoto, la radice deve essere un
  array.
- **XML**: `rows.path` è obbligatorio (`rss.channel.item`); i namespace sono
  confrontati sul nome locale.

#### Campi

Un campo HTML usa `selector` (lista = fallback) e `attr`. Con `attr` vuoto o
`text` viene estratto il testo del nodo, altrimenti l'attributo indicato.

Un campo JSON usa `path`; se assente, usa il nome del campo. Un campo XML usa
`path` con le forme `title`, `title.#text`, `enclosure@url` e `@attr` (attributo
della riga).

I nomi speciali riconosciuti dalla normalizzazione, quando dichiarati, sono:

`title`, `guid`, `details`, `download`, `magnet`, `infohash`, `size`, `seeders`,
`leechers`, `date` (o `published`), `category` (o `categories`), `imdbid`,
`tmdbid`, `tvdbid`.

Una riga viene scartata se manca il titolo, se un campo in `required` è vuoto o
non convertibile, o se non ha né magnet né link di download né link ai dettagli.
Il titolo viene normalizzato negli spazi; le categorie assenti diventano
`8000`. I duplicati della stessa ricerca sono rimossi per infohash, poi GUID,
poi URL di download.

#### Tipi

| Tipo | Conversione |
|---|---|
| `text` | stringa, default |
| `int` | intero; tollera separatori delle migliaia, valori non numerici diventano 0 |
| `size` | byte oppure `1.5 GB`, `1,5 GiB`, ecc. |
| `date` | layout Go di `layouts`, poi un insieme di formati comuni; accetta anche timestamp Unix |
| `url` | URL relativo risolto sulla risposta o sul base URL |
| `category` | id del sito mappato da `categories` |

I tipi contano anche nei `required`: un `size` a zero, un `int` a zero, una
data non valida o un URL vuoto fanno scartare la riga.

#### Filtri

I filtri sono applicati nell'ordine dichiarato:

| Filtro | Effetto |
|---|---|
| `trim` | rimuove spazi ai bordi |
| `lower` / `upper` | cambia maiuscole/minuscole |
| `replace: "old:new"` | sostituisce tutte le occorrenze |
| `query_param: name` | estrae il parametro dall'URL nel valore |
| `prefix: "x"` / `suffix: "x"` | aggiunge testo |
| `regex: "pattern"` | usa il gruppo 1 della prima corrispondenza; nessuna corrispondenza = stringa vuota |

Esempio:

```yaml
category:
  selector: "a.cat"
  attr: href
  filters: [{ query_param: cat }]
  type: category
```

## `download`

Risolve il magnet o il link `.torrent` da una pagina di dettaglio quando il
listing non li espone. La sezione è **opzionale** e retro-compatibile: le
definizioni che hanno già `magnet` o `download` nella riga di ricerca non
cambiano e `schema` resta 1.

```yaml
download:
  resolve_on: grab            # search | grab (default)
  from: details               # campo della ricerca, default details
  max_concurrent: 2           # 1..8, default 2
  extract:
    - magnet:  { selector: 'a[href^="magnet:"]', attr: href }
    - torrent: { selector: 'a[href$=".torrent"]', attr: href, type: url }
```

| Campo | Obbligatorio | Regole |
|---|---|---|
| `resolve_on` | no | `search` oppure `grab` (default) |
| `from` | no | nome di un campo dichiarato in `search.response.fields`; vuoto = `details` |
| `max_concurrent` | no | da 1 a 8; `0` o assente = 2. Valori fuori intervallo sono rifiutati |
| `extract` | sì | almeno un estrattore; ogni voce ha **solo** `magnet` o **solo** `torrent` |

Ogni estrattore è un `Field` con le stesse regole dei campi di ricerca:
`selector` (lista = fallback) oppure `path` per una risposta JSON, `attr`,
`type` (`text`, `int`, `size`, `date`, `url`, `category`) e `filters`. Per
`torrent` il valore viene risolto come URL relativo alla pagina di dettaglio;
per `magnet` deve essere un URI `magnet:`.

### Semantica

- **`resolve_on: search`** — durante la ricerca, **dopo deduplica e limite**,
  per ogni riga con il campo `from` valorizzato e senza magnet né link di
  download, il motore apre la pagina di dettaglio con lo stesso transport, la
  stessa sessione e la stessa autenticazione della ricerca. Applica gli
  estrattori **nell'ordine dichiarato**: il primo che produce un valore vince.
  Imposta `Magnet` e `InfoHash` normalizzato, oppure `DownloadURL` per un
  `.torrent`. Le righe che hanno già un magnet o un torrent non vengono aperte.
  Con 50 righe e `limit: 10` vengono aperte al massimo 10 pagine di dettaglio.
- **`resolve_on: grab`** (default quando il campo è assente) — la ricerca non
  apre i dettagli; `Download` li apre al momento del grab. Meno richieste al
  sito, infohash disponibile solo dopo il download.
- **`Download`** preferisce un magnet già presente, poi un URL di download
  diretto, poi la pagina di dettaglio risolta con questa sezione. Un redirect
  della pagina (o del link) verso `magnet:` viene restituito come
  `Payload.Magnet`.
- La concorrenza è limitata da `max_concurrent` (default 2, massimo 8). Le
  risoluzioni riuscite sono tenute in una cache in memoria per indexer
  (256 voci, TTL 10 minuti); righe con lo stesso URL di dettaglio condividono
  una sola richiesta.
- Un dettaglio che fallisce non interrompe la pagina: la riga resta senza
  magnet/URL e il fallimento viene contato. Gli errori non contengono mai
  l'URL del tracker, che può includere una passkey.

### Validazione

- `resolve_on` deve essere `search` o `grab`;
- `from` (dopo il default `details`) deve esistere in
  `search.response.fields`;
- `extract` non può essere vuoto;
- ogni voce deve avere esattamente uno tra `magnet` e `torrent`;
- ogni campo deve avere `selector` o `path` non vuoti, un `type` ammesso e
  filtri validi (`replace: old:new`, `query_param` con nome, `regex`
  compilabile, nomi noti);
- `max_concurrent` deve essere `0` (default) o tra 1 e 8.

## `health`

```yaml
health:
  query: test
  min_rows: 1
```

È la query usata dal test del connettore. Il motore esegue la prima pagina e
richiede almeno `min_rows` risultati (default 1, query default `test`). Il test
riporta anche le fasi `reach` (GET sul base URL), `auth` (solo se serve) e
`download` (oggi `skipped`). `Test.OK` richiede reach, auth (quando previsto) e
search riusciti.

## `tests` e fixture offline

```yaml
tests:
  - name: search
    fixture: fixtures/html-forum/search.html
    expect: { min_rows: 2, required: [title, download, size, category] }
  - name: login-ok
    fixture: fixtures/html-forum/login-ok.html
    expect: { authenticated: true }
  - name: login-failed
    fixture: fixtures/html-forum/login-failed.html
    expect: { authenticated: false }
```

- `fixture` è un percorso **relativo al file della definizione**, senza `..`,
  senza path assoluti e senza `~`.
- `expect.min_rows` è il numero minimo di righe estratte.
- `expect.required` elenca i campi che devono essere presenti.
- `expect.authenticated` distingue una fixture di login riuscito da una fallita.

`indexarr-def check-fixtures` verifica solo che i file esistano, siano file
regolari e non vuoti. L'estrazione sulle fixture viene esercitata dai test del
motore (`internal/engine`) con `httptest`; il CLI non esegue richieste né
estrazioni.

Per il catalogo servono fixture per la ricerca e, se l'indexer è privato, per
login riuscito e fallito. Nessuna fixture deve contenere passkey, cookie,
credenziali o nomi utente reali: si usano valori sintetici come
`synthetic-csrf-token`.

## `connector` e famiglie

Il campo opzionale `connector` delega l'interpretazione a un connettore nativo.
I nomi registrati da `internal/family` sono:

- `unit3d` — tracker basati su UNIT3D (API JSON, torrent);
- `newznab` — API Newznab generica (usenet);
- `torznab` — API Torznab generica (torrent);
- `gazelle` — software Gazelle e derivati (musica, API JSON);
- `nexusphp` — tracker basati su NexusPHP (HTML, login form);
- `torrenttrader` — TorrentTrader/TBDev e derivati (HTML);
- `xbtit` — Xbtit/BtiTracker e derivati (HTML).

Le famiglie HTML (`nexusphp`, `torrenttrader`, `xbtit`) hanno selettori e
percorsi predefiniti marcati `bozza`: vanno verificati sul sito reale e, se
diversi, si sovrascrivono con le opzioni del profilo (non serve una definizione
per sito).

Con `connector` valorizzato, `search` può essere omesso. Le impostazioni tipiche
delle famiglie sono URL, API key, categorie e opzioni del transport; nel YAML i
nomi devono essere slug, quindi si usano per esempio `url`, `apikey`,
`categories`. Le categorie e le capability dichiarate nella definizione
completano il connettore.

Ciò che il formato non esprime va in un connettore Go, non in estensioni ad hoc.
La gerarchia delle fonti è: API ufficiale, feed standard, connettore di famiglia,
definizione nativa, connettore dedicato.

## Versioning, upgrader e JSON Schema

Il formato è versionato con `schema`. La versione corrente è **1**; `Parse`
accetta solo `schema: 1`. Il pacchetto `internal/definition` espone anche:

- `Upgrade(body)` — porta un documento alla versione corrente applicando la
  catena di `RegisterUpgrader`; una versione futura o un anello mancante sono
  errori espliciti;
- `Fingerprint(body)` — SHA-256 esadecimale dei byte grezzi, usato per rilevare
  modifiche alle definizioni salvate;
- `JSONSchema()` — lo schema JSON draft 2020-12 incorporato
  (`internal/definition/schema.json`).

Per ogni futura versione si registra un upgrader `vN → vN+1`; il motore esegue
solo la versione corrente. Oggi non esiste una versione 2.

Lo schema JSON si ottiene con:

```bash
indexarr-def schema
```

Può essere usato in editor e CI per autocompletamento e validazione strutturale;
la validazione semantica resta quella di `definition.Validate` e del CLI.

## CLI `indexarr-def`

```bash
indexarr-def validate [flags] <file-or-dir>...
indexarr-def schema
indexarr-def fmt [-w] <file>
indexarr-def fingerprint <file>
indexarr-def check-fixtures <file>
indexarr-def convert-cardigann [-o dir] [-author name] <file-or-dir>...
indexarr-def -version
```

### `validate`

Percorre ricorsivamente le directory, considera solo `*.yaml`/`*.yml`, salta le
directory nascoste (`.git`, …) e ordina i file. Non esclude `testdata/invalid`:
se puntate a una directory con fixture volutamente rotte, le riportate.

```text
ok examples/definitions/html-forum.yaml (id=html-forum, schema=1, 3 settings, 3 categories)
FAIL bad.yaml: definition: meta.id: is required
```

Con `-json` emette un array di `{path, ok, id, error}`. L'exit code è 1 se
almeno un file fallisce, 2 per un errore d'uso.

`-catalog` (alias `-reject-derived`) aggiunge la policy del catalogo: rifiuta
le bozze derivate, cioè `meta.derived_from` non vuoto o `meta.author` uguale a
`cardigann-import` senza distinzione di maiuscole. `make def-validate` usa
questa modalità per `internal/catalog/definitions/`, mentre la validazione
normale continua ad accettare le bozze locali prodotte dal convertitore.

```bash
indexarr-def validate -catalog internal/catalog/definitions
```

### `fmt`

Analizza e riserializza in YAML canonico (2 spazi). **I commenti e l'ordine
originale delle chiavi non vengono conservati.** Senza `-w` scrive su stdout.

```bash
indexarr-def fmt definition.yaml
indexarr-def fmt -w definition.yaml
```

### `fingerprint`

Stampa lo SHA-256 dei byte grezzi del file, senza parsare né normalizzare.

### `check-fixtures`

Elenca ogni fixture con `ok <path> (<byte>)` o `FAIL <path>: <errore>`; exit 1
se una fixture manca, è vuota o non è un file regolare.

### `convert-cardigann`

Converte **in locale** definizioni Cardigann (Jackett/Prowlarr) in **bozze** del
formato nativo v1. È uno **strumento di scrittura, non di migrazione**: non è un
runtime Cardigann, indexarr non esegue mai questi file e non li distribuisce.
Ogni bozza prodotta contiene `meta.derived_from: cardigann`; non esiste una
rotta UI/API che la importi e il catalogo built-in la rifiuta. Si parte da lì
per riscrivere la definizione osservando il sito, con fixture nostre.

Il convertitore copre un sottoinsieme documentato in
`internal/cardigann/MAPPING.md`; ciò che non è rappresentabile finisce in
`warning`/`unsupported` e la definizione prodotta resta `bozza`.

```bash
# una definizione: YAML nativo (bozza) su stdout
indexarr-def convert-cardigann ~/.config/Jackett/Indexers/example.yml

# una directory: un file per definizione, poi validazione
indexarr-def convert-cardigann -o ./native ~/.config/Jackett/Indexers
indexarr-def validate ./native
```

Le definizioni prodotte non hanno fixture: per entrare nel catalogo vanno
riscritte e verificate su pagine reali, con test offline e senza
`derived_from`. Le credenziali non vengono copiate nel YAML: restano
impostazioni che l'utente compila e che il vault cifra.

## Sicurezza e scrittura pulita

- **Le definizioni sono dati.** Non contengono codice, include, script o comandi.
  I template leggono solo le variabili elencate e l'unico filtro è `join`.
- Non esistono richieste implicite oltre a quelle dichiarate in `auth.steps`,
  `search.request`, `download.extract` (la pagina di dettaglio del campo
  `from`) e ai link di download restituiti dal sito; il transport applica
  timeout, limite di dimensione, rate limit per host, circuit breaker e blocco
  SSRF.
- I segreti (password, token, API key, cookie) sono redatti negli errori; gli URL
  di download possono contenere una passkey e non escono dalle API.
- **Nessuna passkey, cookie o credenziale nelle fixture.** Le fixture sono
  sintetiche e vengono conservate insieme alla definizione.
- **Clean-room (§6.6 di `indexarr2.md`).** Le definizioni si scrivono osservando
  il sito (pagine, risposte, documentazione delle API), non copiando file o
  codice di Jackett o Prowlarr. Ogni definizione registra autore, data e le
  fixture da cui è stata scritta; il catalogo è interamente nostro e
  distribuibile con la licenza scelta dal progetto.
- **Le bozze derivate restano locali.** Il convertitore Cardigann è un aiuto
  alla scrittura: produce `derived_from: cardigann`, non è raggiungibile da
  UI/API e il catalogo (e `make def-validate`) rifiuta queste bozze.
- niente CAPTCHA automatico: un indexer bloccato richiede intervento umano.

## Come aggiungere una definizione

1. Scegli la fonte più stabile: API ufficiale, feed, connettore di famiglia;
   usa lo scraping HTML solo se necessario.
2. Crea `examples/definitions/<id>.yaml` (o la directory del catalogo) con
   `schema: 1` e un `meta.id` slug univoco. Se parti da una bozza del
   convertitore, riscrivila e togli `derived_from`: il catalogo rifiuta le
   bozze derivate.
3. Dichiara `settings`, `categories` e `capabilities` in base a ciò che il sito
   espone davvero.
4. Descrivi l'autenticazione con passi espliciti; estrai CSRF/token e verifica
   `success`/`failure` con selettori o status.
5. Scrivi `search.request`, la paginazione e i campi tipizzati. Metti in
   `required` solo i campi senza cui la release è inutilizzabile.
6. Se il listing non espone il magnet, dichiara `download` con il campo
   `from` e gli estrattori della pagina di dettaglio; usa `resolve_on: grab`
   se vuoi rimandare l'apertura dei dettagli al momento del download.
7. Aggiungi `health` con una query reale e `min_rows` ragionevole.
8. Salva fixture sintetiche in `fixtures/<id>/` e dichiara i test con
   `min_rows`, `required` e, per i siti privati, login riuscito/fallito.
9. Esegui:

   ```bash
   indexarr-def validate examples/definitions
   indexarr-def check-fixtures examples/definitions/<id>.yaml
   indexarr-def fmt -w examples/definitions/<id>.yaml
   make def-validate
   ```

10. Verifica che la definizione passi anche i test del motore e che nessun dato
    sensibile sia finito nelle fixture.
11. Aggiorna `meta.updated`, autore e descrizione; per il catalogo rispetta i
    requisiti di §7.2 (`health`, test, fixture di login, stato `bozza` →
    `verificata`).

## Esempio completo

```yaml
schema: 1
meta:
  id: example-tracker
  name: Example Tracker
  language: it-IT
  type: private
  protocol: torrent
  author: indexarr examples
  urls:
    - https://tracker.example/
    - https://tracker-mirror.example/
  updated: "2026-10-23"

settings:
  - { name: username, type: text, required: true }
  - { name: password, type: password, required: true }

categories:
  - { site: "5",  standard: 5040, desc: "TV HD" }
  - { site: "12", standard: 2040, desc: "Film HD" }

capabilities:
  search: [q]
  tv-search: [q, season, ep, imdbid]
  movie-search: [q, imdbid]

auth:
  strategy: form
  steps:
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
    type: html
    rows: { selector: "table.torrents > tbody > tr" }
    fields:
      title:    { selector: ["a.title", "td.name a"], filters: [trim] }
      details:  { selector: "a.title", attr: href, type: url }
      download: { selector: "a.download", attr: href, type: url }
      category: { selector: "a.cat", attr: href, filters: [{ query_param: cat }], type: category }
      size:     { selector: "td.size", type: size }
      seeders:  { selector: "td.seeders", type: int }
      leechers: { selector: "td.leechers", type: int }
      date:     { selector: "td.date", type: date, layouts: ["02-01-2006 15:04"] }
    required: [title, download, size]

download:
  resolve_on: search
  from: details
  max_concurrent: 2
  extract:
    - magnet:  { selector: 'a[href^="magnet:"]', attr: href }
    - torrent: { selector: 'a[href$=".torrent"]', attr: href, type: url }

health:
  query: test
  min_rows: 1

tests:
  - name: search
    fixture: fixtures/example/search.html
    expect: { min_rows: 1, required: [title, download, size, category] }
  - name: login-ok
    fixture: fixtures/example/login-ok.html
    expect: { authenticated: true }
```

Esempi pronti e validati: `examples/definitions/` (`html-forum.yaml`,
`unit3d.yaml`, `newznab.yaml`).
