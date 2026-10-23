# indexarr 2 — Prossimi passi (work package per gli agenti)

Data: 23 ottobre 2026
Origine: revisione del codice e della documentazione dopo le ondate 1–8
(commit `f1ef35b`…`384e541`).
Collegati: [visione](../indexarr2.md), [piano e contratti](INDEXARR2-PLAN.md),
[formato definizioni](DEFINITIONS.md), [manifesto](MANIFESTO.md).

Questo è il documento operativo da cui partono gli agenti. Ogni work package
(WP) è autonomo: **perché** serve (con evidenza), **cosa** fare, **dove**,
**stub** di riferimento, **criteri di accettazione** e **cosa non fare**. Gli
stub indicano forma e confini, non sono codice da incollare alla cieca:
l'agente adatta nomi e dettagli al codice reale e lo verifica con i test.

> **Decisione del coordinatore (23/10, dopo il checkpoint `4e429e5`).** Il
> database di produzione attuale (`data/indexarr.db`) **non va preservato**: si
> può buttare e rifare. I dati di partenza si ricopiano dalle istanze Jackett
> (192.168.1.161:9117) e Prowlarr (:9696) **in sola lettura**: nessuna
> scrittura, nessuna modifica di configurazione, nessun riavvio su quelle
> istanze. Conseguenze:
>
> - niente migrazione dei dati né rollback del vault: WP8 diventa
>   **ripartenza pulita**;
> - il percorso legacy (`sources`, ponte `legacy-*`, `legacyimport`, tab
>   Nativi/Legacy) non deve più sopravvivere per proteggere dati esistenti:
>   si rimuove in **WP16**, dopo WP8;
> - la compatibilità con installazioni precedenti non è un vincolo di
>   sviluppo; resta un vincolo solo il formato delle definizioni v1 e i
>   contratti Torznab/Newznab verso le *arr.

---

## 0. Stato reale (base della revisione)

Misurato il 23/10/2026 sulla build `384e541`:

| Fatto | Evidenza |
|---|---|
| Build, vet e test verdi | `go build ./... && go vet ./... && go test ./...` |
| In produzione **0 indexer nativi**: 37/37 passano dal ponte legacy | `select count(*), sum(legacy_source_id is not null) from indexers` → `37\|37` |
| 0 applicazioni, `release_sources` vuota | query sul DB di produzione |
| Le 37 sorgenti: 3 Torznab (Jackett, Prowlarr, MirCrew), 32 listing HTML (18 ExtTo, 13 TorrentGalaxy, + Corsaro via ExtTo), 2 feed RSS (Knaben, TorrentLeech) | tabella `sources` |
| La pipeline a stadi e i moduli non sono collegati: `Registry.Invoke` e `RegisterModule` non sono mai chiamati a runtime; `Module.Migrations()` non viene applicato | `grep -rn "\.Invoke(\|RegisterModule(" internal cmd` |
| Rate limit per host disattivato: `cmd/indexarr` non imposta `RatePerSecond` (default 0 = illimitato) | `cmd/indexarr/main.go:134`, `engine/transport.go` `normalizeTransportConfig` |
| Il percorso legacy (100% del traffico) usa un `http.Client` proprio, senza rate limit né breaker; i listing HTML aprono 6 worker per sorgente sulle pagine di dettaglio | `internal/ingest/engine.go:136`, `internal/feed/html_listing.go:265` |
| Chiave del vault accanto al DB (`data/indexarr.db.key`); `internal/backup` non la salva; credenziali delle `sources` in chiaro | `cmd/indexarr/main.go:124`, `data/` |
| Il formato v1 non sa risolvere il magnet da una pagina di dettaglio: impossibile oggi scrivere ExtTo/TorrentGalaxy/Corsaro come definizioni | `definition.Definition` non ha una sezione `download` |
| Nessuna famiglia RSS nativa; il nome `rss` è trattato sempre come ponte | `internal/indexer/indexer.go:267`, `internal/legacyimport/legacyimport.go:88` |
| Catalogo: 7 definizioni generiche, tutte `bozza`, solo fixture sintetiche | `internal/catalog/catalog.yaml` |
| Memoria del servizio: ~64 MiB dopo 4 min, ~73 MiB dopo 10 min (prima di indexarr 2: ~30 MiB). Obiettivo manifesto < 40 MiB | `ps -o rss= -p <pid>` |
| CI solo Ubuntu, senza matrice né budget di risorse | `.github/workflows/qa.yml` |

---

## 1. Regole per ogni agente

1. Leggi questo file, poi `docs/INDEXARR2-PLAN.md` §1–§3 (contratti) e, se
   tocchi le definizioni, `docs/DEFINITIONS.md`.
2. **Un WP per volta.** Non allargare il perimetro: se scopri altro, annotalo
   in fondo al WP come "scoperto" e fermati lì.
3. **Niente Cardigann a runtime, niente definizioni convertite nel catalogo**
   (WP0). Non copiare file o codice di Jackett/Prowlarr.
4. Test offline: `httptest`, fixture su disco, clock iniettabile. Nessuna rete
   nei test.
5. Prima di dichiarare finito: `gofmt -l .` vuoto, `go vet ./...`,
   `go test ./...`, `go test -race ./...` dei package toccati,
   `make def-validate`, `./scripts/qa.sh`.
6. Nessun segreto in log, errori, fixture, commit. Le fixture prese da siti
   reali vanno ripulite (passkey, cookie, nomi utente, rsskey negli URL).
7. A fine WP aggiorna **solo** la riga del WP nella tabella §3 di questo file
   (stato + commit) e, se cambia un contratto, `INDEXARR2-PLAN.md`. Lo stato
   della checklist di parità vive in `indexarr2.md` §3: aggiornalo lì.
8. Operazioni sul servizio di produzione (WP8) solo con conferma esplicita del
   coordinatore.

---

## 2. Ordine e dipendenze

```text
Gruppo A (in parallelo, file disgiunti)
  WP0 policy convertitore      → internal/definition, internal/cardigann, internal/catalog
  WP1 vault                    → internal/vault, cmd/indexarr, internal/store/sources.go, internal/backup
  WP3 memoria                  → solo indagine + fix mirati
  WP12 CI e budget             → .github/, scripts/

Gruppo B (in sequenza: toccano engine/transport e indexer)
  WP2 rate limit + percorso legacy educato
  WP11 FlareSolverr per indexer nativi
  WP4 pipeline e moduli collegati
  WP9 health check e stati
  WP10 refresh seeders

Gruppo C (in sequenza: copertura reale)
  WP5 download.resolve nel formato  ─┐
  WP6 famiglia RSS nativa           ─┼─► WP7 prime definizioni verificate ─► WP8 dogfooding in produzione
                                     │
Gruppo D (dopo C)                    │
  WP13 parità residua (import applicato, Mylar3/LazyLibrarian, rollback sync, i18n legacy)

Gruppo E (UI, in parallelo ad A/B; tocca internal/web: coordinarsi con WP1 e WP2
che aggiungono piccoli riquadri)
  WP14 fasi 1–3 subito; fase 4 dopo; fase 5 dopo WP7 e WP13
```

Priorità se c'è un solo agente: WP0 → WP14 fase 1 → WP1 → WP2 → WP5 → WP6 →
WP7 → WP8 → WP14 fasi 2–3 → WP3 → WP4 → WP9 → WP10 → WP11 → WP12 → WP13 →
WP14 fasi 4–5.

**Ordine aggiornato dopo il checkpoint `4e429e5`** (WP0, WP1, WP2, WP5, WP6,
WP7, WP12, WP14 fase 1 fatti):

```text
WP-R correzioni ─► WP8 ripartenza pulita ─► WP16 rimozione legacy ─► WP14 fasi 2–3
                   (parallelo: WP3 memoria, WP15 parametri condizionali)
poi: WP4 → WP9 → WP10 → WP11 → WP13 → WP14 fasi 4–5
```

WP16 viene prima delle fasi 2–3 di WP14 perché metà dell'"ostico" della UI
(tab Nativi/Legacy, definizioni `legacy-*`, pagine sorgenti) sparisce con il
codice legacy: inutile riprogettare schermate destinate a sparire.

---

## 3. Tabella di avanzamento

| WP | Titolo | Stato | Commit |
|---|---|---|---|
| WP0 | Convertitore solo come aiuto alla scrittura | fatto | `4e429e5` |
| WP1 | Vault: chiave fuori dai dati, backup chiave, segreti legacy cifrati | fatto (backend; riquadro UI in WP14) | `4e429e5` |
| WP2 | Rate limit di default e percorso legacy sul transport educato | fatto | `4e429e5` |
| WP3 | Memoria: tornare sotto 40 MiB a riposo | da fare | |
| WP4 | Pipeline a stadi e moduli collegati a runtime | da fare | |
| WP5 | `download.resolve`: magnet/torrent dalla pagina di dettaglio | fatto | `4e429e5` |
| WP6 | Famiglia RSS/Atom nativa | fatto | `4e429e5` |
| WP7 | Prime definizioni native verificate (ExtTo, TorrentGalaxy, Corsaro, Knaben, TorrentLeech RSS) | fatto (4/5 verificati; TorrentLeech bozza) | `4e429e5` |
| WP8 | Ripartenza pulita: nuovo DB, import in sola lettura da Jackett/Prowlarr | da fare | |
| WP9 | Health check e transizioni di stato del catalogo | da fare | |
| WP10 | Refresh periodico dei seeders | da fare | |
| WP11 | FlareSolverr per indexer nativi e stato `blocked` | da fare | |
| WP12 | CI multipiattaforma e budget di risorse | fatto (Windows noto; budget CI 80 MiB fino a WP3) | `4e429e5` |
| WP13 | Parità residua | da fare | |
| WP14 | Interfaccia: da pannello tecnico a prodotto semplice ([UI-GUIDE](UI-GUIDE.md)) | fase 1 fatta; 1b subito; 2–5 dopo WP16 | `4e429e5` |
| WP-R | Correzioni dalla revisione del checkpoint | da fare | |
| WP15 | Parametri condizionali nelle definizioni (v1.1) | da fare | |
| WP16 | Rimozione del percorso legacy | da fare | |

---

## WP0 — Convertitore solo come aiuto alla scrittura

**Decisione del progetto.** indexarr non usa Cardigann. Il convertitore
(`internal/cardigann`, `indexarr-def convert-cardigann`) esiste **solo** per
aiutare a scrivere definizioni native: produce una bozza locale da cui si
parte per riscrivere la definizione osservando il sito. Una bozza convertita
non entra mai nel catalogo, non viene importata da UI/API e non è un percorso
di migrazione per gli utenti.

**Perché.** Fino al 23/10 la documentazione pianificava un "import Cardigann
applicato da UI/API"; la documentazione è stata corretta, il codice no: oggi
nulla impedisce a una bozza convertita di finire nel catalogo. Le bozze
derivano da un corpus GPL e non devono finire nel catalogo distribuito.

**Cosa fare.**

1. Aggiungere a `meta` il campo `derived_from` (formato v1, JSON Schema,
   `DEFINITIONS.md`).
2. Il convertitore scrive sempre `derived_from: cardigann`.
3. Il catalogo built-in rifiuta definizioni con `derived_from` non vuoto o con
   `author: cardigann-import`; `make def-validate` fallisce sullo stesso caso
   per i file in `internal/catalog/definitions/`.
4. `POST /api/v2/definitions` e la UI accettano una bozza derivata solo come
   `origin: local`, `state: bozza`, e mostrano l'avviso "bozza derivata: da
   riscrivere".
5. Nessuna rotta UI/API di "import Cardigann". Il convertitore resta solo CLI.
6. Aggiornare `cmd/indexarr-def` help e `DEFINITIONS.md` §`convert-cardigann`:
   "strumento di scrittura, non di migrazione".

**Stub.**

```go
// internal/definition/types.go — in Meta
// DerivedFrom marks a draft produced by a conversion tool. Derived drafts are
// writing aids: they stay local and the built-in catalog rejects them.
DerivedFrom string `yaml:"derived_from,omitempty" json:"derived_from,omitempty"`

// internal/cardigann/convert.go
const DerivedFrom = "cardigann"
// ... in the Meta literal:
DerivedFrom: DerivedFrom,

// internal/catalog/catalog.go — in load(), after definition.Parse
if def.Meta.DerivedFrom != "" || strings.EqualFold(def.Meta.Author, "cardigann-import") {
    return nil, fmt.Errorf("catalog: %s: derived drafts (%q) cannot enter the catalog; rewrite it from observed fixtures",
        item.File, def.Meta.DerivedFrom)
}
```

**Accettazione.**

- Test: una definizione con `derived_from` nel catalogo fa fallire
  `catalog.Builtins()` e `make def-validate`.
- Test: il convertitore emette sempre `derived_from: cardigann`.
- Test: `POST /api/v2/definitions` con bozza derivata → salvata `local`/`bozza`,
  mai `builtin`.
- Nessuna rotta in `internal/web` o `internal/adminapi` importa
  `internal/cardigann` (test di architettura con `go list -deps` o un controllo
  sugli import).

**Non fare.** Non eliminare il convertitore; non aggiungere un runtime
Cardigann; non usare il corpus nei test committati.

---

## WP1 — Vault: chiave fuori dai dati, backup della chiave, segreti legacy cifrati

**Perché.** La chiave `<db>.key` sta nella stessa cartella del DB: chi copia
`data/` (backup di un volume Docker, rsync) copia anche la chiave, e la
protezione promessa ("backup e copie del DB") non esiste. Al contrario,
`internal/backup` non salva la chiave: un restore su un'altra macchina perde i
segreti nativi. Le credenziali delle `sources` (API key di Jackett/Prowlarr,
password) restano in chiaro; l'URL di TorrentLeech contiene la rsskey nel path.

**Cosa fare.**

1. Ordine di risoluzione della chiave:
   1. `INDEXARR_MASTER_KEY` (valore);
   2. `INDEXARR_MASTER_KEY_FILE` (percorso; convenzione Docker secrets);
   3. `vault_key_path` nel file di configurazione;
   4. file legacy `<db>.key` **se esiste** (compatibilità), con warning
      "chiave nella cartella dati: spostala";
   5. default nuovo: `os.UserConfigDir()/indexarr/master.key`;
   6. se `UserConfigDir` non è disponibile (container senza HOME): `<db>.key`
      con warning, e la documentazione Docker raccomanda il punto 2.
2. Comando `indexarr vault export-key` (stampa la chiave in base64 su stdout,
   con avviso) e `indexarr vault move-key -to <path>` (sposta il file legacy).
3. UI (Sistema → Manutenzione/Backup): riquadro "Chiave del vault" con
   posizione, avviso se è nella cartella dati, istruzioni di backup. Il backup
   del DB **non** include la chiave; il restore verifica che la chiave apra
   almeno un segreto e, se no, lo dice chiaramente.
4. Segreti legacy: `api_key`, `password` e l'URL delle `sources` cifrati a
   riposo con prefisso versionato, decifrati in lettura dallo store. Migrazione
   all'avvio idempotente; senza vault i valori restano in chiaro (nessun blocco
   del percorso legacy).
5. Redazione: l'URL delle sorgenti nei log/UI passa da `redactSecrets` anche
   per segmenti di path lunghi e esadecimali (caso rsskey).

**Stub.**

```go
// internal/vault/keypath.go
// KeySource describes where the master key came from, for the UI and logs.
type KeySource struct {
    Kind      string // "env" | "env-file" | "config" | "legacy-data-dir" | "user-config" | "data-dir-fallback"
    Path      string // empty for "env"
    InDataDir bool   // true when the key sits next to the database
}

// Resolve applies the documented order and returns the vault and where its key
// lives. It never logs or returns key material.
func Resolve(opts ResolveOptions) (*Vault, KeySource, error)

type ResolveOptions struct {
    EnvKey, EnvKeyFile string // "INDEXARR_MASTER_KEY", "INDEXARR_MASTER_KEY_FILE"
    ConfigPath         string // vault_key_path from the config file
    DBPath             string // used for the legacy <db>.key and InDataDir
    UserConfigDir      func() (string, error) // os.UserConfigDir, injectable in tests
}

// internal/store/secrets_codec.go
// SecretCodec seals legacy source credentials at rest. *vault.Vault satisfies
// it through a small adapter in cmd/indexarr; store does not import vault.
type SecretCodec interface {
    SealString(plaintext string) (string, error)
    OpenString(blob string) (string, error)
}

const sealedPrefix = "vault:v1:"

func (s *Store) SetSecretCodec(c SecretCodec) // nil = plaintext (legacy behaviour)

// sealField / openField wrap api_key, password and url in CreateSource,
// UpdateSource and scanSource. Values without the prefix are returned as-is.
func (s *Store) sealField(value string) (string, error)
func (s *Store) openField(value string) (string, error)

// SealLegacySecrets rewrites plaintext source credentials once; it is called
// at start-up after SetSecretCodec and is idempotent.
func (s *Store) SealLegacySecrets(ctx context.Context) (int, error)
```

**Accettazione.**

- Test della risoluzione per ognuno dei 6 casi (con `UserConfigDir` finto).
- Test: con codec, la colonna `sources.api_key` nel file SQLite non contiene il
  valore in chiaro; `GetSource` lo restituisce in chiaro.
- Test: DB con valori in chiaro + codec → `SealLegacySecrets` li cifra, una
  seconda chiamata non cambia nulla.
- Test: backup + restore su directory nuova con la stessa chiave → segreti
  leggibili; con chiave diversa → errore esplicito, nessun crash.
- `./scripts/qa.sh` verde; nessun valore segreto in `go test -v` output.

**Non fare.** Non bloccare l'avvio se la chiave manca; non includere la
chiave nei backup; non cambiare il formato `vault.SealString`.

---

## WP2 — Rate limit di default e percorso legacy sul transport educato

**Perché.** Oggi nessun rate limit è attivo: il transport nativo parte con
`RatePerSecond = 0` e il percorso legacy ha un client proprio. 18 sorgenti
ExtTo e 13 TorrentGalaxy colpiscono gli stessi due host, ognuna con 6 worker
sulle pagine di dettaglio: è il caso tipico che porta a 429 e ban.

**Cosa fare.**

1. Impostazioni (con default prudenti, modificabili in UI): `host_rate`
   (default 1 richiesta/s), `host_burst` (default 4), `breaker_threshold` (5),
   `breaker_cooldown` (60s). Valori passati al transport nativo in
   `cmd/indexarr`.
2. Esporre dal package `engine` un `http.RoundTripper` "educato" che applica
   limiter per host + breaker per host + `Retry-After`, **senza** guard SSRF
   (il percorso legacy deve poter raggiungere Jackett/Prowlarr in LAN).
3. `ingest.New` usa quel RoundTripper. Il limiter è **condiviso** tra nativo e
   legacy (stessa istanza), così un host non riceve il doppio del budget.
4. I listing HTML legacy: worker sui dettagli ridotti a 2 e sempre dietro al
   limiter.
5. Metriche: contatori 429/403/breaker-open per host nella pagina Attività.

**Stub.**

```go
// internal/engine/polite.go
// Politeness is the per-host budget shared by every HTTP path of the process.
type Politeness struct {
    limiter *hostLimiter
    breaker *circuitBreaker
    clock   Clock
}

func NewPoliteness(rate float64, burst, threshold int, cooldown time.Duration, clock Clock) *Politeness

// RoundTripper wraps base: wait for a host token, check the host breaker,
// record success/failure, honour Retry-After on 429/503.
func (p *Politeness) RoundTripper(base http.RoundTripper) http.RoundTripper

// TransportConfig gains:
//   Politeness *Politeness // shared; nil = build a private one from the rate fields
```

**Accettazione.**

- Test con clock finto: 10 richieste allo stesso host con rate 1/s burst 4 →
  la decima parte non prima di 6 s simulati; host diversi non si bloccano.
- Test: 429 con `Retry-After: 30` → nessuna richiesta a quell'host per 30 s.
- Test: nativo e legacy sullo stesso host condividono il budget.
- Il percorso legacy raggiunge ancora `192.168.x.x` (nessun guard SSRF).
- `./scripts/qa.sh` e `./sim/run.sh` verdi.

**Non fare.** Non applicare il guard SSRF al percorso legacy; non rendere il
limiter persistente su disco.

---

## WP3 — Memoria: tornare sotto 40 MiB a riposo

**Perché.** Il servizio è passato da ~30 MiB (audit) a ~64 MiB dopo 4 minuti e
~73 MiB dopo 10 minuti, con 0 indexer nativi. Il manifesto promette < 40 MiB
con 50 indexer.

**Cosa fare.**

1. Riprodurre su una **copia** del DB di produzione, con
   `INDEXARR_PPROF=127.0.0.1:6061`, porta diversa da 8484.
2. Campionare RSS e heap a 1, 5, 15, 30, 60 minuti
   (`go tool pprof -top http://127.0.0.1:6061/debug/pprof/heap`,
   `-sample_index=inuse_space` e `alloc_space`), più `runtime.MemStats`.
3. Sospetti da verificare in ordine: cataloghi i18n caricati per lingua e
   richiesta; template della nuova UI; pool SQLite a 4 connessioni (cache per
   connessione); catalogo/definizioni ri-parsati a ogni run; cache dei
   connettori; JSON Schema incorporato; buffer di log.
4. Correggere solo ciò che i profili mostrano. Valutare `debug.SetMemoryLimit`
   da impostazione (`memory_limit_mib`, default 0 = automatico).
5. Aggiornare `docs/PERFORMANCE.md` con prima/dopo.

**Accettazione.**

- Su copia del DB, 37 sorgenti attive: RSS a riposo < 40 MiB dopo 60 minuti e
  nessuna crescita monotona su 6 ore (campioni ogni 10 minuti allegati a
  `PERFORMANCE.md`).
- Nessuna regressione nei benchmark esistenti.

**Non fare.** Non "risolvere" alzando il budget; non ridurre il pool SQLite
senza misura.

---

## WP4 — Pipeline a stadi e moduli collegati a runtime

**Perché.** `indexarr2.md` §5 promette che le funzioni nuove si innestano come
middleware o moduli. Oggi i tipi esistono ma `Registry.Invoke` e
`RegisterModule` non sono chiamati e `Module.Migrations()` non è applicato:
la promessa non è vera.

**Cosa fare.**

1. `indexer.Manager.Run` e `Manager.Search` passano dagli stadi `plan`,
   `fetch`, `normalize`, `dedup`, `store` con `Registry.Invoke`. Il comportamento
   attuale diventa l'handler terminale di ogni stadio.
2. `cmd/indexarr` costruisce la lista dei moduli abilitati
   (`internal/modules`), li registra e applica le loro migrazioni con il
   meccanismo numerato dello store (spazio di versioni per modulo).
3. Primo modulo reale per dimostrare il contratto: spostare il **blocklist**
   (oggi chiamato direttamente in `Run`) in un middleware dello stadio
   `normalize`.
4. Documentare in `INDEXARR2-PLAN.md` §3.2 "come si scrive un modulo".

**Stub.**

```go
// internal/indexer/run.go — forma, non implementazione
job := &engine.Job{Indexer: ref, Connector: connector, Search: req}
if err := m.registry.Invoke(ctx, engine.StageFetch, job, func(ctx context.Context, j *engine.Job) error {
    items, err := fetchItems(ctx, j) // today's Search/Latest call
    j.Items = items
    return err
}); err != nil { ... }
if err := m.registry.Invoke(ctx, engine.StageNormalize, job, normalizeItems); err != nil { ... }
if err := m.registry.Invoke(ctx, engine.StageStore, job, m.storeItems(indexer, stats)); err != nil { ... }

// internal/modules/modules.go
// Enabled returns the compile-time modules switched on by settings.
func Enabled(settings store.Settings) []engine.Module

// internal/store/modules.go
// ApplyModuleMigrations applies m's migrations once, recorded in
// schema_migrations under the "<module>:<version>" key.
func (s *Store) ApplyModuleMigrations(ctx context.Context, module string, migrations []engine.Migration) error

// internal/modules/blocklist/blocklist.go
type Module struct{ Store *store.Store }
func (Module) Name() string                         { return "blocklist" }
func (Module) Migrations() []engine.Migration        { return nil }
func (m Module) Register(r *engine.Registry) error  { r.Use(engine.StageNormalize, m.filter); return nil }
```

**Accettazione.**

- Test: un middleware registrato su `fetch` vede ogni run e ogni ricerca.
- Test: modulo disattivato → output Torznab byte-identico a prima.
- Test: migrazioni di un modulo applicate una volta sola, anche dopo riavvio.
- Il blocklist funziona come prima (test esistenti verdi) ma passa dal modulo.

**Non fare.** Non cambiare lo schema delle risposte Torznab; non introdurre
plugin dinamici.

---

## WP5 — `download.resolve`: magnet/torrent dalla pagina di dettaglio

**Perché.** ExtTo, TorrentGalaxy e Corsaro (32 sorgenti su 37) mostrano nel
listing solo il link al dettaglio; il magnet sta nella pagina di dettaglio. Il
formato v1 non lo esprime, quindi questi siti non possono diventare
definizioni native.

**Cosa fare.**

1. Nuova sezione `download` nel formato v1 (retro-compatibile: opzionale).
2. Il connettore da definizione, in `Download` e opzionalmente durante la
   ricerca (per avere l'infohash in cache), apre `details` e applica gli
   estrattori nell'ordine dichiarato.
3. Concorrenza limitata (`max_concurrent`, default 2) e sempre dietro al
   transport (WP2). Cache in memoria con limite delle risoluzioni recenti.
4. `resolve_on: search | grab`: con `grab` la ricerca non apre i dettagli e il
   proxy `/dl/` risolve al momento del download (meno richieste al sito).

**Stub.**

```yaml
download:
  resolve_on: search          # search | grab
  from: details               # campo da aprire
  max_concurrent: 2
  extract:
    - magnet:  { selector: 'a[href^="magnet:"]', attr: href }
    - torrent: { selector: 'a[href$=".torrent"]', attr: href, type: url }
```

```go
// internal/definition/types.go
type Download struct {
    ResolveOn     string           `yaml:"resolve_on,omitempty" json:"resolve_on,omitempty"` // search | grab
    From          string           `yaml:"from,omitempty" json:"from,omitempty"`             // field name, default "details"
    MaxConcurrent int              `yaml:"max_concurrent,omitempty" json:"max_concurrent,omitempty"`
    Extract       []DownloadTarget `yaml:"extract" json:"extract"`
}

type DownloadTarget struct {
    Magnet  *Field `yaml:"magnet,omitempty" json:"magnet,omitempty"`
    Torrent *Field `yaml:"torrent,omitempty" json:"torrent,omitempty"`
}
// Definition gains: Download *Download `yaml:"download,omitempty" json:"download,omitempty"`
```

**Accettazione.**

- Validazione: `from` deve essere un campo dichiarato; almeno un estrattore.
- Test con fixture listing + dettaglio: la ricerca restituisce magnet e
  infohash; con `resolve_on: grab` la ricerca non apre i dettagli e
  `Download` risolve il magnet.
- Test: con 20 righe e `max_concurrent: 2` non ci sono mai più di 2 richieste
  di dettaglio in volo.
- JSON Schema e `DEFINITIONS.md` aggiornati; upgrader non necessario (campo
  opzionale), `schema` resta 1.

---

## WP6 — Famiglia RSS/Atom nativa

**Perché.** Knaben e TorrentLeech sono feed RSS: senza famiglia RSS nativa non
possono lasciare il ponte. Inoltre `connectorFor` tratta `rss` sempre come
ponte, anche per un indexer nuovo.

**Cosa fare.**

1. Connettore `rss` in `internal/family/rss.go`, che riusa i parser puri di
   `internal/feed` (`rss.go`) attraverso il transport nativo.
2. Settings: `url` (segreto se contiene una chiave: tipo `password`),
   `default_category`, `query` (filtro lato client opzionale).
3. Regola ponte allineata a Torznab: `rss` è legacy **solo** se
   `LegacySourceID > 0`.
4. `legacyimport` propone le sorgenti `kind=rss` come candidati nativi se l'URL
   non è un listing HTML riconosciuto (ExtTo/TorrentGalaxy/Corsaro passano da
   WP7).

**Stub.**

```go
// internal/family/rss.go
const ConnectorRSS = "rss"

func rssFactory(cfg engine.Config) (engine.Connector, error)

// Latest fetches the feed URL; Search filters the cached feed by keywords
// because plain RSS feeds have no search endpoint.
type rssConnector struct { /* url, defaultCategory, transport, ref */ }

// internal/indexer/indexer.go — bridge rule
if (name == "rss" || name == "torznab") && indexer.LegacySourceID > 0 {
    return nil, &LegacyConnectorError{Name: name}
}
```

**Accettazione.**

- Test con fixture RSS 2.0, Atom e feed con `enclosure` `.torrent`.
- Test: un indexer `rss` nuovo (senza `LegacySourceID`) gira nel runner nativo;
  un ponte `rss` resta legacy.
- L'URL con chiave non compare in log, errori, API.

---

## WP7 — Prime definizioni native verificate

**Perché.** Il catalogo ha 0 definizioni verificate. Le sorgenti reali
dell'utente danno l'inventario prioritario: ExtTo, TorrentGalaxy, Corsaro
(HTML), Knaben e TorrentLeech (RSS, via WP6). È anche l'uscita di M1 ("i tre
parser HTML attuali sostituiti da definizioni").

**Cosa fare.**

1. Per ogni sito: catturare dal sito **reale** una pagina listing e una di
   dettaglio, ripulirle (nessun cookie, passkey, nome utente non pubblico) e
   salvarle in `internal/catalog/fixtures/<id>/`.
2. Scrivere la definizione **osservando le pagine**, non i parser legacy: i
   parser legacy servono solo come oracolo di confronto. Settings per il
   filtro (`uploader`, `category`, `language`) così che le 18+13 sorgenti
   diventino 2 definizioni con impostazioni diverse.
3. Test di equivalenza: sulla stessa fixture, il parser legacy
   (`internal/feed`) e la definizione nativa producono lo stesso insieme di
   (titolo, infohash) e categorie compatibili.
4. Stato nel catalogo: `verificata` solo con fixture reali + test verdi;
   `health.query` definita.

**Stub (forma).**

```yaml
schema: 1
meta:
  id: extto
  name: ExtTo
  type: public
  protocol: torrent
  author: indexarr
  updated: "2026-10-xx"
  urls: [https://extto.org/]
settings:
  - { name: uploader, type: text, label: "Uploader (opzionale)" }
  - { name: tag, type: text, label: "Tag lingua (es. Italian)" }
search:
  request:
    get: /browse/
    params:
      q: "{{ query.keywords }}"
      # filtri da impostazioni: vedi DEFINITIONS.md per la sintassi dei parametri condizionali
  response:
    type: html
    rows: { selector: "…osservato sulla fixture…" }
    fields:
      title:   { selector: "…" }
      details: { selector: "…", attr: href, type: url }
      size:    { selector: "…", type: size }
      seeders: { selector: "…", type: int }
    required: [title, details]
download:
  resolve_on: search
  from: details
  extract:
    - magnet: { selector: 'a[href^="magnet:"]', attr: href }
health: { query: "", min_rows: 1 }
tests:
  - name: listing
    fixture: fixtures/extto/listing.html
    expect: { min_rows: 1, required: [title, details] }
```

```go
// internal/catalog/equivalence_test.go
// TestLegacyEquivalence runs the legacy parser and the native definition on
// the same fixture and compares (title, infohash) sets.
func TestLegacyEquivalence(t *testing.T)
```

**Accettazione.**

- 5 definizioni nel catalogo in stato `verificata`, con fixture reali ripulite.
- Test di equivalenza verdi per ExtTo, TorrentGalaxy, Corsaro.
- `make def-validate` e CI verdi.

**Non fare.** Non leggere definizioni Jackett/Prowlarr di questi siti; non
committare pagine con dati personali.

---

## WP8 — Ripartenza pulita: nuovo DB, import in sola lettura da Jackett/Prowlarr

**Prerequisiti:** WP-R. **Decisione:** il DB attuale non si migra (vedi la
decisione in testa al documento). **Richiede il coordinatore presente** solo
per lo scambio del servizio su :8484 (passo 5).

**Regola assoluta su Jackett e Prowlarr: sola lettura.** Solo richieste `GET`
(l'importer Prowlarr in `internal/importers/prowlarr.go` usa già solo
`http.MethodGet`); per Jackett si legge una **copia** della directory dati
(`ServerConfig.json`, `Indexers/*.json`) presa dal coordinatore, mai la
directory viva. Nessuna API di scrittura, nessun salvataggio di configurazione,
nessun riavvio.

**Cosa fare.**

1. `internal/importers` produce oggi sorgenti ponte (`-apply` crea `sources`).
   Cambiarlo perché crei **indexer nativi**:
   - se `DefinitionHint` corrisponde a una definizione `verificata` del catalogo
     (extto, torrentgalaxy, corsaro, knaben, …) → indexer nativo da quella
     definizione, con i segreti nel vault;
   - altrimenti → indexer nativo della famiglia `torznab` che punta
     all'endpoint Torznab **per singolo indexer** di Jackett/Prowlarr
     (`/api/v2.0/indexers/<id>/results/torznab/`, `/<id>/api`), con l'API key
     nel vault: è il "ponte", ma sul runtime nativo, senza `LegacySourceID`;
   - indexer disattivati a monte → importati disattivati.
2. Stessa logica nella UI (`/import`): anteprima → "Importa" (oggi è un
   vicolo cieco, evidenza U7 di WP14).
3. MirCrew (:9118/api) e i feed RSS (Knaben, TorrentLeech): creati come
   indexer nativi `torznab`/`rss`/da catalogo, dall'utente o da un file di
   import, non da `sources`.
4. Prova su DB nuovo e porta diversa (`-listen :18484 -db <scratch>/new.db`):
   import, 24 h di funzionamento, confronto del numero di release per sito
   con quelle del servizio attuale (lettura del DB attuale in sola lettura con
   `sqlite3 -readonly`).
5. Scambio (con il coordinatore): fermare :8484, spostare `data/` in
   `data.old-<data>/` (non cancellarlo finché la nuova istanza non ha girato
   una settimana), avviare il nuovo binario con DB vuoto, rifare l'import,
   **mantenere la stessa API key Torznab** (impostarla nel nuovo DB) così i
   client non vanno riconfigurati.

**Stub.**

```go
// internal/importers/plan_native.go
// NativeTarget decides how an imported indexer is recreated: from a verified
// catalog definition, or as a torznab indexer pointing at the source manager.
type NativeTarget struct {
	DefinitionID string // "extto", … or "torznab-generic"
	ViaManager   string // "jackett" | "prowlarr" | "" (direct)
	BaseURL      string // per-indexer Torznab endpoint when ViaManager != ""
	Secrets      map[string]string // sealed by the caller, never logged
	Enabled      bool
}

func Target(ix Indexer, cat *catalog.Catalog) NativeTarget
```

**Accettazione.** Test offline con le fixture di `internal/importers/testdata`
(Jackett dir e risposta Prowlarr): nessuna riga in `sources`, nessun indexer
con `legacy_source_id`; indexer verificati creati dalla definizione di
catalogo; gli altri come `torznab` verso il manager; segreti solo nel vault;
`httptest` che fallisce il test se riceve un metodo diverso da `GET`. In
produzione: stessa API key Torznab, nessun errore nuovo in Sonarr/Radarr,
`data.old-*` conservato.

**Non fare.** Non scrivere su Jackett/Prowlarr, non copiare i loro file
Cardigann, non cancellare `data/` (si sposta).

---

## WP-R — Correzioni dalla revisione del checkpoint `4e429e5`

1. **WP5:** la risoluzione dei dettagli avviene dopo limit e dedup; con
   `resolve_on` assente vale `grab`. Test: 50 righe, limit 10 → al massimo 10
   richieste di dettaglio.
2. **Fixture senza symlink:** `internal/catalog/definitions/fixtures ->
   ../fixtures` diventa un file di testo in un checkout Windows. Directory vera
   o percorsi `../fixtures/...`.
3. **Windows:** `syscall.Kill` in `cmd/indexarr/main.go` → helper con build tag
   unix/windows; `GOOS=windows go build ./...` verde e build Windows bloccante
   in CI.
4. **Nessun URL di sorgente in chiaro** nella UI legacy (`sources.html`,
   `source_form.html`) né copiato in chiaro da backfill/legacyimport. Se WP16 è
   imminente basta la redazione nella UI: il resto sparisce con WP16.
5. ~~Rollback del vault~~ — non serve: il DB attuale è usa e getta.
6. `example-html.yaml`: nessuna fixture dichiarata che non esiste.

**Non fare:** `make build` verso `bin/` o riavvio di :8484.

---

## WP15 — Parametri condizionali nelle definizioni (v1.1)

**Perché.** ExtTo e TorrentGalaxy scelgono la sezione del sito in base alla
categoria (`c=TV`, `c=Movies`), all'uploader o al tag. v1 non lo esprime, quindi
oggi un'impostazione testuale contiene l'espressione intera: l'utente deve
sapere cosa scrivere (problema "ostico" di WP14).

**Cosa fare.** Progettare da zero (senza riprendere il templating Cardigann)
un meccanismo dichiarativo, per esempio una mappa categoria → parametri:

```yaml
search:
  paths:
    - path: /browse
      params:
        c: { from_category: true }   # valore preso dalla mappatura categories[].site
        u: { setting: uploader, omit_if_empty: true }
```

Nuovo `schema: 1` compatibile (campi opzionali) o `schema: 2` con upgrader
v1→v2; JSON Schema, validazione stretta, `indexarr-def test` con casi per ogni
ramo. Poi ExtTo e TorrentGalaxy perdono l'impostazione `filter` libera.

**Accettazione.** Una ricerca Sonarr (cat 5000) su ExtTo produce `c=TV` senza
impostazioni manuali; test di equivalenza di WP7 ancora verdi.

---

## WP16 — Rimozione del percorso legacy

**Prerequisiti:** WP8 passi 1–4 (import in indexer nativi). Il vincolo
"una settimana in produzione" è sospeso dal coordinatore (23/10): il DB è usa
e getta e il commit `b9597b4` resta come riferimento del codice legacy.

**Perché.** Con il DB usa e getta il doppio modello `sources` + `indexers`
non protegge più nulla e costa: due scheduler, due client HTTP, tab
Nativi/Legacy, 37 definizioni `legacy-*`, `legacyimport`, pagine sorgenti,
cifratura dei segreti legacy.

**Cosa fare.** Rimuovere: tabella `sources` (migrazione che la elimina),
`internal/ingest` nella parte sorgenti, connettore ponte con `LegacySourceID`,
`internal/legacyimport`, route `/sources*` (redirect a `/indexers`),
`legacy/apply|revert` in `adminapi`, tab Nativi/Legacy, chiavi i18n inutili,
`SealLegacySecrets`. Le famiglie restano l'unico modo di raggiungere un sito;
il "ponte" è un indexer `torznab` verso Jackett/Prowlarr.

**Accettazione.** `grep -rn 'LegacySourceID\|legacy-' --include=*.go` vuoto
(salvo migrazioni storiche); `scripts/ui-smoke.sh` verde; `./sim/run.sh`
verde; RSS a riposo misurata e confrontata con WP3; documentazione (UI-MAP,
ARCHITECTURE, indexarr2.md) senza il percorso legacy.

---

## WP9 — Health check e transizioni di stato del catalogo

**Cosa fare.** Job periodico (default ogni 6 h, con jitter) che esegue
`health.query` per ogni indexer nativo abilitato, registra l'esito in
`indexer_history` (`kind = health`) e aggiorna lo stato della definizione:
`verificata → stabile` dopo N successi, `stabile → degradata` dopo M fallimenti,
`degradata → verificata` dopo un successo. Notifica sui passaggi a `degradata`.

**Stub.**

```go
// internal/health/health.go
type Checker struct { Store *store.Store; Manager *indexer.Manager; Clock engine.Clock }
func (c *Checker) RunOnce(ctx context.Context) ([]Result, error)
func (c *Checker) Start(ctx context.Context)

// pure transition function, fully table-tested
func NextState(current string, recent []Outcome, thresholds Thresholds) string
```

**Accettazione.** Test a tabella di `NextState`; test end-to-end con
`simmock`; stato visibile in UI e `/api/v2/definitions`.

---

## WP10 — Refresh periodico dei seeders

**Cosa fare.** Job che usa `store.StaleProvenance(olderThan, limit)` per
riaggiornare seeders/leechers delle release restituite di recente alle \*arr,
rispettando limiti query e rate per host; TTL configurabile (default 6 h).

**Stub.**

```go
// internal/provenance/refresh.go
func (r *Refresher) RunOnce(ctx context.Context) (refreshed int, err error)
```

**Accettazione.** Test con clock finto; nessun superamento di `query_limit`;
i seeders esposti da Torznab sono quelli aggiornati.

---

## WP11 — FlareSolverr per indexer nativi e stato `blocked`

**Cosa fare.** Interfaccia `ChallengeSolver` nel transport; implementazione
FlareSolverr (riusa `internal/flaresolverr`); setting per indexer
`challenge: none | flaresolverr`. Rilevamento challenge (403/503 + marker noti)
→ stato `blocked` con messaggio "attiva FlareSolverr" invece di errore
generico. Nessuna risoluzione di CAPTCHA.

**Stub.**

```go
// internal/engine/challenge.go
type ChallengeSolver interface {
    Solve(ctx context.Context, req *http.Request) (*Response, error)
}
// TransportConfig gains: Solver ChallengeSolver
```

**Accettazione.** Test con server che simula la challenge; con solver →
risposta; senza → errore tipizzato `ErrChallenge` e stato `blocked`.

---

## WP12 — CI multipiattaforma e budget di risorse

**Cosa fare.**

1. Matrice di build (`CGO_ENABLED=0`): linux amd64/arm64/arm(v7,v6)/386/riscv64,
   windows amd64/arm64, darwin amd64/arm64, freebsd amd64/arm64.
2. Test su `ubuntu-latest`, `windows-latest`, `macos-latest`.
3. `scripts/budget.sh`: fallisce se il binario linux/amd64 ottimizzato supera
   25 MB o se l'RSS dopo 60 s di avvio su un DB seed (`internal/perf`) supera
   il budget (40 MiB, da abbassare quando WP3 è chiuso).
4. `go test -race ./...` in CI.

**Stub.**

```yaml
# .github/workflows/qa.yml — job aggiuntivi
  build-matrix:
    strategy:
      matrix:
        target: [linux/amd64, linux/arm64, linux/arm/7, linux/arm/6, linux/386, linux/riscv64,
                 windows/amd64, windows/arm64, darwin/amd64, darwin/arm64, freebsd/amd64, freebsd/arm64]
  budget:
    runs-on: ubuntu-latest
    steps: [checkout, setup-go, "run: ./scripts/budget.sh"]
```

**Accettazione.** Tutti i target compilano; il budget fallisce davvero se si
aumenta artificialmente la dimensione (prova documentata nel PR).

---

## WP13 — Parità residua

Da fare dopo il gruppo C, ognuno come WP separato quando si arriva lì:

- **Import applicato** (UI/API): dal piano Prowlarr/Jackett crea indexer nativi
  quando esiste la definizione o la famiglia, altrimenti indexer ponte. Usa
  **solo la configurazione** (siti, credenziali, tag, proxy, applicazioni),
  mai i file YAML delle definizioni.
- **App sync**: snapshot e rollback in un clic, filtri per tag, Mylar3 e
  LazyLibrarian (oggi `ErrUnsupported`).
- **Notifiche** per health, grab, aggiornamenti catalogo.
- **i18n** delle pagine legacy (Release, Rules, Settings, Maintenance, form
  sorgenti) nelle 7 lingue.
- **UI mobile**: paginazione liste lunghe, touch target.

---

## WP14 — Interfaccia: da pannello tecnico a prodotto semplice

> **Specifica di riferimento: [UI-GUIDE.md](UI-GUIDE.md)** (23/10, dopo il
> checkpoint `4e429e5`): regole, struttura a 6 voci, pagina per pagina,
> componenti e fasi 1b–5 allineate a WP16. Questa sezione resta la diagnosi
> originale; in caso di conflitto vale UI-GUIDE.

Analisi fatta il 23/10/2026 leggendo le pagine servite dall'istanza reale
(`:8484`, build `384e541`, sola lettura). Le revisioni precedenti
(`UI-REVIEW*.md`) hanno verificato accessibilità, layout e link: sono a posto.
Il problema è un altro: **l'interfaccia è ostica perché mostra l'architettura
interna invece dei compiti dell'utente.**

### Diagnosi

Per aggiungere un tracker oggi l'utente deve capire sei concetti interni:
*definizione*, *indexer nativo*, *sorgente legacy*, *ponte*, *catalogo*,
*famiglia/connettore*. Prowlarr ne espone uno solo: "aggiungi indexer, cerca il
sito, inserisci le credenziali". Il manifesto promette "prima ricerca in meno
di due minuti, senza leggere documentazione": oggi non è raggiungibile.

### Evidenze

| # | Dove | Cosa si vede | Perché è un problema |
|---|---|---|---|
| U1 | Sidebar | 10 voci: Overview, Releases, Indexers, Definitions, Applications, Import, Activity, Rules, Settings, Maintenance | "Definitions" e "Import" sono strumenti, non destinazioni quotidiane; la nav rispecchia i package |
| U2 | `/indexers` | Due tab "Native 0" e "Legacy bridge 37"; testo che spiega definizioni, connettori e ponte | L'utente vede due mondi; il 100% dei suoi siti è sotto "legacy", la tab di default è vuota |
| U3 | `/indexers/new` | Il menu "Definition" elenca **37 voci `legacy-*`** (es. "extto.org (7) · legacy-10") insieme alle 7 generiche; il menu compare **due volte** (carica campi + definizione) | Contraddice `/definitions` ("le definizioni legacy sono in sola lettura"); scegliere `legacy-10` per un nuovo indexer non ha senso |
| U4 | `/indexers/new` | Campi subito visibili: Protocol, Slug, Priority, Tags, User agent, Allow private addresses, Categories come ID numerici | Protocollo e slug sono derivabili; categorie come numeri Torznab; SSRF esposto come checkbox |
| U5 | `/indexers/new` | Si parte da una definizione, non da un sito; non c'è ricerca né "incolla URL" | Il gesto naturale è "cerco il nome del sito" |
| U6 | `/definitions` | Colonne Hash, Origin, Family/connector; stato "draft" su tutto | Dettagli da sviluppatore; "draft" ovunque comunica "non usare" |
| U7 | `/import` | Produce solo un'anteprima: "Applying the plan will arrive…" | Vicolo cieco: chi arriva da Prowlarr non può completare la migrazione |
| U8 | Dashboard | "Errors (24h) **169**" accanto a "no errors" e "all without errors" | Messaggi contraddittori: l'utente non si fida dei numeri |
| U9 | `/indexers?tab=legacy` | 37 righe "extto.org (2)…(19)", "torrentgalaxy.one (2)…(13)"; avviso in italiano dentro pagina inglese | Lo stesso sito ripetuto 19 volte perché ogni filtro è una sorgente; nomi non distinguibili |
| U10 | `/settings` | 35+ campi in un'unica pagina, titoli misti italiano/inglese; un solo client di download anche se ne esistono 6 | Impostazioni tecniche (worker, timeout, volume factor) allo stesso livello di quelle essenziali |
| U11 | Generale | Dopo un'azione non c'è un "passo successivo" (es. dopo aver creato un indexer: "collegalo a Sonarr") | Ogni flusso finisce in una tabella |
| U12 | Generale | L'endpoint Torznab mostrato è `http://127.0.0.1:8484/api` | Inutile per Sonarr su un'altra macchina; `public_url` non viene chiesto |

### Principi del redesign

1. **Un solo concetto per l'utente: l'indexer (= un sito).** Come ci si
   collega (definizione nativa, famiglia, ponte Jackett/Prowlarr) è un
   dettaglio mostrato come badge, non una sezione.
2. **Si parte dal sito, non dal formato.** Ricerca per nome, o "incolla l'URL";
   il sistema sceglie definizione/famiglia.
3. **Mostrare solo ciò che serve, il resto in "Avanzate".**
4. **Ogni flusso finisce con il passo successivo.**
5. **Numeri coerenti**: uno stato, una fonte.
6. **Lingua coerente**: nessuna stringa fuori catalogo i18n.

### Architettura dell'informazione proposta

| Voce | Contenuto | Sostituisce |
|---|---|---|
| **Home** | Stato in 3 righe (tutto ok / N problemi con link), endpoint da copiare, "prossimo passo" consigliato | Overview |
| **Cerca** | Ricerca release (la pagina più usata) | Releases |
| **Indexer** | Elenco unico di tutti i siti; badge "Nativo", "Via Jackett", "Via Prowlarr", "Feed"; filtro per stato | Indexers (2 tab) + sorgenti legacy |
| **App** | Sonarr/Radarr/… + client di download | Applications + client in Settings |
| **Attività** | Run, log, cronologia ricerche e grab | Activity |
| **Impostazioni** | Generale, Sicurezza (API key, vault), Rete (FlareSolverr, proxy, rate limit), Backup, Avanzate; Regole come sottosezione | Settings, Rules, Maintenance |
| *(Strumenti, in Impostazioni → Avanzate)* | Definizioni, editor YAML, import da Prowlarr/Jackett, manutenzione DB | Definitions, Import, Maintenance |

Da 10 voci a 6. Definizioni e import restano raggiungibili, ma non sono la
prima cosa che un utente nuovo vede.

### Flussi riprogettati

**F1 — Aggiungi indexer (obiettivo: < 60 secondi)**

```text
[Aggiungi indexer]
  ┌───────────────────────────────────────────────┐
  │ Cerca un sito o incolla un URL  [ extto      ]│
  │  ExtTo · pubblico · nativo ✓                   │
  │  TorrentGalaxy · pubblico · nativo ✓           │
  │  UNIT3D (qualsiasi sito UNIT3D) · privato      │
  │  Non c'è? → Collega tramite Jackett/Prowlarr   │
  └───────────────────────────────────────────────┘
        ↓ scelta
  Solo i campi obbligatori della definizione (es. uploader, lingua;
  oppure username/password/API key)          [Avanzate ▸]
        ↓ [Salva e prova]
  Test per fasi: raggiungibile ✓ → login ✓ → ricerca ✓ (12 risultati)
        ↓
  "Fatto. Prossimo passo: collega Sonarr/Radarr" [Collega app]
```

- Protocollo, slug, priorità, tag, user agent, categorie, accesso a indirizzi
  privati: in **Avanzate**, con valori derivati.
- Le definizioni `legacy-*` non compaiono mai nella scelta.
- Categorie come nomi ("Film HD", "Serie TV"), non come ID.

**F2 — Più filtri sullo stesso sito.** Un indexer ExtTo con più "feed
salvati" (uploader/tag) al suo interno, invece di 19 indexer "extto.org (n)".
Dipende da WP7 (definizione con settings di filtro).

**F3 — Primo avvio (wizard).** 1) URL pubblico per le \*arr (precompilato e
verificato); 2) "Hai Prowlarr o Jackett?" → importa **e applica**; 3) aggiungi
indexer (F1); 4) collega app. Saltabile, riapribile da Home.

**F4 — Import applicato.** Anteprima → selezione → applica → riepilogo con
"nativo" o "via ponte" per ogni sito e possibilità di annullare. Dipende da
WP13 (import applicato).

**F5 — Problemi.** Ogni errore mostra causa in parole semplici + azione
("Il sito risponde 429: troppe richieste. [Aumenta intervallo] [Unisci
filtri]"). La Home elenca solo ciò che richiede un'azione.

### Terminologia (da applicare in tutte le lingue)

| Oggi | Domani | Note |
|---|---|---|
| Native indexer / Legacy source / Legacy bridge | **Indexer** + badge "Via Jackett", "Via Prowlarr", "Feed" | Il termine "legacy" sparisce dalla UI |
| Definition | **Definizione** solo in Avanzate; altrove "supporto del sito" | |
| draft / verified / stable / degraded | "In prova" / "Verificato" / "Stabile" / "Problemi" | |
| Slug | "Nome nell'URL" (in Avanzate) | |
| Run | "Aggiorna ora" | |
| Allow private addresses | "Il sito è nella mia rete locale" (in Avanzate) | |

### Cosa fare

1. **Correzioni immediate (P0, piccole):**
   - escludere `legacy-*` dai menu di `/indexers/new` e togliere il menu
     duplicato (U3);
   - una sola fonte per gli errori della dashboard (U8);
   - chiedere/mostrare `public_url` dove si mostra l'endpoint (U12);
   - stringhe fuori catalogo i18n nelle pagine nuove (U9).
2. **Indexer unificato (P1):** una lista sola con badge (U2), F1 con ricerca
   del sito e campi avanzati nascosti (U4, U5).
3. **Navigazione a 6 voci (P1)** e Impostazioni a sezioni (U1, U10).
4. **Flussi guidati (P2):** wizard F3, "prossimo passo" (U11), F5.
5. **Dopo WP7/WP13:** F2 (filtri dentro un indexer), F4 (import applicato).

### Stub

```go
// internal/web/indexers_view.go
// IndexerRow is the single list model: native and bridged indexers look the
// same; Via tells the user how indexarr reaches the site.
type IndexerRow struct {
    ID      int64
    Name    string
    Site    string // host shown to the user
    Via     string // "native" | "jackett" | "prowlarr" | "torznab" | "feed"
    Status  string // "ok" | "problems" | "disabled" | "testing"
    Problem string // one human sentence, empty when ok
    Action  string // suggested fix route, empty when ok
    LastRun time.Time
}

func (s *Server) indexerRows(ctx context.Context) ([]IndexerRow, error) // merges indexers + legacy sources

// internal/web/catalog_search.go
// SiteChoice is one result of the "search a site or paste a URL" box.
type SiteChoice struct {
    DefinitionID string
    Name         string
    Kind         string // "site" | "family" | "bridge"
    Public       bool
    State        string // user-facing: "Verificato", "In prova", …
}

// GET /indexers/new/search?q=extto  → []SiteChoice (JSON or HTML fragment)
// GET /indexers/new?definition=<id> → form with required settings only;
// advanced fields in a <details> block.
func (s *Server) searchSites(w http.ResponseWriter, r *http.Request)
```

### Accettazione (test di usabilità, non solo tecnici)

Cinque compiti, eseguiti da una persona che non ha mai visto indexarr,
cronometrati e annotati in `docs/UI-USABILITY.md`:

| Compito | Obiettivo |
|---|---|
| Trovare l'endpoint da dare a Sonarr | < 15 s, senza errori |
| Aggiungere un indexer pubblico (ExtTo) | < 60 s |
| Aggiungere un tracker UNIT3D con API key | < 90 s |
| Capire perché un indexer non funziona e correggerlo | < 2 min |
| Collegare Sonarr | < 2 min |

Più i controlli automatici: `scripts/ui-smoke.sh` verde; nessuna stringa
fuori catalogo i18n (test che cerca testo letterale nei template); nessuna voce
`legacy-*` nei form di creazione (test); dashboard senza contraddizioni (test
sul modello).

**Non fare.** Non nascondere informazioni diagnostiche: spostarle in
"Avanzate" o nel dettaglio. Non rompere le URL esistenti (redirect). Non
introdurre un framework JS: la UI resta SSR con poco JS progressivo.

---

## 4. Igiene della documentazione

- `indexarr2.md` = visione + checklist §3 con colonna di stato. È l'unica fonte
  dello stato di parità.
- `docs/INDEXARR2-PLAN.md` = contratti e diario tecnico.
- `docs/NEXT-STEPS.md` (questo file) = lavoro da fare, un WP per volta.
- Numeri (asserzioni, link, RSS) si scrivono una volta sola, con data e
  comando che li produce.
