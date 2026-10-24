# indexarr 2 — piano di implementazione

Documento operativo collegato a [`indexarr2.md`](../indexarr2.md). Traduce la
visione in package, contratti Go e ordine di lavoro. Le decisioni qui fissate
sono vincolanti per il codice della milestone M0/M1: se una scelta va cambiata,
si cambia prima questo documento.

Stato: **in corso** — M0 (fondamenta) e M1 (motore e definizioni) presenti nei
package; catalogo, integrazione e distribuzione in avanzamento.

---

## 1. Regole di ingaggio

- Un package per volta, dipendenze dirette verso il basso. Nessun ciclo.
- `internal/engine` **non** importa `store`, `web`, `torznabapi`.
  `internal/definition` non importa nulla di indexarr.
- Solo dipendenze pure Go, senza CGO. già ammesse: `gopkg.in/yaml.v3`,
  `github.com/andybalholm/cascadia` (su `golang.org/x/net/html`).
- Ogni funzione pubblica ha un test. I test non toccano la rete: `httptest`,
  fixture su disco, clock iniettabile.
- `gofmt`, `go vet` e `go test` del proprio package devono passare prima di
  dichiarare finito un lavoro.
- Niente commit: il repository resta modificabile dal coordinatore.

## 2. Mappa dei package

| Package | Stato | Contenuto |
|---|---|---|
| `internal/vault` | M0 | AES-256-GCM, chiave da env o file, rotazione non richiesta in M0 |
| `internal/engine` | M0/M1 | contratti (`Connector`, `Module`, pipeline a stadi), transport con rate limit, circuit breaker, SSRF, sessioni, connettore da definizione |
| `internal/definition` | M1 | formato nativo v1, parsing YAML, validazione, upgrader, JSON Schema |
| `internal/family` | M1/M2 | connettori di famiglia: `unit3d`, `newznab` generico |
| `internal/store` | M0 | tabelle §8, migrazioni numerate, backfill da `sources` |
| `internal/torznabapi` | M3 | routing per indexer, alias Jackett, errori 100–900 |
| `internal/appsync` | M4 | client Sonarr/Radarr/Lidarr/Readarr/Whisparr/Mylar3/LazyLibrarian |
| `internal/ingest` | M2 | diventa orchestratore della pipeline (dopo il motore) |

## 3. Contratti condivisi

### 3.1 `internal/engine` — tipi

```go
type Protocol string
const (
    ProtocolTorrent Protocol = "torrent"
    ProtocolUsenet  Protocol = "usenet"
)

type Mode string
const (
    ModeSearch Mode = "search"
    ModeTV     Mode = "tvsearch"
    ModeMovie  Mode = "movie"
    ModeMusic  Mode = "music"
    ModeBook   Mode = "book"
)

type Item struct {
    Protocol    Protocol
    Title       string
    GUID        string
    DetailsURL  string
    DownloadURL string // può contenere la passkey: non esce mai dalle API
    Magnet      string
    InfoHash    string
    Size        int64
    Seeders     int64
    Leechers    int64
    Categories  []int
    Published   time.Time
    IMDbID      string
    TMDbID      string
    TVDbID      string
    Attributes  map[string]string
}

type Capabilities struct {
    Protocol   Protocol
    Modes      []Mode
    Params     map[Mode][]string
    Categories []int
}

type SearchRequest struct {
    Mode     Mode
    Query    string
    Season   int
    Episode  int
    Year     int
    IMDbID   string
    TMDbID   string
    TVDbID   string
    TVMazeID string
    RID      string
    Artist   string
    Album    string
    Author   string
    Title    string
    Categories []int
    Limit    int
    Offset   int
}

type LatestRequest struct {
    Categories []int
    Limit      int
}

type Payload struct {
    Body        []byte
    ContentType string
    Filename    string
    Magnet      string // quando il download è un redirect verso un magnet
}

type TestPhase struct {
    Name     string
    OK       bool
    Message  string
    Duration time.Duration
}

type TestResult struct {
    OK     bool
    Phases []TestPhase
}

type Connector interface {
    Capabilities() Capabilities
    Search(ctx context.Context, req SearchRequest) ([]Item, error)
    Latest(ctx context.Context, req LatestRequest) ([]Item, error)
    Download(ctx context.Context, item Item) (Payload, error)
    Test(ctx context.Context) (TestResult, error)
}
```

### 3.2 `internal/engine` — pipeline e moduli

La pipeline è una catena di middleware **per stadio**. Chi orchestra chiama
`Invoke` con il terminale dello stadio; un modulo si aggancia senza toccare gli
altri.

```go
type Stage string
const (
    StagePlan       Stage = "plan"
    StageRequest    Stage = "request"
    StageFetch      Stage = "fetch"
    StageExtract    Stage = "extract"
    StageNormalize  Stage = "normalize"
    StageEnrich     Stage = "enrich"
    StageDedup      Stage = "dedup"
    StageStore      Stage = "store"
    StageDistribute Stage = "distribute"
)

type Job struct {
    Indexer   IndexerRef // ID, Slug, Name, Protocol
    Connector Connector
    Search    SearchRequest
    Latest    *LatestRequest
    Response  *Response
    Items     []Item
    Payload   *Payload
}

type Handler func(context.Context, *Job) error
type Middleware func(Handler) Handler

type Registry struct{ /* ... */ }
func NewRegistry() *Registry
func (r *Registry) Use(stage Stage, mw Middleware)
func (r *Registry) Invoke(ctx context.Context, stage Stage, job *Job, terminal Handler) error
func (r *Registry) RegisterConnector(name string, factory ConnectorFactory)
func (r *Registry) Connector(name string) (Connector, bool)

type Migration struct {
    Version int
    Name    string
    SQL     string
}

type Module interface {
    Name() string
    Migrations() []Migration
    Register(r *Registry) error
}
```

- `Use` accoda i middleware nell'ordine di registrazione: il primo registrato è
  il più esterno.
- Un modulo spento non registra nulla: nessun effetto su schema o risposte.

### 3.3 `internal/engine` — transport

Un solo transport per processo, condiviso dai connettori. Deve garantire:
limite di dimensione, timeout, rate limit **per host**, circuit breaker **per
indexer**, rispetto di `Retry-After`, blocco SSRF.

```go
type Clock interface{ Now() time.Time }

type TransportConfig struct {
    Timeout          time.Duration
    UserAgent        string
    MaxBodyBytes     int64
    AllowPrivate     bool    // opt-in per indexer locali
    RatePerSecond    float64 // per host; 0 = illimitato
    Burst            int
    FailureThreshold int
    Cooldown         time.Duration
    Clock            Clock
}

type Response struct {
    Status int
    Header http.Header
    Body   []byte
    URL    *url.URL
}

type Transport struct{ /* ... */ }
func NewTransport(cfg TransportConfig) *Transport
func (t *Transport) Do(ctx context.Context, breakerKey string, req *http.Request) (*Response, error)
func (t *Transport) Download(ctx context.Context, breakerKey, rawURL string, maxBytes int64) (*Response, error)

var (
    ErrBreakerOpen   error
    ErrRateLimited   error
    ErrBlockedAddress error
    ErrBodyTooLarge  error
)
```

- Il breaker è per `breakerKey` (slug indexer). Dopo `FailureThreshold` errori
  consecutivi passa a `open` per `Cooldown`; poi `half-open` con una sola
  richiesta di prova.
- `429`/`503` con `Retry-After` aprono il breaker fino alla data indicata e
  restituiscono `ErrRateLimited` con l'attesa.
- L'SSRF guard risolve il nome e valida **tutti** gli IP; blocca loopback,
  private, link-local, unspecified, multicast, ULA e `169.254.169.254`. I
  redirect vengono rivalidati. `AllowPrivate` disattiva il blocco (indexer
  locale, opt-in esplicito).
- Il body è tagliato a `MaxBodyBytes` (default 16 MiB) e oltre si restituisce
  `ErrBodyTooLarge`.

### 3.4 `internal/definition`

Formato v1 come §6.4 di `indexarr2.md`, con due aggiunte retro-compatibili:

```yaml
connector: torznab        # opzionale: connettore nativo che interpreta la definizione
```

Tipi principali (i nomi Go sono vincolanti):

```go
const CurrentSchema = 1

type Definition struct {
    Schema       int
    Connector    string
    Meta         Meta
    Settings     []Setting
    Categories   []Category
    Capabilities Capabilities
    Auth         *Auth
    Search       Search
    Health       *Health
    Tests        []Test
}

type Meta struct {
    ID, Name, Language, Type, Protocol, Author, Description string
    URLs []string
    Updated string
}

type Setting struct {
    Name, Type, Label, Help, Default string
    Required bool
    Options []string
}

type Category struct {
    Site     string
    Standard int
    Desc     string
}

type Capabilities struct {
    Search      []string
    TVSearch    []string
    MovieSearch []string
    MusicSearch []string
    BookSearch  []string
}

type Auth struct {
    Strategy     string // none|api_key|basic|form|cookie|token
    Steps        []AuthStep
    Success      *Condition
    Failure      []Condition
    RefreshAfter time.Duration
}

type AuthStep struct {
    Get     string
    Post    string
    Form    map[string]string
    Headers map[string]string
    Extract map[string]Extractor
}

type Extractor struct {
    Selector string
    Attr     string
    JSON     string
    Regex    string
}

type Condition struct {
    Selector string
    Message  string
    Status   int
}

type Search struct {
    Request    Request
    Pagination Pagination
    Response   Response
}

type Request struct {
    Get     string
    Post    string
    Params  map[string]string
    Form    map[string]string
    Headers map[string]string
}

type Pagination struct {
    Type     string // none|page|offset|cursor
    Start    int
    Step     int
    MaxPages int
    Param    string
    StopWhen string // empty_rows
}

type Response struct {
    Type     string // html|json|xml
    Rows     Selector
    Fields   map[string]Field
    Required []string
}

type Field struct {
    Selector Selector // string o lista: fallback in ordine
    Attr     string
    Type     string // text|int|size|date|url|category
    Path     string // json: percorso puntato
    Filters  []Filter
    Layouts  []string
}

type Health struct {
    Query   string
    MinRows int
}

type Test struct {
    Name    string
    Fixture string
    Expect  Expectation
}
```

`Selector` e `Filter` hanno unmarshalling YAML personalizzato:

- `Selector` accetta `"a.title"` oppure `["a.title", "td.name a"]`.
- `Filter` accetta `"trim"` oppure `{ "query_param": "cat" }`.

Funzioni pubbliche:

```go
func Parse(body []byte) (*Definition, error) // YAML → struct + Validate
func Load(path string) (*Definition, error)
func (d *Definition) Validate() error
func Fingerprint(body []byte) string    // sha256 esadecimale
func Upgrade(body []byte) ([]byte, int, error) // catena vN→vN+1, schema corrente
func JSONSchema() []byte                // //go:embed schema.json
```

Validazione minima: `schema == 1`; `meta.id` slug (`^[a-z0-9][a-z0-9-]*$`);
`meta.name` e `meta.urls` non vuoti; URL `http`/`https`; `protocol` in
`torrent|usenet`; `type` in `public|semi-private|private`; `settings.name`
univoci e slug; `standard` nelle categorie 1000–8999; `capabilities` solo
parametri noti (`q, season, ep, imdbid, tvdbid, tmdbid, tvmazeid, rid, artist,
album, author, title, year, genre`); `auth.strategy` nell'enum; ogni step con
`get` o `post`; `search.response.type` nell'enum; ogni campo `required` deve
esistere in `response.fields`; fixture dei test relative e senza `..`.

`schema.json` è il JSON Schema draft 2020-12 pubblicato: deve essere JSON
valido, con `$id`, e descrivere il formato. `internal/definition` include
almeno `testdata/example.yaml` (quello del §6.4) e un caso per ogni tipo di
campo/auth.

### 3.5 `internal/vault`

```go
var ErrKeyRequired = errors.New("vault: master key required")

type Vault struct{ /* ... */ }
func New(key []byte) (*Vault, error)                        // 32 byte
func FromEnvOrFile(envVar, keyPath string) (*Vault, error)  // env: base64/hex; file 0600 creato se assente
func (v *Vault) Seal(plaintext string) (nonce, ciphertext []byte, err error)
func (v *Vault) Open(nonce, ciphertext []byte) (string, error)
func (v *Vault) SealString(plaintext string) (string, error) // base64(nonce||ciphertext)
func (v *Vault) OpenString(blob string) (string, error)
```

- AES-256-GCM, nonce casuale di 12 byte per ogni Seal.
- La chiave non viene mai loggata né inclusa negli errori.
- `FromEnvOrFile`: se la env è impostata e non vuota vince; altrimenti legge il
  file, lo crea con 32 byte casuali e permessi `0600` se manca.
- Formati env accettati: base64 standard/raw, hex (64 caratteri).

### 3.6 `internal/store` — schema §8

Nuove tabelle, in una migrazione numerata (`schema_migrations`), con le colonne
di §8 più:

- `indexers.legacy_source_id INTEGER` — ponte di migrazione, `UNIQUE` parziale;
- `indexer_state.circuit` (`closed|open|half-open`), `disabled_until`;
- `definitions.family` per i connettori di famiglia.

Tabella di versionamento:

```sql
schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  applied_at INTEGER NOT NULL
)
```

API Go (file `internal/store/indexarr2.go`):

```go
type Definition struct { ID string; SchemaVersion int; Family, State, Origin,
    Author, Body, Hash string; UpdatedAt time.Time }
type Indexer struct { ID int64; DefinitionID, Protocol, Slug, Name string;
    Enabled bool; Priority int; ConfigJSON, Tags string; ProxyID *int64;
    QueryLimit, GrabLimit int; LegacySourceID int64; CreatedAt, UpdatedAt time.Time }
type Secret struct { IndexerID int64; Key string; Nonce, Ciphertext []byte; UpdatedAt time.Time }
type IndexerState struct { ... }
type ReleaseSource struct { ReleaseID, IndexerID int64; FirstSeenAt, LastSeenAt time.Time; Seeders, Leechers int64; DownloadRef string }
type IndexerHistory struct { ... }
type Application struct { ... }
type AppIndexer struct { ... }
type Proxy struct { ... }

func (s *Store) UpsertDefinition(ctx context.Context, d Definition) error
func (s *Store) GetDefinition(ctx context.Context, id string) (Definition, error)
func (s *Store) ListDefinitions(ctx context.Context) ([]Definition, error)
func (s *Store) CreateIndexer(ctx context.Context, in Indexer) (int64, error)
func (s *Store) GetIndexer(ctx context.Context, id int64) (Indexer, error)
func (s *Store) GetIndexerBySlug(ctx context.Context, slug string) (Indexer, error)
func (s *Store) ListIndexers(ctx context.Context) ([]Indexer, error)
func (s *Store) UpdateIndexer(ctx context.Context, in Indexer) error
func (s *Store) DeleteIndexer(ctx context.Context, id int64) error
func (s *Store) PutSecret(ctx context.Context, in Secret) error
func (s *Store) GetSecret(ctx context.Context, indexerID int64, key string) (Secret, error)
func (s *Store) ListSecrets(ctx context.Context, indexerID int64) ([]Secret, error)
func (s *Store) UpsertIndexerState(ctx context.Context, in IndexerState) error
func (s *Store) GetIndexerState(ctx context.Context, indexerID int64) (IndexerState, error)
func (s *Store) RecordReleaseSource(ctx context.Context, in ReleaseSource) error
func (s *Store) ListReleaseSources(ctx context.Context, releaseID int64) ([]ReleaseSource, error)
func (s *Store) RecordIndexerHistory(ctx context.Context, in IndexerHistory) (int64, error)
func (s *Store) ListIndexerHistory(ctx context.Context, indexerID int64, limit int) ([]IndexerHistory, error)
func (s *Store) BackfillLegacySources(ctx context.Context) (int, error)
```

`BackfillLegacySources` è idempotente: per ogni riga di `sources` crea una
definizione `legacy-<id>` (origin `local`, state `verificata`, family = kind
della sorgente) e un indexer con `legacy_source_id`, slug univoco derivato dal
nome, `config_json` con URL/categorie/query (mai segreti). Non tocca
`api_key`, `password` e `username`: restano in `sources` finché il vault non è
collegato.

La migrazione gira in transazione e viene registrata in `schema_migrations`.
Un database esistente continua a funzionare: le vecchie tabelle non vengono
rimosse.

## 4. Ordine di lavoro

1. **Ondata 1 (M0)**: `vault`, `definition`, `store`, `engine` (contratti +
   transport). Package disgiunti, in parallelo.
2. **Ondata 2 (M1)**: connettore `engine/definition.go`, `family/unit3d`,
   `family/newznab`, sessioni/login.
3. **Ondata 3 (M3)**: `torznabapi` per indexer + alias Jackett + errori.
4. **Ondata 4 (M4)**: `appsync`.

## 5. Criterio di uscita M0

- `go test ./...` verde con i nuovi package.
- `go vet ./...` verde.
- Un database reale copiato in `/tmp` si apre, migra, e contiene una riga
  `indexers` per ogni `sources` con `legacy_source_id` valorizzato.
- Nessuna regressione nei test esistenti.

## 6. Stato al 23 ottobre 2026

### Fatto e verificato

| Area | Package / file | Verifica |
|---|---|---|
| Vault AES-256-GCM, chiave da env o file `0600` | `internal/vault` | test + race |
| Formato nativo v1, validazione, upgrader, JSON Schema | `internal/definition` | test, 3 definizioni di esempio |
| Tabelle §8, migrazioni numerate, backfill idempotente da `sources` | `internal/store/migrations.go`, `indexarr2.go` | test su DB reale |
| Contratti `Connector`, `Module`, pipeline a 9 stadi (non ancora invocati a runtime, WP4) | `internal/engine` | test + race |
| Transport: rate limit per host, circuit breaker, `Retry-After`, SSRF, limiti | `internal/engine` | test + race |
| Motore definizioni: HTML/JSON/XML, login/CSRF/cookie/token/basic, paginazione, tipi, fallback, dedup | `internal/engine` | test + fixture |
| Connettori di famiglia UNIT3D, Newznab, Torznab, Gazelle, NexusPHP, TorrentTrader, Xbtit | `internal/family` | test + fixture (stato `bozza`) |
| Limiti query/grab per indexer e proxy HTTP/SOCKS per indexer | `internal/indexer`, `internal/engine`, `internal/torznabapi` | test + race |
| Runtime store+vault+definizione+connettore, run e provenance | `internal/indexer` | test + race |
| Scheduler degli indexer nativi, skip del ponte legacy | `internal/native` | test + race |
| Catalogo built-in (bozza) e sync nel DB | `internal/catalog` | test (7 definizioni) |
| UI web per definizioni e indexer nativi (form dai `settings`, test/run) | `internal/web`, `internal/indexersvc` | test + QA |
| UI web per applicazioni e anteprima import Prowlarr/Jackett | `internal/web` | test + QA |
| API `/api/v2`: definizioni, catalogo, indexer, applicazioni, import, legacy preview | `internal/adminapi` | test + QA reale |
| App sync: riconciliazione periodica con le \*arr, `applications`/`app_indexers` | `internal/syncjob` | test + race |
| Payload Usenet `.nzb`, protocollo per release, proxy download opaco | `internal/store` (migrazione 2), `internal/downloadproxy`, `internal/torznabapi` | test + race |
| Client Deluge, rTorrent, SABnzbd, NZBGet | `internal/downloadclient` | test + race |
| Import da Prowlarr e Jackett (piano di migrazione) | `internal/importers` | test + race |
| Anteprima di migrazione delle sorgenti legacy verso Torznab nativo | `internal/legacyimport` | test + race |
| Apply/revert della migrazione legacy con vault e disattivazione della sorgente | `internal/legacyimport`, `internal/adminapi` | test + QA |
| Provenance multi-indexer (`indexers`, `provenance`), seeders massimi, `StaleProvenance` | `internal/store`, `internal/torznabapi` | test + race |
| Simulazione end-to-end locale e Docker (Torznab, Prowlarr, Sonarr, client, tracker HTML) | `internal/simmock`, `cmd/simmock`, `sim/` | test + `sim/run.sh` + Docker |
| Convertitore locale Cardigann → bozza nativa v1, **solo come aiuto alla scrittura** (le bozze non entrano nel catalogo, WP0) | `internal/cardigann`, `indexarr-def convert-cardigann` | test + race; 926/927 file generati sul corpus reale (non "funzionanti") |
| Import Jackett/Prowlarr in un DB usa-e-getta | `cmd/indexarr-import`, `internal/importers` | test + prova su istanze reali |
| UI ridisegnata: sidebar, Indexer unificato, Attività, Sistema, redirect legacy | `internal/web` | test + `scripts/ui-smoke.sh` (26 route, 444 link) |
| i18n: inglese default + it/fr/de/es/zh/pl, 427 chiavi, selettore lingua | `internal/i18n`, `internal/web` | test parità + QA |
| Prestazioni: indici, FTS, XML, pool SQLite | `internal/store`, `internal/torznabapi` | benchmark + `docs/PERFORMANCE.md` |
| Simulazione con Prowlarr/Sonarr/Radarr reali | `sim/real/` | 62 asserzioni verdi |
| Client Sonarr/Radarr/Lidarr/Readarr/Whisparr e reconcile | `internal/appsync` | test + race |
| CLI `indexarr-def`, esempi, documentazione | `cmd/indexarr-def`, `internal/deftool` | test + CI |
| Alias Jackett/Newznab, routing per indexer, filtri aggregati, errori 100–900 | `internal/torznabapi` | test + QA |

### Cablaggio nel binario

- All'avvio: chiave del vault (`INDEXARR_MASTER_KEY` o `<db>.key`), catalogo
  built-in sincronizzato, trasporto condiviso, registry delle famiglie,
  `indexer.Manager`, `native.Runner`, `indexersvc`, `syncjob`, resolver del
  proxy download, `/api/v2` e pagine web native.
- Gli indexer ponte (`connector: torznab|rss`, cioè quelli creati dal backfill)
  restano sullo scheduler legacy delle `sources`; il runner nativo li salta.
- Gli indexer nativi creati da `/api/v2` o dalla UI vengono eseguiti subito e
  poi ogni `default_interval`.
- Il sync delle applicazioni parte dopo 2 secondi e poi ogni 15 minuti; usa
  `public_url` (o l'indirizzo locale come fallback) e l'API key primaria.
- Il resolver del proxy serve i payload non ancora salvati leggendo il
  `download_ref` cifrato e chiamando il connettore dell'indexer.
- I limiti query/grab per indexer sono applicati dal runtime e dal proxy;
  i proxy HTTP/SOCKS dell'indexer sono usati dal trasporto dedicato.
- Le pagine `/applications` e `/import` coprono il sync \*arr e l'anteprima di
  import Prowlarr/Jackett; l'applicazione del piano di import è il passo dopo.
- Gli errori del vault non bloccano l'avvio: il percorso legacy continua a
  funzionare e solo gli indexer con segreti restano indisponibili.

### Prossimi passi

Il lavoro aperto è in [`NEXT-STEPS.md`](NEXT-STEPS.md), diviso in work package
con stub e criteri di accettazione (WP0–WP14). In particolare:

- il convertitore Cardigann è solo uno strumento di scrittura: **nessun import
  Cardigann da UI/API** (WP0);
- la pipeline a stadi e i moduli descritti in §3.2 non sono ancora invocati a
  runtime (WP4);
- il rate limit per host esiste nel transport ma è disattivato di default e
  il percorso legacy non lo usa (WP2).

### Verifica

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./...
make def-validate
./scripts/qa.sh
```


## 7. Aggiornamenti dei contratti (23 ottobre 2026, WP0–WP7)

Questi punti integrano i contratti dei §3 e vanno considerati vincolanti:

- **Bozze derivate.** `Meta.DerivedFrom` (`derived_from`) è un campo opzionale
  del formato v1. Il convertitore Cardigann lo valorizza sempre a `cardigann`;
  una definizione derivata resta locale, non entra nel catalogo e viene
  rifiutata da `catalog.Builtins()` e da `indexarr-def validate -catalog`.
- **Download dalla pagina di dettaglio.** `Definition.Download *Download`
  (`resolve_on`, `from`, `max_concurrent`, `extract` con target
  magnet/torrent). È opzionale, schema 1 invariato; il motore risolve durante
  la ricerca (solo dopo deduplica e limite) o al grab, con concorrenza e cache.
  `resolve_on` assente vale `grab`: la ricerca non apre i dettagli.
- **Famiglia RSS.** Il connettore `rss` è nativo e usa i parser puri di
  `internal/feed` attraverso il transport. Un indexer `rss` è ponte legacy
  **solo** se `LegacySourceID > 0`, come per Torznab.
- **Politeness.** `engine.Politeness` è l'unico budget per host condiviso da
  percorso nativo e legacy (limiter, breaker, Retry-After). Le impostazioni
  `host_rate` (1/s), `host_burst` (4), `breaker_threshold` (5),
  `breaker_cooldown` (default 10 min dal WP20; 60s nella prima stesura) sono modificabili e non applicano SSRF al percorso
  legacy, che deve raggiungere Jackett/Prowlarr in LAN.
- **Vault e segreti legacy.** La chiave si risolve da env, env-file, config,
  file legacy, user config, fallback; `SecretCodec` cifra `api_key`, `password`
  e `url` delle `sources` con prefisso `vault:v1:` e migrazione idempotente.
  I backup del DB non includono la chiave; il restore verifica che la chiave
  apra almeno un segreto.
- **Catalogo verificato.** Le definizioni `extto`, `torrentgalaxy`, `corsaro`,
  `knaben` sono `verificate` con fixture reali ripulite; `torrentleech` resta
  `bozza` finché non c'è una fixture reale con rsskey personale sanificata.
