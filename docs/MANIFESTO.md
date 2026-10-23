# indexarr — Documento programmatico

**Il miglior indexer disponibile: ovunque, semplice, leggero.**

Data: 23 ottobre 2026
Documenti collegati: [indexarr 2 — Visione e architettura](../indexarr2.md),
[Audit](AUDIT-2026-10-23.md)

---

## La nostra ambizione

Jackett ha dimostrato che serviva un traduttore universale verso i tracker.
Prowlarr ha dimostrato che serviva un punto unico di gestione per le \*arr.
indexarr nasce per essere il passo successivo: **un solo indexer per tutto,
che gira su qualsiasi dispositivo, che si configura in due minuti e che
consuma meno di qualsiasi alternativa**.

Non vogliamo essere "un'alternativa a Prowlarr". Vogliamo che chi installa
oggi un indexer non abbia motivo di scegliere altro.

---

## 1. La promessa

Tre pilastri, due fondamenta.

| | Promessa | In una frase |
|---|---|---|
| **Ovunque** | Compatibilità totale | Ogni sistema, ogni architettura, ogni \*arr, ogni client, ogni tracker |
| **Semplice** | Facilità d'uso | Dall'installazione alla prima ricerca in meno di due minuti, senza leggere documentazione |
| **Leggero** | Risorse minime | Gira bene su un Raspberry Pi Zero 2 W e non si nota su un NAS |
| *Fondamenta* | Parità ed evoluzione | 100% di ciò che fanno Jackett e Prowlarr al giorno uno, poi crescita continua |
| *Fondamenta* | Fiducia | Libero, sicuro, privato per default, misurato in pubblico |

Ogni promessa ha **numeri** (§8) e un **meccanismo che la garantisce** (§9).
Una promessa senza numero non è una promessa.

---

## 2. Da dove partiamo

indexarr oggi è già in produzione. Valori misurati il 23 ottobre 2026:

| Misura | Oggi |
|---|---|
| Memoria (RSS) a riposo | ~30 MiB (min 26,9, max 35,2 sotto carico) |
| Binario Linux amd64, ottimizzato | 15,9 MB |
| Dipendenze native | Nessuna (SQLite in Go puro, niente CGO) |
| Piattaforme rilasciate | Linux amd64/arm64, Windows amd64, macOS arm64 |
| Database con 37 sorgenti e 2.116 release | ~22 MiB |
| Ricerca | SQLite FTS5 (trigram) |
| Immagine Docker | Alpine + binario statico |

La base tecnica (Go statico, nessun CGO, SQLite puro) è esattamente quella
che serve per essere ovunque e leggeri. Non va cambiata: va portata al
massimo.

---

## 3. Pilastro 1 — Ovunque

### 3.1 Sistemi e architetture

Tre livelli di supporto, dichiarati pubblicamente:

- **Tier 1**: build e test completi in CI a ogni commit, pacchetti ufficiali,
  bug bloccanti per il rilascio.
- **Tier 2**: build e smoke test a ogni rilascio, pacchetti ufficiali.
- **Tier 3**: build a ogni rilascio, supporto best effort.

| Sistema | Architetture | Tier |
|---|---|---|
| Linux (glibc e musl) | amd64, arm64 | 1 |
| Linux | armv7, armv6 (Raspberry Pi Zero/1), 386 | 2 |
| Linux | riscv64, ppc64le, s390x, loong64 | 3 |
| Windows 10/11, Server 2019+ | amd64, arm64 | 1 |
| macOS 12+ | arm64, amd64 (binario universale) | 1 |
| FreeBSD 14+ | amd64, arm64 | 2 |
| OpenBSD, NetBSD | amd64 | 3 |
| Android (Termux) | arm64 | 3 |

Il limite reale è il driver SQLite in Go puro: le architetture finali sono
quelle che il driver supporta, verificate in CI.

### 3.2 Distribuzione: ogni canale dove l'utente già cerca

| Ambiente | Canale |
|---|---|
| Container | Immagine multi-arch su GHCR e Docker Hub, base minimale; `PUID`/`PGID`/`TZ` come le immagini più diffuse; Podman rootless; Helm chart |
| Linux | `.deb`, `.rpm`, `.apk`, AUR, Nix flake, servizio systemd pronto, utente dedicato |
| Windows | Installer firmato (MSI) con servizio di sistema, `winget`, Scoop, Chocolatey, versione portable con icona nella tray |
| macOS | Homebrew (con `brew services`), app firmata e notarizzata |
| FreeBSD | Port/pkg, rc.d, jail TrueNAS CORE |
| NAS | Synology (SPK), QNAP (QPKG), Unraid (Community Applications), TrueNAS SCALE, OpenMediaVault, CasaOS/Umbrel |
| Home lab | Template Proxmox LXC, add-on Home Assistant |

Su ogni piattaforma l'esperienza è nativa: percorsi dati standard
(`/var/lib`, `%ProgramData%`, `~/Library/Application Support`), servizio di
sistema, log integrati nel sistema, avvio automatico.

### 3.3 Ecosistema

| Area | Copertura |
|---|---|
| \*arr | Sonarr, Radarr, Lidarr, Readarr, Whisparr, Mylar3, LazyLibrarian: Torznab/Newznab e app sync completo |
| Client torrent | qBittorrent, Transmission, Deluge, rTorrent/ruTorrent, Flood, Aria2, Synology Download Station |
| Client Usenet | SABnzbd, NZBGet |
| Protocolli in uscita | Torznab, Newznab, RSS per filtro (per l'auto-download dei client), API REST con OpenAPI |
| Compatibilità API | Alias Jackett (`/api/v2.0/...`); API Prowlarr v1 in sola lettura per i widget di dashboard (Homepage, Organizr e simili) |
| Notifiche | Webhook, Apprise, ntfy, Gotify, Telegram, Discord, email |
| Challenge | FlareSolverr e servizi compatibili; browser esterno opzionale |
| Proxy | HTTP, SOCKS4/5, per indexer |
| Osservabilità | Prometheus, health endpoint, log JSON |

### 3.4 Migrazione in un clic

Chi arriva da Jackett o Prowlarr non deve riconfigurare niente:

- **Prowlarr**: indexarr legge la configurazione tramite la sua API (URL + API
  key) oppure dal database, e importa indexer, credenziali, tag, proxy,
  applicazioni e impostazioni di sync.
- **Jackett**: importa gli indexer configurati con le credenziali.
- Ogni indexer importato viene associato alla definizione nativa di indexarr
  per quel sito. Se la definizione non esiste ancora, il sito continua a
  funzionare tramite il **ponte** (il Jackett/Prowlarr dell'utente usato come
  sorgente Torznab) finché la definizione nativa non è pronta.
- **Le \*arr**: l'app sync sostituisce gli indexer puntati a Jackett/Prowlarr
  con quelli di indexarr, mantenendo priorità e impostazioni; prima fa una
  copia e permette il rollback.
- **Convivenza**: durante la prova Jackett e Prowlarr restano utilizzabili
  come sorgenti.

### 3.5 Lingue e accessibilità

- UI tradotta (italiano e inglese al lancio; traduzioni della community con
  una piattaforma di traduzione aperta).
- Accessibilità WCAG 2.2 AA: tastiera, screen reader, contrasto.
- Tema chiaro/scuro, layout mobile (PWA già presente).

---

## 4. Pilastro 2 — Semplice

### 4.1 Primo avvio: meno di due minuti

1. **Installa e apri**: su Windows e macOS il primo avvio apre il browser;
   negli altri casi il terminale stampa l'indirizzo.
2. **Importa o parti da zero**: se trova Jackett o Prowlarr sulla rete locale
   o sulla rete Docker, propone la migrazione (§3.4).
3. **Collega le \*arr**: rileva Sonarr, Radarr e le altre sulle porte note,
   chiede solo l'API key e configura il sync.
4. **Aggiungi indexer**: cerca nel catalogo, inserisci le credenziali; il test
   parte da solo e dice in chiaro se funziona.

Nessun file di configurazione da scrivere. I default sono buoni: rate limit
prudenti, categorie già mappate, backup automatico attivo.

### 4.2 Aggiungere un indexer

- Ricerca nel catalogo per nome, URL, lingua o tipo.
- **Incolla un URL** e indexarr riconosce il sito e la definizione.
- Form generato dalla definizione: solo i campi che servono, con spiegazione.
- Test immediato diviso per fasi: raggiungibile → login → ricerca → download.
  Se una fase fallisce, il messaggio dice perché e cosa fare.

### 4.3 Si ripara da solo

- **Definizioni aggiornate a caldo**: il catalogo si aggiorna senza
  riavviare e senza aggiornare il binario.
- **Mirror automatici**: se il dominio principale cade, prova i domini
  alternativi noti della definizione.
- **Sessioni**: login rinnovato da solo quando scade.
- **Errori in linguaggio umano**: non "HTTP 403", ma "il sito blocca le
  richieste automatiche: attiva FlareSolverr per questo indexer" con un
  pulsante per farlo.
- **Pacchetto diagnostico** in un clic: log, configurazione e stato, con
  segreti già rimossi, pronto da allegare a una segnalazione.

### 4.4 Studio delle definizioni

Scrivere una definizione non deve richiedere di conoscere YAML:

- l'utente apre il sito dentro lo studio, clicca su una riga di risultati e
  sui campi (titolo, size, seeders, link);
- lo studio propone i selettori, mostra l'anteprima dei risultati estratti e
  genera la definizione;
- la pagina viene salvata come fixture di test, con i dati sensibili rimossi;
- la definizione si condivide con il catalogo della community con un clic.

Chi vuole può sempre modificare il YAML a mano, con validazione in tempo reale.

### 4.5 Ricerca

- Ricerca istantanea sulla cache locale (FTS5), con filtri per categoria,
  qualità, dimensione, data, indexer.
- Comprensione della query: `S01E02`, `1080p`, `2024`, esclusioni, id IMDb.
- Risultati deduplicati con "visto su N indexer" e scelta del link migliore.
- Invio diretto al client di download o alla \*arr.

### 4.6 Aggiornamenti senza paura

- Canali stable e beta; aggiornamento dalla UI dove il sistema lo permette
  (binario standalone, Windows); altrove notifica con le istruzioni.
- Backup automatico prima di ogni migrazione del database.
- Rollback alla versione precedente in un comando.
- Changelog leggibile dentro la UI.

### 4.7 Per chi vuole di più

- CLI completa: `indexarr indexer add|test|list`, `indexarr db check`,
  `indexarr definition test`.
- API REST documentata con OpenAPI, token con permessi.
- Configurazione esportabile e versionabile (senza segreti).

---

## 5. Pilastro 3 — Leggero

### 5.1 Leggero per il dispositivo

| Risorsa | Come la teniamo bassa |
|---|---|
| Memoria | Parsing in streaming, cache con limite, definizioni caricate solo quando servono, limite di memoria del runtime Go impostato in base al dispositivo |
| CPU | Nessun polling attivo: il processo si sveglia solo per i job pianificati e le richieste |
| Disco | Retention configurabile, `.torrent`/`.nzb` deduplicati, vacuum pianificato, dimensione del DB prevedibile e mostrata in UI |
| Avvio | Nessuna inizializzazione pesante; migrazioni solo quando servono |
| Binario | Asset della UI compressi e incorporati; nessun browser incorporato; ogni dipendenza motivata |

### 5.2 Amico dei NAS

- **Dischi a riposo**: scritture raggruppate e checkpoint del WAL pianificati,
  così un NAS con dischi meccanici può spegnerli quando non c'è attività.
- **Modalità basso consumo**: intervalli più lunghi e concorrenza ridotta,
  scelta automaticamente sui dispositivi con poca RAM.
- Funziona con 256 MiB di RAM totale.

### 5.3 Leggero anche verso i siti

Un indexer educato è un indexer che non viene bloccato:

- richieste condizionali (`ETag`, `If-Modified-Since`) dove il sito le
  supporta;
- riuso delle connessioni, compressione;
- cache + live: si interroga il sito solo quando la cache non basta;
- rate limit per host, rispetto di `Retry-After`, nessuna tempesta di retry.

### 5.4 Benchmark pubblico

Le prestazioni si dimostrano. Un benchmark riproducibile, nel repository,
confronta indexarr con Jackett e Prowlarr sullo stesso hardware e con lo
stesso carico simulato (stessi indexer finti, stesse ricerche):

- memoria a riposo e sotto carico;
- CPU a riposo;
- tempo di avvio;
- latenza di ricerca;
- dimensione dell'installazione.

I risultati si pubblicano a ogni rilascio, anche quando non ci sono
favorevoli.

---

## 6. Fondamenta — Parità ed evoluzione continua

Descritte in dettaglio in [indexarr2.md](../indexarr2.md). In sintesi:

- **Parità al 100% alla 1.0**, misurata con una checklist verificabile:
  Torznab/Newznab, app sync, gestione indexer, tutto il catalogo.
- **Niente Cardigann**: tutte le definizioni sono riscritte in un formato
  nativo più robusto. Prima le API ufficiali e i feed, poi i connettori di
  famiglia (un connettore per tutti i siti con lo stesso software), lo
  scraping HTML solo come ultima scelta.
- **Definizioni che resistono**: campi tipizzati, selettori con fallback,
  mirror, flussi a passi espliciti, health check integrato, test obbligatori.
- **Nessun sito perso**: i siti non ancora riscritti passano dal ponte verso il
  Jackett/Prowlarr dell'utente; il ponte si svuota sito per sito.
- **Pipeline a stadi e moduli**: le nuove funzioni si innestano senza toccare
  il nucleo.
- **Torrent e Usenet** nello stesso indexer.

---

## 7. Fondamenta — Fiducia

### 7.1 Libero

indexarr è e resterà software libero. Motore, formato, catalogo delle
definizioni, UI e strumenti sono interamente nostri: la licenza libera la
scegliamo noi.

Perché il catalogo resti nostro, le definizioni si scrivono **osservando i
siti** e la loro documentazione, mai copiando file o codice di Jackett e
Prowlarr. Ogni definizione registra autore, data e fixture di origine. I
contributi della community seguono la stessa regola e accettano la licenza
del progetto.

### 7.2 Sicuro

- Credenziali e cookie cifrati; passkey e API key degli indexer mai esposti
  alle \*arr (proxy dei download).
- Definizioni come dati, mai codice eseguibile; catalogo remoto firmato.
- Protezione SSRF, limiti di dimensione e timeout su ogni richiesta.
- Rilasci firmati, build riproducibili, SBOM pubblicato.
- Processo di segnalazione delle vulnerabilità con tempi di risposta
  dichiarati.

### 7.3 Privato per default

- Nessuna telemetria attiva per default.
- Health check delle definizioni **opt-in**, anonimo, con contenuto
  documentato e visibile prima dell'invio.
- Nessun servizio esterno obbligatorio: funziona completamente offline
  rispetto a noi.

### 7.4 Trasparente

- Roadmap pubblica, decisioni tecniche documentate.
- Matrice di compatibilità (piattaforme, \*arr, client) pubblicata e
  generata dalla CI.
- Stato di ogni definizione visibile: bozza, verificata, stabile, degradata.

---

## 8. Numeri che ci impegniamo a rispettare

| Area | Obiettivo 1.0 | Oggi |
|---|---|---|
| Memoria a riposo, 50 indexer | < 40 MiB | ~30 MiB con 37 sorgenti |
| Memoria di picco, ricerca su 50 indexer | < 80 MiB | da misurare |
| CPU a riposo | ~0% (nessun risveglio fuori dai job) | da misurare |
| Avvio a servizio pronto | < 1 s (DB da 100k release) | da misurare |
| Binario (amd64, ottimizzato) | < 25 MB con motore e UI completi | 15,9 MB |
| Immagine Docker compressa | < 15 MB | da misurare |
| Ricerca in cache p95 | < 50 ms su 100k release | da misurare |
| Fan-out live p95, 10 indexer | < 5 s (limitato dal sito più lento, con deadline) | da misurare |
| Dispositivo minimo | Raspberry Pi Zero 2 W, 512 MiB RAM | da verificare |
| Prima ricerca dopo l'installazione | < 2 minuti per un utente nuovo | da misurare |
| Migrazione da Prowlarr | Un'azione, nessuna riconfigurazione delle \*arr | da fare |
| Parità | Checklist di indexarr2.md §3 tutta verde | da fare |
| Siti dell'utente funzionanti al giorno uno | 100% (nativi o tramite ponte) | da fare |
| Inventario prioritario con definizione nativa verificata | 100% alla 1.0 | da fare |

---

## 9. Come lo garantiamo

Le promesse reggono solo se una regressione blocca il rilascio.

| Promessa | Meccanismo |
|---|---|
| Ovunque | CI con matrice di build per tutti i tier; test di integrazione su Linux, Windows e macOS; smoke test su emulazione arm |
| Compatibilità \*arr | Test di integrazione contro versioni reali delle \*arr in container; matrice pubblicata |
| Catalogo | Test di tutte le definizioni sulle fixture a ogni modifica; canary sui siti pubblici; nessuna definizione senza test |
| Leggero | **Budget di risorse in CI**: memoria, dimensione del binario e dell'immagine, tempo di avvio. Superare il budget fa fallire la build |
| Semplice | Test end-to-end del primo avvio e della migrazione; test di usabilità con utenti nuovi prima di ogni versione maggiore |
| Affidabile | Soak test di 24 ore prima di ogni rilascio; test di migrazione del DB su copie reali |
| Sicuro | Analisi statica e dei pacchetti vulnerabili in CI, fuzzing dei parser |

---

## 10. Grandi idee (oltre la parità)

Cose che oggi nessun indexer fa, o fa bene.

- **Annunci IRC in tempo reale**: molti tracker privati annunciano le nuove
  release su IRC. indexarr le riceve nel momento in cui escono, senza
  interrogare il sito; la cache è aggiornata in secondi invece che a ogni
  intervallo di RSS.
- **Riparazione assistita delle definizioni**: quando un sito cambia layout,
  indexarr confronta la pagina nuova con l'ultima fixture valida e propone i
  nuovi selettori; l'utente conferma con un clic.
- **Rete di salute delle definizioni (opt-in)**: le istanze segnalano in modo
  anonimo quali definizioni funzionano. Un sito rotto si scopre in minuti, non
  quando gli utenti aprono una segnalazione.
- **Ricerca che capisce**: query interpretate (serie, stagione, episodio,
  qualità, lingua), ranking per pertinenza e affidabilità dell'indexer.
- **Regole e automazioni**: filtri salvati che producono feed RSS, notifiche o
  invii automatici ai client.
- **Multi-utente opzionale**: utenti con ruoli e API key separate, per le
  installazioni condivise.
- **Statistiche utili**: quali indexer portano davvero le release che si
  scaricano, quali sono lenti, quali conviene disattivare.

---

## 11. Orizzonti

| Orizzonte | Obiettivo |
|---|---|
| **Ora → 1.0** | Parità completa (indexarr2.md), piattaforme Tier 1, migrazione da Jackett/Prowlarr, budget di risorse in CI, benchmark pubblico |
| **1.x** | Tutti i canali di distribuzione (§3.2), studio delle definizioni, riparazione assistita, Tier 2 completo, traduzioni |
| **2.x** | Annunci IRC, rete di salute delle definizioni, ricerca che capisce, multi-utente |
| **Sempre** | Catalogo nativo che cresce, ponte che si svuota, numeri del §8 rispettati |

---

## 12. Cosa non faremo mai

- Rompere il contratto con le \*arr: Torznab, Newznab, API key e URL
  esistenti restano validi.
- Usare Cardigann o copiare definizioni e codice di altri indexer.
- Eseguire codice scaricato dalla rete dentro le definizioni.
- Incorporare un browser nel binario.
- Risolvere CAPTCHA in modo automatico o aggirare protezioni di accesso.
- Raccogliere dati senza consenso esplicito.
- Diventare un client di download.
- Rendere obbligatorio un servizio nostro per funzionare.
