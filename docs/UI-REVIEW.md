# Revisione UI indexarr 2 — base indipendente

Data: 23 ottobre 2026  
Ambito: interfaccia web di `indexarr` in `/home/andres/indexarr`, istanza viva su `http://127.0.0.1:8484`.  
Modalità: **sola lettura**. Nessuna modifica a codice, template, CSS, dati o servizio. Solo richieste `GET`.
Nessuno screenshot: le evidenze sono state raccolte con `curl` e con l'ispezione dei template.

> **Snapshot.** Le evidenze riflettono i file letti e l'istanza viva il 23 ottobre 2026 (base `8fa3c57`). Il redesign procede in parallelo e può aver già cambiato template e route: questo documento va usato come baseline e checklist, non come fotografia finale.

Scopo del documento: fotografare la confusione attuale («vecchio e nuovo mescolati») e fornire la checklist di riferimento per il redesign parallelo.

---

## 1. Inventario

### 1.1 Voci di navigazione attuali

La barra principale contiene **12 voci piatte**, senza gruppi:

`Dashboard` · `Releases` · `Sources` · `Definizioni` · `Indexer nativi` · `Applicazioni` · `Importa` · `Runs` · `Rules` · `Logs` · `Settings` · `Maintenance`

Otto etichette sono in inglese, quattro in italiano. La pagina dichiara `<html lang="en">` e lo skip link è `Skip to content`.

### 1.2 Pagine e route di primo livello

| Route | H1 / voce | Scopo | Strato | CTA primaria |
|---|---|---|---|---|
| `GET /` | `Dashboard` | Cache release, stato sorgenti, ultime esecuzioni | Legacy (dati), aspetto misto | `Run all now`; secondaria `Add source` |
| `GET /releases` | `Releases` | Ricerca nella cache SQLite, filtri, ricerche salvate | Legacy | `Search` |
| `GET /releases/{id}` | titolo release | Dettaglio release, magnet/`.torrent`, preferiti, blocco, invio | Legacy | `Download .torrent` / `Send to…` |
| `GET /sources` | `Sources` | Elenco e gestione delle sorgenti legacy (Torznab, RSS, HTML) | Legacy | `Run all now`; secondarie `Add source`, `Bulk add RSS`, `Export JSON`, `Test all` |
| `GET /sources/new` | `New source` | Form sorgente legacy con preset Prowlarr/Jackett/RSS/HTML | Legacy | `Create source` |
| `GET /sources/{id}/edit` | `Edit source` | Modifica sorgente legacy | Legacy | `Save source` |
| `GET /sources/bulk` | `Bulk add RSS feeds` | Import massivo RSS/OPML | Legacy | `Add feeds` |
| `GET /sources/discover` | `Feed discovery` | Scoperta feed da una pagina | Legacy | `Add as RSS` per candidato |
| `GET /definitions` | `Definizioni` | Catalogo nativo + definizioni locali + definizioni `legacy-*` | Nativo | `Nuova definizione` |
| `GET /definitions/new` | `Nuova definizione` | Incolla/carica YAML, valida e salva | Nativo | `Valida e salva` |
| `GET /definitions/{id}` | nome definizione | Metadati, YAML, indexer che la usano | Nativo | `Crea indexer` |
| `GET /indexers` | `Indexer nativi` | Elenco indexer nativi **e** ponte legacy generati dal backfill | Misto nativo/ponte | `Nuovo indexer` |
| `GET /indexers/new` | `Nuovo indexer nativo` | Form generato dai `settings` della definizione | Nativo | `Crea indexer` |
| `GET /indexers/{id}` | nome indexer | Stato, test, run, configurazione pubblica, segreti | Misto | `Esegui ora`; secondaria `Esegui test` |
| `GET /indexers/{id}/edit` | `Modifica …` | Modifica campi dell'indexer e segreti | Misto | `Salva indexer` |
| `GET /applications` | `Applicazioni` | Istanze \*arr collegate e livello di sync | Nativo | `Nuova applicazione`; secondaria `Sincronizza tutte` |
| `GET /applications/new` | `Nuova applicazione` | Form Sonarr/Radarr/Lidarr/… | Nativo | `Crea applicazione` |
| `GET /applications/{id}/edit` | `Modifica …` | Modifica applicazione e chiave | Nativo | `Salva applicazione` |
| `GET /import` | `Importa` | Anteprima piano da Prowlarr/Jackett | Nativo, **solo anteprima** | `Genera piano da Prowlarr` / `Genera piano da Jackett` |
| `GET /runs` | `Runs` | Storico ingest legacy | Legacy | `Filter`; secondaria `Clear older than 30 days` |
| `GET /runs/{id}` | dettaglio run | Dettaglio di una singola esecuzione legacy | Legacy | — |
| `GET /rules` | `Rules` | Blocklist per infohash/titolo/regex/sorgente/categoria | Legacy | `Add rule` |
| `GET /logs` | `Logs` | Buffer log in memoria, livello runtime, download | Sistema | `Apply`; secondarie `Problems`, `Download .txt`, `Clear` |
| `GET /settings` | `Settings` | API key, ingestion, FlareSolverr, notifiche, client download, backup, retention, accesso web | Sistema | `Save settings` |
| `GET /maintenance` | `Maintenance` | Report DB, check, pulizia, backup/restore, danger zone | Sistema | `Download backup`; `Backup now` |
| `GET /healthz`, `/metrics`, `/sw.js`, `/static/*` | — | Endpoint tecnici / PWA | Sistema, non UI | — |
| `/api`, `/newznab/api`, `/torznab/api`, `/api/v2/` | — | API Torznab e admin | API, non UI | — |

**CTA**: nell'inventario ho indicato il pulsante marcato `primary` nei template; dove non c'è, la prima azione visibile.

### 1.3 Stato osservato sull'istanza viva

- Dashboard: `37 / 37` sorgenti abilitate, `2343` release, `679` run nelle 24h con `169 failed`.
- `/sources`: 37 sorgenti; la tabella contiene **111 pulsanti** (3 per riga: `Run`, `Test`, `Clone`), oltre a `Edit` e agli eventuali `Cancel`.
- `/indexers`: **37 righe**, tutte con definizione `legacy-*`; tutte mostrano stato `in attesa` (37) e ultima esecuzione `mai` (37).
- `/definitions`: **44 righe**, di cui **37** sono definizioni `legacy-*` generate dal backfill; le altre 7 sono catalogo/esempi.
- `/applications`: `Nessuna applicazione configurata`.
- `/runs`: 200 righe (limite della query), tutte riferite a sorgenti legacy.
- Esistono sorgenti con nomi auto-generati e duplicati visivi: `extto.org`, `extto.org (2)`, … `extto.org (19)`, dovuti agli import massivi.

---

## 2. Perché è confuso

### 2.1 La stessa entità compare due volte, con due ID e due form

Il backfill (`BackfillLegacySources`) crea per ogni riga di `sources` una definizione `legacy-<id>` e una riga in `indexers` con `legacy_source_id`. Sull'istanza viva:

- `/sources` elenca `extto.org` → `/sources/4/edit`;
- `/indexers` elenca lo stesso `extto.org` → `/indexers/4`, definizione `legacy-4`, slug `extto-org`.

L'utente vede lo stesso indexer in due pagine, con due form, due ID e due azioni `Esegui`. Non è indicato da nessuna parte che sono la stessa entità. Peggio: modificare `/indexers/4/edit` **non modifica la sorgente legacy** che lo scheduler esegue davvero; i segreti non vengono copiati dal backfill, quindi il dettaglio indexer mostra `Segreti: nessuno` mentre la sorgente funziona con la sua API key.

### 2.2 «Indexer nativi» non è vero: 37 su 37 sono ponte legacy

La pagina si chiama `Indexer nativi` e la descrizione dice «Istanze configurate delle definizioni native», ma tutte le 37 righe puntano a definizioni `legacy-*` e sono saltate dal runner nativo (`ReasonLegacyBridge` in `internal/native`). Il risultato visibile è:

- stato `in attesa` e `mai` su tutte le righe, perché la cronologia nativa non viene mai scritta;
- pulsanti `Esegui` / `Test` / `Modifica` presentati come se funzionassero nativamente;
- nessun badge `Ponte legacy`, `Legacy`, `Migrato` o simile;
- definizioni `legacy-*` elencate come normali definizioni, con CTA `Crea indexer` che creerebbe un ennesimo indexer ponte.

### 2.3 Quattro modi per aggiungere un indexer/sorgente

1. Dashboard → `Add source` → `/sources/new` (preset Torznab/RSS/HTML).
2. `/sources` → `Quick add` (rileva il tipo dall'URL).
3. `/sources` → `Bulk add RSS` (RSS/OPML).
4. `/indexers` → `Nuovo indexer` (definizione nativa + campi generati).

Non c'è un chooser che spieghi quale percorso usare. Il form legacy chiama il tipo `Torznab / indexer`, confondendo il protocollo con l'entità. L'utente che vuole «aggiungere un indexer» ha quattro porte e nessuna mappa.

### 2.4 `/sources` e `/indexers` si sovrappongono anche nelle azioni

- Due `Test all` diversi per semantica: `/sources/test-all` testa le sorgenti legacy; `/indexers/{id}/test` esegue un test nativo a fasi; `/maintenance` offre un terzo `Test` sulle sorgenti in errore.
- Due `Run` diversi: `/sources/{id}/run` e `/indexers/{id}/run`.
- Due `Esegui ora` con esiti diversi: il primo scrive in `runs`, il secondo in `indexer_history`.
- La pagina `Runs` legge solo `runs` (legacy); le esecuzioni degli indexer nativi non compaiono. Le attività sono quindi divise in due mondi senza un'unica vista.

### 2.5 La dashboard racconta solo il vecchio mondo

Le card sono `Releases`, `Sources`, `Runs (24h)`, `Database`; il sottotitolo è «Cached releases, source health and the last runs». Non esistono indicatori per:

- indexer nativi vs ponte legacy;
- definizioni installate / catalogo / `legacy-*`;
- applicazioni collegate e ultimo sync;
- migrazione in corso o completata.

Le CTA sono `Add source` e `Run all now`: entrambe legacy. Un utente nuovo non capisce che esiste un secondo modello dati.

### 2.6 Definizioni, indexer, applicazioni: tre concetti senza gerarchia visibile

- `Definizioni` = blueprint; `Indexer` = istanza; `Applicazioni` = destinazioni \*arr. La relazione non è spiegata né nella nav né nelle pagine.
- La tabella definizioni mescola catalogo, definizioni locali e 37 `legacy-*`, senza filtri, ricerca o paginazione. Le colonne sono 9.
- La pagina definizione offre `Crea indexer` anche per una definizione `legacy-*`, cioè per un connettore che il motore nativo rifiuta.
- Le definizioni non hanno route di modifica/eliminazione (`GET /definitions/new`, `POST /definitions`, nessun `POST /definitions/{id}`): dopo la creazione il solo modo per correggere è ricreare.
- Le applicazioni dicono «sincronizza gli indexer» ma l'utente vede 37 sorgenti e 37 indexer: non sa quali verranno sincronizzati.

### 2.7 Impostazioni e manutenzione duplicate

| Funzione | Dove appare | Problema |
|---|---|---|
| Backup automatici | `Settings` → `Automatic backups`; `Maintenance` → `Backups` / `Backup now` / `Restore from file` | Policy e operazioni separate in due pagine |
| Retention | `Settings` → `Data retention`; `Maintenance` → `Apply retention now` | Configurazione e azione separate |
| Errori sorgente | `/sources`, card e tabella `Sources needing attention` in `/maintenance` | Due viste, due CTA `Test`/`Run` |
| Pulizia run | `/runs` → `Clear older than 30 days`; `/maintenance` → `Clear run history` | Stessa azione in due punti |
| API key | `Settings` | Unico punto, ma la pagina è lunga e mescola key, notifiche, backup e accesso web |

### 2.8 L'import è un vicolo cieco

`/import` è in navigazione come funzione di primo livello, ma la pagina dice:

> «L'import produce solo un **piano**… **Non** crea definizioni, non scrive nel database»  
> «L'applicazione del piano arriverà insieme alla mappatura delle definizioni native.»

Non esiste alcuna CTA `Applica` o `Annulla migrazione`. L'API admin (`legacyimport.Apply` / `Revert`) esiste, ma non è raggiungibile dalla UI. L'utente vede una funzione incompleta collocata accanto a funzioni complete.

### 2.9 Lingua e terminologia incoerenti

- 8 voci di nav in inglese (`Dashboard`, `Releases`, `Sources`, `Runs`, `Rules`, `Logs`, `Settings`, `Maintenance`) e 4 in italiano (`Definizioni`, `Indexer nativi`, `Applicazioni`, `Importa`).
- Le pagine legacy sono interamente in inglese; le pagine native (`Definizioni`, `Indexer nativi`, `Applicazioni`, `Importa`) sono interamente in italiano.
- `lang="en"` nonostante la metà italiana dell'interfaccia.
- Nei filtri di `/releases` ci sono **due label `Source`**: la sorgente (`id="source"`) e la qualità della fonte (`id="source_quality"`).
- «Source», «Indexer», «Definizione», «Applicazione», «Ponte» non hanno un glossario visibile.

### 2.10 Navigazione e gerarchia delle azioni

- 12 voci piatte, nessun gruppo, nessuna sezione secondaria.
- Su mobile la nav diventa una riga orizzontale scrollabile con scrollbar nascosta (`nav { overflow-x: auto; scrollbar-width: none }`): le voci oltre lo schermo non hanno indizio di esistenza.
- `/sources` ha 5 CTA nell'header, 2 form in `Quick add` e una barra bulk con altre 5 azioni. La CTA primaria è `Run all now`, non `Add source`.
- Ogni riga sorgente ha 3–5 pulsanti: su 37 righe sono 111 pulsanti nella sola tabella.
- Le tabelle `Indexer nativi` (11 colonne) e `Definizioni` (9 colonne) non hanno ricerca, filtro o paginazione.

### 2.11 Empty state che presuppongono il vecchio modello

- Dashboard: «No sources yet. Add a Torznab endpoint…» anche se esistono indexer nativi.
- `/indexers`: «Nessun indexer nativo configurato. Crea prima una definizione e poi un indexer.» anche quando le definizioni ci sono.
- `/applications`: «Nessuna applicazione configurata…» senza spiegare cosa verrà sincronizzato.
- `/definitions`: «Nessuna definizione disponibile.» senza distinguere catalogo, locali e legacy.

### 2.12 Nessun percorso di migrazione visibile

README e piano dicono che il percorso legacy resta il predefinito finché la migrazione non è completa, e che il backfill è il ponte. La UI non mostra:

- quante entità sono ancora legacy (37);
- quali sono migrabili e quali no;
- come applicare o annullare una migrazione;
- cosa cambia per l'utente dopo la migrazione.

Il risultato è esattamente la percezione riportata: «una confusione di vecchie e nuove funzioni».

---

## 3. IA proposta

### 3.1 Navigazione di primo livello (8 voci, raggruppata)

| # | Voce | Route canonica | Sotto-voci / tab | Note |
|---|---|---|---|---|
| 1 | **Panoramica** | `/` | — | KPI unificati: release, indexer nativi, ponte legacy, applicazioni, run falliti; una sola CTA primaria |
| 2 | **Release** | `/releases` | — | Resta la pagina di ricerca; dettaglio `/releases/{id}` |
| 3 | **Indexer** | `/indexers` | `Nativi` (`?tipo=nativi`) · `Ponte legacy` (`?tipo=ponte`) | Un solo elenco, due tab; badge espliciti; creazione con chooser |
| 4 | **Definizioni** | `/definitions` | `Catalogo` · `Locali` · `Legacy` (filtro, non tab obbligatorio) | Le `legacy-*` non stanno nella tabella principale |
| 5 | **Applicazioni** | `/applications` | — | Destinazioni \*arr e livello di sync |
| 6 | **Importa** | `/import` | `Prowlarr` · `Jackett` | Anteprima + `Applica`/`Annulla` quando disponibile; altrimenti badge `Anteprima` e voce secondaria |
| 7 | **Attività** | `/activity/runs` | `Esecuzioni` · `Registro` (`/activity/logs`) | Unifica run legacy e run nativi, oppure due sezioni chiaramente etichettate |
| 8 | **Sistema** | `/system/settings` | `Regole` · `Impostazioni` · `Manutenzione` | Backup/retention: policy in Impostazioni, azioni in Manutenzione, con link incrociati |

Regole di visualizzazione:

- 8 voci massimo; le sotto-voci sono tab, non nuove voci di nav.
- `aria-current="page"` sulla voce di gruppo e `aria-current="page"` (o `true`) sulla tab attiva.
- Su mobile: menu a scomparsa (drawer) o due righe; mai l'unica riga scrollabile con scrollbar nascosta.
- Su ogni pagina: una sola CTA primaria; le azioni distruttive restano nella danger zone.

### 3.2 Gerarchia delle pagine

```
Panoramica
├── KPI unificati (release, nativi, ponte, applicazioni, errori)
├── "Cosa fare adesso" (massimo 3 azioni contestuali)
├── Attività recente unificata (legacy + nativo)
└── Avviso migrazione (solo se esistono ponte legacy)

Release
├── Elenco + ricerca + filtri salvati
└── Dettaglio release

Indexer
├── Tab Nativi
│   ├── Elenco (Nome, Definizione, Protocollo, Stato, Ultima esecuzione)
│   ├── Nuovo indexer nativo
│   └── Dettaglio → Test / Esegui / Modifica / Elimina
└── Tab Ponte legacy
    ├── Banner: "37 indexer gestiti dalle sorgenti legacy"
    ├── Elenco (Nome, Sorgente, Stato reale, Ultima esecuzione legacy)
    ├── Azioni: "Apri sorgente" / "Migra" (se disponibile)
    └── Nessun "Esegui nativo" se il runner lo salta

Definizioni
├── Catalogo (badge "catalogo")
├── Locali (badge "locale")
└── Legacy (badge "ponte legacy"; sola lettura, niente "Crea indexer")

Applicazioni
├── Elenco + ultimo sync
└── Form + livello di sync

Importa
├── Prowlarr (anteprima)
├── Jackett (anteprima)
└── Piano → Applica / Annulla (quando disponibile)

Attività
├── Esecuzioni (legacy + native, filtro per entità e stato)
└── Registro (log, livello runtime, download)

Sistema
├── Regole (blocklist)
├── Impostazioni (API key, ingestion, notifiche, client, backup policy, retention, accesso)
└── Manutenzione (report DB, check, pulizia, backup/restore, danger zone)
```

### 3.3 Route canoniche e redirect

| Vecchia route | Nuova route | Tipo | Note |
|---|---|---|---|
| `/sources` | `/indexers?tipo=ponte` | `301` (GET) | La pagina diventa il tab ponte |
| `/sources/new` | `/indexers/new?tipo=ponte` | `301` (GET) | Form legacy dentro il chooser |
| `/sources/{id}/edit` | `/indexers/{indexerID}/edit?tipo=ponte` | `301` (GET) | Risolvere `indexerID` via `legacy_source_id`, **non** assumere ID uguali |
| `/sources/bulk` | `/indexers/bulk-rss` | `301` (GET) | Resta un'azione secondaria |
| `/sources/discover` | `/indexers/discover` | `301` (GET) | — |
| `/runs` | `/activity/runs` | `301` (GET) | — |
| `/runs/{id}` | `/activity/runs/{id}` | `301` (GET) | — |
| `/logs` | `/activity/logs` | `301` (GET) | — |
| `/logs/download` | `/activity/logs/download` | `301` (GET) | — |
| `/rules` | `/system/rules` | `301` (GET) | — |
| `/settings` | `/system/settings` | `301` (GET) | — |
| `/maintenance` | `/system/maintenance` | `301` (GET) | — |
| `/maintenance/backup*` | `/system/maintenance/backup*` | `301` (GET) | — |
| `/maintenance/backups/{name}` | `/system/maintenance/backups/{name}` | `301` (GET) | — |

**POST**: non redirigere i POST con `301/302`. Mantenere gli handler legacy come alias (o usare `308` solo se si conserva metodo e body). I form esistenti continuano a postare su `/sources/...`, `/runs/clear`, `/settings`, `/maintenance`: devono continuare a funzionare e, dopo il successo, redirigere alla nuova route.

**Collisioni da evitare**: `/indexers/new` e `/indexers/{id}` convivono (Go 1.22 dà precedenza al literal), ma aggiungere `/indexers/ponte` o `/indexers/nativi` come literal prima di `{id}`. Non usare lo stesso path per GET e POST con semantiche diverse.

---

## 4. Checklist di redesign

Priorità: **P0** = blocca il rilascio/confonde ancora; **P1** = qualità e coerenza; **P2** = rifinitura.

### P0 — togliere l'ambiguità

| ID | Intervento | Criterio di accettazione |
|---|---|---|
| P0-1 | Nav a 8 voci raggruppata; tab per Indexer, Attività, Sistema | 8 `<a>` di primo livello, 0 duplicati, `aria-current` sulla voce e sulla tab attiva |
| P0-2 | Tab `Nativi` / `Ponte legacy` in `/indexers` | Entrambe le tab presenti; ogni riga ha badge `Nativo` o `Ponte legacy`; il conteggio corrisponde ai dati |
| P0-3 | Ponte legacy: niente `Esegui`/`Test` nativo; mostrare `Gestito dalla sorgente legacy` e link alla sorgente | Nessuna riga ponte offre `Esegui ora`; lo stato non è `in attesa` ma `Gestito da sorgente` o `Migrato` |
| P0-4 | Dashboard unificata con KPI nativi + ponte + applicazioni, una CTA primaria | La dashboard cita almeno indexer nativi, ponte, applicazioni; una sola CTA `primary` |
| P0-5 | Definizioni: separare `legacy-*`; niente `Crea indexer` su legacy; filtro Catalogo/Locali/Legacy | La tabella principale non mescola 37 legacy; il dettaglio legacy non mostra `Crea indexer` |
| P0-6 | Importa: implementare `Applica`/`Annulla` **oppure** marcare `Anteprima` e togliere dalla nav primaria | Se non operativo: badge `Anteprima`, nessuna promessa di apply, voce in Sistema o secondaria |
| P0-7 | Attività unificata o due sezioni esplicite | `/activity/runs` mostra anche i run nativi, oppure tab `Sorgenti legacy` / `Indexer nativi` con conteggi |
| P0-8 | Lingua unica: italiano, `lang="it"`, skip link `Vai al contenuto` | `grep 'lang="it"'`; nessuna label inglese nelle pagine italiane; skip link italiano |
| P0-9 | Empty state dipendenti dall'entità | Dashboard distingue «nessuna sorgente», «nessun indexer nativo», «nessuna applicazione»; ogni empty state ha una CTA coerente |
| P0-10 | CTA: una primaria per pagina; `Aggiungi` prima di `Esegui`; distruttive solo in danger zone | Conteggio `btn primary` = 1 per pagina; `Add source` non è più secondaria rispetto a `Run all` |

### P1 — coerenza e usabilità

| ID | Intervento | Criterio di accettazione |
|---|---|---|
| P1-1 | Ridurre le azioni per riga a 2 visibili + menu `⋯` | Nessuna riga di `/sources` o `/indexers` supera 2 pulsanti visibili |
| P1-2 | Tabelle: ricerca, filtro, paginazione; ridurre le colonne | Definizioni ≤ 5 colonne, Indexer ≤ 6; 37/44 righe navigabili senza scroll orizzontale |
| P1-3 | Badge di stato coerenti | Set unico: `Nativo`, `Ponte legacy`, `Attivo`, `In attesa`, `Errore`, `Disattivato`, `Migrato`; stessi colori in tutte le pagine |
| P1-4 | Backup/retention: policy in Impostazioni, azioni in Manutenzione, link incrociati | Nessuna funzione duplicata senza link; `Settings` non contiene la lista backup |
| P1-5 | Separare «backup sorgenti JSON» (Sistema/Manutenzione) da «migrazione Prowlarr/Jackett» (Importa) | `/import` non contiene import/export JSON di sorgenti |
| P1-6 | Percorso di migrazione visibile | Banner ponte con conteggio e link a anteprima/apply/revert; stato per riga |
| P1-7 | Mobile: drawer o due righe; target ≥ 44 px; nessuna scrollbar nascosta come unico affordance | Test a 360 px: tutte le 8 voci raggiungibili, focus visibile, nessun taglio |
| P1-8 | Accessibilità form e tabelle | Ogni input ha label univoca; i due `Source` di `/releases` diventano `Sorgente` e `Qualità fonte`; `th scope` presente; focus visibile |
| P1-9 | Contrasto | Già misurato: muted 5.53:1 su light e 7.58:1 su dark, accent 6.19:1/7.99:1, ok/warn > 5.4:1. Mantenere ≥ 4.5:1 e verificare i nuovi badge |
| P1-10 | Copy: glossario breve e stabile | `Sorgente legacy`, `Indexer nativo`, `Definizione`, `Applicazione`, `Ponte` definiti una volta e usati ovunque |

### P2 — rifinitura

| ID | Intervento | Criterio di accettazione |
|---|---|---|
| P2-1 | Filtri avanzati release in un pannello espandibile | La pagina resta leggibile senza aprire i filtri |
| P2-2 | Unificare date/ore e `ago` in italiano | Nessun `just now` / `never` residuo |
| P2-3 | Eliminare le route e i template morti dopo il periodo di deprecazione | Nessun link interno punta a route deprecate |
| P2-4 | Messaggi flash con `role="status"` / `role="alert"` e testo contestuale | I messaggi indicano l'entità e l'azione, non solo «ok» |
| P2-5 | PWA: pagina offline e cache solo asset | Nessun HTML con CSRF in cache |

### Cosa rimuovere o fondere

- Rimuovere `/sources` come pagina autonoma: diventa il tab `Ponte legacy` di `/indexers`.
- Fondere `Quick add`, `Bulk add RSS` e `New source` in un unico chooser con tre modalità.
- Rimuovere `Sources needing attention` da `/maintenance`: il problema appartiene a `/indexers?tipo=ponte`.
- Rimuovere la lista backup da `Settings`: resta la policy; la lista va in `Manutenzione`.
- Rimuovere `Importa` dalla nav primaria finché non esiste `Applica`.
- Non mostrare `Crea indexer` sulle definizioni `legacy-*`.
- Non mostrare `Esegui ora` / `Esegui test` sugli indexer ponte.

---

## 5. Rischi

| Rischio | Impatto | Mitigazione |
|---|---|---|
| URL vecchi salvati nei bookmark o nei client | `404` o pagine vuote | Redirect `301` per GET; mantenere i POST alias; test su tutte le route della tabella 3.3 |
| Redirect dei POST con `301/302` | Perdita di metodo/body/CSRF, form rotti | Non redirigere i POST; usare `308` solo se necessario e conservare gli handler |
| Route duplicate o in conflitto (`/sources/{id}` vs `/indexers/{id}`, `/indexers/new` vs `{id}`) | Panic del mux o handler sbagliato | Registrare i literal prima; test `GET /indexers/new` e `GET /indexers/1` |
| Test che asseriscono le vecchie etichette | Build rossa | `internal/web/web_test.go` (`TestDashboard` cerca `No sources yet`), `native_test.go` (`Indexer nativi`, `Nuovo indexer nativo`), `maintenance_test.go` (`Sources needing attention`): aggiornare ai nuovi testi e, dove possibile, asserire comportamento/route invece del copy |
| Utenti con sorgenti legacy esistenti | Perdita di configurazione o doppia esecuzione | Non eliminare `sources`; migrare solo dopo che il nativo funziona; mantenere `legacy_source_id`; backup pre-migrazione |
| ID sorgente ≠ ID indexer | Redirect o edit sulla entità sbagliata | Risolvere sempre `legacy_source_id`; non assumere uguaglianza |
| Segreti non copiati nel backfill | L'indexer ponte sembra senza chiave e fallisce se eseguito nativamente | Mostrare `Gestito dalla sorgente legacy`; non offrire run nativo; migrare i segreti nel vault solo con `Apply` |
| Eliminazione di una definizione `legacy-*` | Indexer ponte orfani | Nessuna route di delete oggi; se aggiunta, bloccare se `Indexers > 0` |
| `lang="it"` e nuovi testi | Screenshot/test E2E con testo inglese | Aggiornare gli snapshot e i test in un unico commit |
| Cache del service worker | Vecchia UI dopo il deploy | Gli asset hanno `?v=`; l'HTML è `no-store`; verificare dopo il redesign |
| API e Torznab | Rottura dei client \*arr | Non toccare `/api`, `/torznab/api`, `/newznab/api`, `/dl/`, `/healthz`; testarli separatamente |

---

## 6. Verifica dopo il redesign

Checklist `curl` da eseguire su un'istanza di staging (non sulla produzione):

```bash
base=http://127.0.0.1:8484

# 1. Pagine canoniche: tutte 200
for p in / /releases /indexers /definitions /applications /import \
         /activity/runs /activity/logs /system/rules /system/settings /system/maintenance; do
  printf '%s ' "$p"; curl -s -o /dev/null -w '%{http_code}\n' "$base$p"
done

# 2. Redirect delle vecchie route: 301 e Location attesa
for p in /sources /sources/new /runs /logs /rules /settings /maintenance; do
  printf '%s -> ' "$p"
  curl -s -o /dev/null -w '%{http_code} %{redirect_url}\n' "$base$p"
done

# 3. Un solo salto di redirect (niente catene)
curl -sIL "$base/sources" | grep -c '^HTTP/'

# 4. Le due tab degli indexer esistono e non sono duplicate
curl -s "$base/indexers" | grep -o 'Nativi\|Ponte legacy' | sort | uniq -c
# atteso: una occorrenza per tab (più eventuali badge di riga)

# 5. Nav: 8 voci, nessun duplicato
curl -s "$base/" | python3 -c '
import sys,re
h=sys.stdin.read()
nav=re.search(r"<nav[^>]*>(.*?)</nav>",h,re.S).group(1)
links=re.findall(r"<a [^>]*href=\"([^\"]+)\"[^>]*>(.*?)</a>",nav,re.S)
print(len(links)); print([u for u,_ in links]); print(len(set(u for u,_ in links)))'

# 6. Lingua e accessibilità di base
curl -s "$base/" | grep -c 'lang="it"'
curl -s "$base/" | grep -c 'Vai al contenuto'
curl -s "$base/" | grep -c 'aria-current'

# 7. Nessun "Indexer nativi" duplicato e nessun Esegui sui ponte
curl -s "$base/indexers" | grep -c 'Indexer nativi'
curl -s "$base/indexers?tipo=ponte" | grep -c 'Esegui ora'   # atteso 0

# 8. Definizioni: legacy fuori dalla tabella principale
curl -s "$base/definitions" | grep -c 'legacy-'            # atteso 0 nella vista Catalogo/Locali
curl -s "$base/definitions?origine=legacy" | grep -c 'legacy-'  # atteso > 0

# 9. Dashboard unificata
curl -s "$base/" | grep -c 'Ponte legacy'
curl -s "$base/" | grep -c 'Applicazioni'

# 10. Endpoint tecnici intatti
for p in /healthz /metrics /sw.js /static/app.css; do
  printf '%s ' "$p"; curl -s -o /dev/null -w '%{http_code}\n' "$base$p"
done

# 11. I POST legacy restano vivi (staging): 403 CSRF, non 404/405
curl -s -o /dev/null -w '%{http_code}\n' -X POST "$base/sources/1/run"   # atteso 403
```

Controlli manuali consigliati:

- A 360 px: tutte le 8 voci raggiungibili, tab degli indexer visibili, nessun testo tagliato.
- Da tastiera: `Tab` mostra il focus, lo skip link porta a `#main`, `aria-current` è sulla voce e sulla tab giuste.
- Un indexer ponte: il dettaglio non deve mostrare `Esegui ora` e deve linkare la sorgente legacy.
- Una definizione `legacy-*`: nessun `Crea indexer`.
- `/import`: se non c'è `Applica`, la pagina deve dire chiaramente `Anteprima` e non deve essere in nav primaria.

---

## Sintesi

La confusione non è estetica: è **strutturale**. Due modelli dati (sorgenti legacy e indexer nativi) convivono nella stessa UI senza una relazione visibile; il backfill li duplica; la dashboard e le attività raccontano solo il modello legacy; l'import è una promessa senza azione; la lingua e la nav mescolano due generazioni. Il redesign deve prima di tutto **separare e gerarchizzare**, poi rifinire. La tab `Ponte legacy` e il badge `Nativo`/`Ponte legacy` sono il singolo intervento con il rapporto impatto/sforzo più alto.
