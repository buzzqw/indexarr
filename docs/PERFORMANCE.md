# Prestazioni e risorse di indexarr

Documento di misura della passata "performance e resource optimization" su
indexarr 2. Le modifiche riguardano solo `internal/store/**` e
`internal/torznabapi/**`; il generatore di dataset e la suite di misura vivono
in `internal/perf/**` e non vengono linkati dal binario di produzione.

Ultimo aggiornamento: **23 ottobre 2026**.

---

## 1. Obiettivi

Questi sono i limiti che il prodotto deve rispettare. Non sono numeri misurati:
sono la soglia oltre la quale una modifica viene considerata una regressione.

| Obiettivo | Soglia |
|---|---|
| Binario statico (`CGO_ENABLED=0`, `-trimpath -s -w`) | < 20 MB |
| RSS a riposo | < 60 MB |
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
| RSS a riposo < 60 MB | **32,4–36,5 MiB** (51,0 MiB dopo il carico a 50k) | ✅ |
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
