# Prestazioni e risorse di indexarr

Documento di misura della passata "performance e resource optimization" su
indexarr 2. Le modifiche riguardano solo `internal/store/**` e
`internal/torznabapi/**`; il generatore di dataset e la suite di misura vivono
in `internal/perf/**` e non vengono linkati dal binario di produzione.

Ultimo aggiornamento: **25 ottobre 2026**.

---

## 1. Obiettivi

Questi sono i limiti che il prodotto deve rispettare. Non sono numeri misurati:
sono la soglia oltre la quale una modifica viene considerata una regressione.

| Obiettivo | Soglia |
|---|---|
| Binario statico (`CGO_ENABLED=0`, `-trimpath -s -w`) | < 20 MB |
| RSS a riposo | avviso sopra 64 MiB, errore sopra 128 MiB (decisione del coordinatore, 23/10) |
| Latenza Torznab p95 con 50.000 release | < 50 ms |
| Goroutine e file descriptor dopo ricerche ripetute | nessuna crescita illimitata |

La latenza p95 è intesa su ricerche Torznab realistiche (`q` con un titolo,
eventualmente `cat`). La query di solo qualità (`q=1080p`), che intercetta
circa il 43% del dataset, è misurata separatamente come limite superiore.

---

## 2. Metodologia

### Macchina di misura

| Voce | Valore |
|---|---|
| CPU | Intel N97, 4 core |
| RAM | 15,7 GiB |
| Sistema | Linux 7.1.13+deb13-amd64 |
| Go | 1.27.2 |
| Disco | SSD locale |

I numeri sono indicativi e rumorosi (±10–20% tra esecuzioni): questa macchina
non è un server dedicato e non ha frequency scaling bloccato. Le conclusioni
vanno lette sulle differenze grandi, non sui decimali.

### Dataset

`internal/perf.Seed` crea un database disposable con release sintetiche ma
verosimili:

- titoli di film, serie e documentari con qualità (`1080p`, `2160p`, `720p`,
  `BluRay`, `WEB-DL`, `HDTV`) e lingue;
- 10 sorgenti legacy, 9 categorie Torznab (con gruppi `2xxx`, `5xxx`, …);
- seeders/leechers, dimensioni, date decrescenti e magnet;
- il 5% delle righe ha un piccolo payload `.torrent`;
- 3 indexer nativi e 1–3 righe di provenance per release;
- `ANALYZE` al termine, così i piani di query sono quelli reali.

L'inserimento usa SQL diretto in un'unica transazione con statement preparato:
è veloce e soprattutto **esercita trigger FTS, vincoli e indici reali**. Non
usa `UpsertRelease` perché 50.000 upsert misurerebbero il generatore, non lo
schema.

### Comandi

```bash
# Benchmark in-process (store)
go test ./internal/store -run '^$' \
  -bench 'BenchmarkUpsertRelease|BenchmarkSearchReleases|BenchmarkReleaseProvenance' \
  -benchmem -benchtime=200x

# Benchmark in-process (Torznab, httptest.NewRecorder)
go test ./internal/torznabapi -run '^$' \
  -bench BenchmarkTorznabSearch -benchmem -benchtime=300x

# Harness end-to-end: binario, startup, RSS, FD, latenza con curl
./scripts/bench.sh

# Test di non-regressione su goroutine e file descriptor
go test ./internal/perf -run TestTorznabSearchDoesNotLeak -v

# Profili (esempio)
go test ./internal/store -run '^$' -bench 'BenchmarkSearchReleases/50k/text$' \
  -benchtime=300x -cpuprofile /tmp/cpu.out -memprofile /tmp/mem.out
go tool pprof -top /tmp/cpu.out
go tool pprof -top -alloc_space /tmp/mem.out
```

`scripts/bench.sh` non richiede strumenti esterni: usa solo `curl`, `ps`,
`sort`, `awk` e i binari Go. Per ogni dataset crea un DB temporaneo, avvia
un'istanza nuova, attende `/healthz`, misura startup e RSS, poi esegue 100
richieste sequenziali e 20 concorrenti per scenario.

---

## 3. Misure end-to-end

Esecuzione del 23 ottobre 2026, `./scripts/bench.sh`, build di release.

| Dataset | Scenario | startup ms | RSS MiB | FD | p50 ms | p95 ms | max ms |
|---|---|---|---|---|---|---|---|
| 10k | empty | 42 | 36,5 | 14 | 4,5 | 6,0 | 7,9 |
| 10k | titolo | – | – | – | 13,4 | 15,7 | 32,4 |
| 10k | titolo + cat | – | – | – | 10,4 | 13,6 | 21,9 |
| 10k | stress `q=1080p` | – | – | – | 8,3 | 11,0 | 13,9 |
| 10k | 20× titolo | – | – | – | 124,2 | 137,9 | 152,7 |
| 10k | 20× stress | – | – | – | 45,2 | 58,7 | 63,2 |
| 10k | dopo il carico | – | 39,6 | 16 | – | – | – |
| 50k | empty | 61 | 32,4 | 14 | 8,1 | 10,9 | 12,2 |
| 50k | titolo | – | – | – | 18,5 | 20,2 | 21,3 |
| 50k | titolo + cat | – | – | – | 14,4 | 17,2 | 18,7 |
| 50k | stress `q=1080p` | – | – | – | 18,9 | 22,7 | 26,0 |
| 50k | 20× titolo | – | – | – | 131,0 | 147,9 | 153,8 |
| 50k | 20× stress | – | – | – | 110,8 | 134,4 | 145,0 |
| 50k | dopo il carico | – | 51,0 | 16 | – | – | – |

Riepilogo rispetto agli obiettivi:

| Obiettivo | Misurato | Esito |
|---|---|---|
| Binario < 20 MB | **17,3 MiB** (18.112.672 byte) | ✅ |
| RSS a riposo (avviso 64 MiB, errore 128 MiB) | **32,4–36,5 MiB** (51,0 MiB dopo il carico a 50k) | ✅ (WP3 §9: 26,3 MiB a 60 s e 34,7 MiB a 300 s con 15 indexer) |
| p95 Torznab < 50 ms a 50k | **20,2 ms** titolo · **17,2 ms** titolo+cat · **10,9 ms** empty · **22,7 ms** stress | ✅ |
| Nessuna crescita di goroutine/FD | 3 → 3 goroutine, 10 → 10 FD su 1.000 ricerche | ✅ |

Nota sulla concorrenza: le 20 richieste partono insieme. Con 50k release il
p95 è ~148 ms perché il pool SQLite ha 4 connessioni e le richieste si
serializzano in ondate. Non è una soglia richiesta dal prodotto, ma è il
numero da tenere presente prima di alzare `MaxOpenConns` (vedi §7).

---

## 4. Benchmark in-process: prima e dopo

Le misure "prima" sono state prese sullo stesso codice di partenza
(`8fa3c57`), con lo stesso seed e `-benchmem`. Dopo le ottimizzazioni:

| Benchmark | Prima | Dopo | Δ |
|---|---|---|---|
| `UpsertRelease/new` | 357 µs | 362 µs | ~0 |
| `UpsertRelease/update` | 279 µs | 286 µs | ~0 |
| `SearchReleases/10k/text` | 14,2 ms | 4,1 ms | **−71%** |
| `SearchReleases/50k/empty` | 4,18 ms | 3,79 ms | −9% |
| `SearchReleases/50k/text` | 66,8 ms | 13,8 ms | **−79%** |
| `SearchReleases/50k/filters` (source+cat+seeders) | 29,6 ms | 15,3 ms | **−48%** |
| `SearchReleases/50k/text+category` | – (non misurato) | 16,8 ms | – |
| `SearchReleases/50k/text+MinSize` | 71,1 ms | 60,3 ms | −15% |
| `SearchReleases/50k` query corta (`4k`, LIKE) | 54,6 ms | 52,1 ms | −5% |
| `ReleaseProvenance` (100 id) | 739 µs | 763 µs | entro il rumore |
| `TorznabSearch/items_100` | 3,11 ms · 1,21 MB · 11.014 alloc | 2,09 ms · 0,64 MB · 5.156 alloc | **−33% tempo, −47% memoria** |
| `TorznabSearch/items_100_text` | 4,63 ms · 553 KB · 5.096 alloc | 3,49 ms · 289 KB · 2.590 alloc | −25% tempo, −48% memoria |

I benchmark `search` includono sempre il `COUNT(*)` e il caricamento della
pagina: è il costo che vede il client Torznab.

### Profilo: dove andava il tempo

Prima delle modifiche, `pprof -top` sulla ricerca testuale a 50k mostrava:

- `_sqlite3VdbeExec`, `_sqlite3BtreeInsert`, `_balance_nonroot`: ~35% del CPU.
  Era la **costruzione dell'indice effimero** con i 21.000 rowid FTS della
  sottoquery `id IN (SELECT rowid FROM releases_fts …)`, ripetuta due volte
  (conteggio e pagina).
- `_fts5MultiIterNext`, `_fts5ExprNodeTest_STRING`: ~10%, la scansione FTS.
- `bytes.growSlice`, `fmt.Fprintf`, `bytes.Buffer.WriteString`: la generazione
  XML, con una `bytes.Buffer` nuova per ogni attributo in `esc()`.
- `_whereLoopAddBtree`, `_yy_reduce`: pianificazione SQL per richiesta.

Dopo le modifiche il costo dominante è la scansione FTS stessa e la
preparazione delle query; non c'è più la doppia materializzazione.

---

## 5. Ottimizzazioni applicate

### 5.1 Indici compositi e categorie come range

In `internal/store/store.go`:

- `releases_visible_published_id` → `(hidden, published_at DESC, id DESC)`
- `releases_visible_seen_id` → `(hidden, last_seen_at DESC, id DESC)`
- `releases_visible_source_published` → `(hidden, source_id, published_at DESC, id DESC)`
- `releases_visible_category_published` → `(hidden, category, published_at DESC, id DESC)`

I vecchi `releases_visible_published` e `releases_visible_seen` vengono
eliminati: erano prefissi dei nuovi e non coprivano il tie-break `id DESC`,
quindi SQLite doveva comunque ordinare. Con `id` nell'indice, `LIMIT` si ferma
dopo la pagina.

In `releases.go` la categoria non usa più `LIKE '2%'` (che non può usare
l'indice perché `LIKE` è case-insensitive per default) ma:

- categoria foglia (`2040`) → `category = ?`;
- categoria di gruppo (`2000`) → `category >= '2' AND category < '3'`.

Il risultato è identico per le categorie numeriche Torznab, ma il predicato è
index-friendly. I test esistenti su gruppi e categorie esatte restano verdi.

### 5.2 Termini FTS combinati

Più termini di ricerca vengono combinati in un'unica espressione
`MATCH '"term1" AND "term2"'` invece di una sottoquery per termine. Meno
scansioni dell'indice FTS sulle query multi-parola.

### 5.3 Percorso veloce per le query testuali non selettive

Il caso peggiore era una query come `1080p`: 21.000 match su 50.000 release.
SQLite materializzava i rowid in un indice effimero, due volte. Il nuovo
percorso in `searchTextFast`:

1. legge i rowid FTS una volta sola (`group_concat`, un solo passaggio dal
   driver);
2. conteggia l'intersezione con il predicato non-testuale (visibilità,
   categoria, sorgente, ecc.) in Go, con ricerca binaria;
3. legge solo un **prefisso** della tabella nell'ordine richiesto, forzando
   l'indice giusto (`INDEXED BY`), e interseca.

Il percorso si attiva solo quando:

- ci sono almeno 2.000 match FTS (sotto quella soglia la query normale è già
  veloce);
- i match sono al massimo 200.000 (limite di memoria per richiesta, ~1,6 MB);
- l'ordinamento è `recent`/`seen`, coperto da un indice;
- non ci sono `id` esterni (`imdbid`/`tmdbid`/`tvdbid`) che cambierebbero la
  forma del predicato;
- `IncludeHidden` è falso (l'indice di visibilità non può ordinare senza il
  vincolo su `hidden`).

Se una condizione non vale, si usa il percorso SQL precedente: il
comportamento è sempre lo stesso, cambia solo il costo. Il test
`TestSearchTextFastPathMatchesReference` confronta il risultato con una
scansione bruta per pagine, offset, ordinamento `seen`, categorie, esclusioni,
dimensione minima, sorgente e hidden.

Per le query testuali la pagina viene caricata in due fasi (prima gli `id`,
poi le righe complete): i blob `.torrent` non entrano nel sort.

### 5.4 XML Torznab

In `internal/torznabapi/server.go`:

- `bytes.Buffer` pre-dimensionata (`2048 + 1400 × item`) invece di crescere
  geometricamente;
- `writeEsc` scrive direttamente nel buffer e replica byte per byte
  `xml.EscapeText` (inclusi `\t`, `\n`, `\r` e UTF-8 non valido). Prima ogni
  attributo allocava una `bytes.Buffer` e una `[]byte`;
- niente `fmt.Fprintf` nel percorso caldo: `writeInt`, `writeTag`,
  `writeAttr`, `writeAttrInt` e `strconv.AppendInt`;
- `url.QueryEscape(apiKey)` calcolato una volta per pagina, non per item;
- `settings` passato per puntatore a `writeItem`.

L'output è identico: `TestWriteEscMatchesStandardLibrary` lo verifica contro
`encoding/xml`.

### 5.5 Pool di connessioni SQLite

`Store.Open` usa `MaxOpenConns(4)` e `MaxIdleConns(4)` invece di una sola
connessione. SQLite in WAL permette più lettori e un solo scrittore:
`busy_timeout=10000` assorbe la contesa. Ogni connessione ha la sua page cache
da 4 MiB, quindi il caso peggiore è ~16 MiB di cache.

Effetto misurato a 50k, 20 richieste concorrenti:

| Scenario | 1 connessione | 4 connessioni |
|---|---|---|
| 20× titolo p95 | 322 ms | 148 ms |
| 20× stress p95 | 694 ms | 134 ms |

Il secondo numero include anche il percorso veloce testuale. A riposo gli FD
passano da ~10 a ~14 e restano stabili.

### 5.6 Cosa **non** è stato cambiato

- `PRAGMA mmap_size` resta disattivato: aumenterebbe l'RSS in modo poco
  prevedibile sui dispositivi piccoli.
- `cache_size` resta a 4 MiB per connessione: alzarlo aiuta solo i DB molto
  più grandi della cache e peggiora il footprint.
- `synchronous` resta `NORMAL`: `OFF` non è sicuro per un servizio sempre
  acceso.
- Nessuna cache delle impostazioni: `LoadSettings` costa una query e ~9 KB per
  richiesta; una cache richiederebbe un'invalidazione corretta e non è
  necessaria per rispettare le soglie.

---

## 6. Risorse e leak

### Test automatico

`internal/perf/perf_test.go::TestTorznabSearchDoesNotLeak` esegue 1.000
ricerche Torznab su un DB popolato (tre forme: search con testo, search vuota,
tvsearch), poi confronta `runtime.NumGoroutine()` e il numero di entry in
`/proc/self/fd` con il valore iniziale.

Risultato su questa macchina:

```
goroutines 3 -> 3, file descriptors 10 -> 10
```

Le soglie del test sono volutamente larghe (+8 goroutine, +8 FD): servono a
intercettare una crescita illimitata, non a fissare il runtime.

### Runner nativi e sync a riposo

Verifica del codice richiesta dalla milestone:

- `internal/native.Runner` usa `time.NewTicker(interval)` con
  `interval = settings.DefaultInterval()`; il default è **3600 s** e il minimo
  è 60 s. Non c'è alcun tick a 30 s.
- `internal/syncjob.Service` usa un ticker da **15 minuti** (minimo 1 minuto) e
  un ritardo iniziale di 2 s. Non c'è alcun tick a 30 s.

A riposo, con un DB senza indexer nativi da eseguire o applicazioni da
sincronizzare, i risvegli sono quindi 1/ora (native) e 4/ora (sync), non
2/minuto. Il `runPass` nativo esegue comunque una `ListIndexers` a ogni tick:
è una query su una tabella piccola e non giustifica una sveglia più frequente.

---

## 7. Punti caldi rimanenti e raccomandazioni

Queste aree non sono state cambiate perché la modifica sarebbe rischiosa o
poco utile rispetto al guadagno. Sono candidati per una passata futura.

1. **Termini di 1–2 caratteri (`4k`, `tv`)** non possono usare il trigram FTS:
   la query fa un `LIKE '%…%'` e resta ~52 ms a 50k. Un indice a 2-gram
   separato risolverebbe, ma raddoppierebbe l'indice e le scritture. Nel
   frattempo conviene documentare ai client di usare almeno 3 caratteri.
2. **Filtri numerici non indicizzati** (`MinSize`/`MaxSize`) su query testuali
   larghe: ~60 ms a 50k. Il percorso veloce deve leggere tutti gli id del
   filtro per calcolare il totale. Un indice su `size` aiuterebbe solo i range
   molto selettivi; con un range largo il costo resta. Da valutare con dati
   reali.
3. **Pianificazione SQL per richiesta**: `_whereLoopAddBtree` + `_yy_reduce`
   pesano ~15–20% del tempo Torznab. Una cache di statement preparati (LRU,
   limite esplicito) potrebbe togliere 0,2–0,4 ms per richiesta, ma va
   progettata con attenzione alla vita degli statement e al numero di forme
   SQL (che dipende dai filtri). Non applicata.
4. **Upsert ~0,36 ms/riga**: ogni item è una transazione implicita con trigger
   FTS e aggiornamento di tutti gli indici della tabella. Per l'ingestion attuale (max 100 item per
   run) va bene. Un batch di N upsert in una transazione sarebbe 5–10× più
   veloce, ma cambierebbe la semantica di `times_seen`/`last_seen_at` e la
   gestione degli errori per singolo item: da fare solo con un requisito di
   throughput esplicito.
5. **20 richieste concorrenti**: p95 ~148 ms con 4 connessioni. Portare il pool
   a 8 dimezzerebbe la coda ma aggiungerebbe ~16 MiB di cache e ~12 FD,
   avvicinandosi alla soglia di 60 MB dopo il carico. Misurare sul dispositivo
   target prima di alzarlo.
6. **Provenance**: una query per pagina (non N+1), ~0,76 ms per 100 release.
   Non è un collo di bottiglia rispetto a ricerca e XML. Se crescesse, l'indice
   `release_sources(release_id, indexer_id)` è già quello giusto; si può
   evitare il `JOIN` con `indexers` denormalizzando il nome, ma non ora.
7. **`LoadSettings` per richiesta**: una query e ~9 KB di allocazioni. Una
   cache invalidata da `SaveSettings` sarebbe sicura in-process, ma il guadagno
   è sotto il rumore rispetto ai 10–20 ms della ricerca. Non applicata.

---

## 8. File toccati

- `internal/store/store.go` — indici compositi, pool di connessioni.
- `internal/store/releases.go` — categorie come range, match FTS combinato,
  percorso veloce testuale, pagina in due fasi.
- `internal/store/store_test.go` — aggiornato l'elenco degli indici da
  eliminare nella simulazione di un DB vecchio.
- `internal/store/bench_test.go` — nuovi benchmark.
- `internal/store/search_fast_test.go` — confronto con la scansione di
  riferimento.
- `internal/torznabapi/server.go` — generazione XML senza `fmt`/buffer
  temporanei.
- `internal/torznabapi/bench_test.go`, `esc_test.go` — benchmark e test
  dell'escaper.
- `internal/perf/seed.go`, `internal/perf/cmd/benchseed/main.go` — generatore
  di dataset.
- `internal/perf/perf_test.go` — test goroutine/FD.
- `scripts/bench.sh` — harness end-to-end.
- `docs/PERFORMANCE.md` — questo documento.

---

## 9. WP3 — memoria a riposo (23 ottobre 2026)

Questa sezione misura il ritorno sotto il vecchio budget del manifesto (**RSS <
40 MiB a riposo**), oggi sostituito dalla soglia con avviso a 64 MiB ed errore a
128 MiB. Le misure di §3 sono precedenti alle fasi UI e ai template i18n:
servono come riferimento storico, non come baseline per il budget.

### 9.1 Metodologia

| Voce | Valore |
|---|---|
| Commit "prima" | `cc49ae5` (legacy rimosso, UI fase 2) |
| Binario | `CGO_ENABLED=0 go build -trimpath -ldflags '-s -w'` |
| Database | `benchseed -n 10000`, più 5 indexer sintetici → **15 indexer** |
| Impostazioni | `default_interval_seconds=60`, `backup_enabled=1`, `backup_interval_hours=6` |
| Porta HTTP / pprof | `127.0.0.1:18520` / `127.0.0.1:6062` |
| Campioni | RSS da `ps -o rss=` e `/proc/<pid>/status`; heap `inuse_space` con `pprof?gc=1` |
| Macchina | Intel N97, 4 core, 15,7 GiB, macchina condivisa (±10–20% tra esecuzioni) |

L'istanza esegue un ciclo dello scheduler ogni 60 s e un backup automatico al
primo tick (~60 s). È quindi incluso il costo del backup, che nelle misure
precedenti era la seconda voce dopo i template.

### 9.2 Prima / dopo

| Campione | Prima (`cc49ae5`) | Dopo | Δ |
|---|---:|---:|---:|
| RSS a 60 s (prima del backup) | **45,3 MiB** (46.344 KiB) | **26,3 MiB** (26.964 KiB) | **−19,0 MiB** |
| RSS a 75 s (subito dopo `VACUUM INTO`) | 67,1 MiB (68.696 KiB) | 47,5 MiB (48.620 KiB) transitorio | −19,6 MiB |
| RSS a 300 s | **52,8 MiB** (54.064 KiB) | **34,7 MiB** (35.516 KiB) | **−18,1 MiB** |
| HWM a 300 s | 68,7 MiB | 49,6 MiB | −19,1 MiB |
| Heap `inuse_space` | **25,5 MiB** | **6,0 MiB** | −19,5 MiB |
| Template nel heap | 23,0 MiB (90%) | 2,0 MiB (33%) | −21,0 MiB |
| FD / thread a riposo | 13 / 10 | 15 / 10 | stabili |

Dopo il campione a 75 s (backup + profilo heap) l'RSS dell'istanza ottimizzata
scende da solo a **33,7–34,3 MiB** in circa un minuto e resta lì; il valore a
300 s include un nuovo profilo heap. Il campione a 300 s è stato accettato dal
coordinatore come misura finale; non è stato prolungato a 10/30 minuti.

### 9.3 Consumatori dopo la correzione

`pprof -top -sample_index=inuse_space` a 60 s e 300 s:

```text
 1537 kB  runtime.mallocgc (stack del profilo / runtime)
 1536 kB  encoding/json/v2.makeString  ← cataloghi i18n (una tantum)
  525 kB  golang.org/x/net/html.map.init.1
 2048 kB  web.compileTemplateSet       ← solo il set inglese
  512 kB  net/http.init
```

- **Template:** il set inglese costa ~2,0 MiB di heap; prima se ne compilavano
  sette all'avvio (~23 MiB). Le altre lingue si compilano alla prima richiesta.
- **i18n:** ~1,5 MiB, caricati una volta in `init`, nessuna allocazione per
  richiesta. Non è stato modificato: non è un consumo per richiesta e il
  margine sotto il vecchio tetto di 40 MiB non lo richiede.
- **SQLite:** pool a 4 connessioni invariato (nessuna evidenza per ridurlo).
- **Backup:** `VACUUM INTO` resta; dopo la copia `debug.FreeOSMemory()` restituisce
  al sistema le pagine del processo. Il picco transitorio resta (~48 MiB a 75 s
  con un DB da 13 MiB), ma non si accumula più: prima l'RSS restava ~53 MiB.

### 9.4 Limiti e note

- Le altre lingue non vengono mai sfrattate: un'istanza che rende tutte e 7 le
  lingue somma i set (~2 MiB di heap ciascuno) e si avvicina al vecchio tetto.
  L'inglese è sempre residente come default e fallback. Lo sfrattamento è stato
  evitato perché richiederebbe di coordinarsi con i render in corso.
- Backup e profilo heap sono transienti: il budget CI misura a 60 s su un DB
  seed senza backup (`default_interval_seconds=3600`), quindi confronta il
  valore 26,3 MiB, non il picco del backup.
- `scripts/budget.sh` avvisa sopra 64 MiB e fallisce sopra 128 MiB. Il job CI
  non ha override: usa le stesse soglie.
- Il campione "prima" e quello "dopo" sono due esecuzioni distinte sullo stesso
  tipo di DB e di seed; la macchina è condivisa, quindi le differenze sotto
  1–2 MiB non sono significative.

---

## 10. Giro CPU/RAM del 25 ottobre 2026

Questo giro non cambia query SQL né schema: riduce il lavoro per richiesta
Torznab e la memoria temporanea delle fusioni.

### 10.1 Modifiche

1. **Normalizzatori precompilati** (`internal/dedup`): `normalizeLanguage`,
   `normalizeSource` e `normalizeCodec` costruivano un `strings.Replacer` per
   ogni titolo. Le tre tabelle sono ora variabili di package. La stessa
   correzione è stata applicata a `textutil.EscapeLike`, usato nella
   costruzione dei pattern `LIKE`.
2. **Snapshot delle righe per i feed piccoli** (`internal/torznabapi`): quando
   la finestra contiene già tutte le righe, lo snapshot conserva le release
   fuse, non solo il totale. Le richieste successive dello stesso ciclo Arr
   evitano del tutto store e fusione.
3. **Finestra limitata per le query** (`internal/torznabapi`): una ricerca con
   un termine non selettivo (`1080p`) usava `searchAllFused` e leggeva fino a
   20.000 righe prima di restituire la prima pagina. Ora usa la stessa finestra
   di `rssWindowPage` (almeno 500 righe, crescita solo se la fusione non basta
   a riempire la pagina).

Il totale di una query oltre la finestra resta una stima: è il conteggio dello
store meno i duplicati osservati nella finestra, come già avveniva per i feed
RSS. Il comportamento è documentato perché i client non devono usare `total`
come garanzia di esaustività su archivi molto grandi.

### 10.2 Benchmark in-process

Stesso seed, stessa macchina, `-benchmem`, 200–300 iterazioni; valori medi.

| Benchmark | Prima | Dopo | Δ tempo | Δ allocato |
|---|---:|---:|---:|---:|
| `TorznabSearch/items_10` | 6,57 ms · 2,92 MB · 6.955 alloc | **0,95 ms · 0,13 MB · 1.723 alloc** | **−86%** | **−96%** |
| `TorznabSearch/items_100` | 10,28 ms · 3,83 MB · 17.498 alloc | **4,99 ms · 1,03 MB · 12.268 alloc** | **−52%** | **−73%** |
| `TorznabSearch/items_100_text` | 4,83 ms · 456 KB | 4,88 ms · 456 KB | ~0 | ~0 |
| `TorznabSearch/tvsearch_100` | 5,00 ms · 25,6 KB | 4,90 ms · 25,6 KB | ~0 | ~0 |
| `SearchReleases/50k/text` | 14,0 ms · 728 KB | 14,2 ms · 728 KB | ~0 | ~0 |

Il profilo `alloc_space` prima del giro attribuiva **1.273 MB su 2.011 MB**
(63%) a `strings.(*Replacer).build`; dopo la correzione la voce scompare dai
primi posti.

### 10.3 Harness end-to-end

`./scripts/bench.sh`, build di release, 100 richieste sequenziali e 20
concorrenti per scenario. La macchina è condivisa: i decimali sotto i 5 ms
sono rumore.

| Dataset | Scenario | p95 prima | p95 dopo | Δ |
|---|---|---:|---:|---:|
| 10k | stress `1080p` | 269,8 ms | **38,4 ms** | **−86%** |
| 10k | 20× stress | 4.672,5 ms | **217,7 ms** | **−95%** |
| 10k | RSS dopo il carico | 103,3 MiB | **67,7 MiB** | **−34%** |
| 50k | stress `1080p` | 1.981,3 ms | **61,4 ms** | **−97%** |
| 50k | 20× stress | 28.767,0 ms | **370,7 ms** | **−99%** |
| 50k | RSS dopo il carico | 111,8 MiB | **65,7 MiB** | **−41%** |
| 50k | title+cat | 37,4 ms | 35,1 ms | −6% |
| 50k | empty | 2,9 ms | 3,7 ms | rumore |

RSS a riposo e startup restano sostanzialmente invariati (46–48 MiB; 104–153
ms). Il binario resta 20,1 MiB.

### 10.4 Test di non-regressione

- `TestRSSWindowDoesNotFuseArchive` ora verifica anche una query non selettiva
  (`q=Archive`): 100 item per pagina, `total=20000`, seconda pagina coerente.
- I test esistenti su fusione e paginazione (`TestFusionRunsBeforePagination`,
  `TestServarrPaginationReceivesEveryFusedRelease`) restano verdi.
- `TestEscapeLike` copre i metacaratteri `%`, `_` e la barra rovesciata.
