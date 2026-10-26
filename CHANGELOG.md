# Changelog

## 2.1.0 (in corso)

### Sostituzione di Jackett e Prowlarr
- Rimossi l'import da Jackett/Prowlarr, la pagina `/import`, le API
  `/api/v2/import/*` e il comando `indexarr-import`.
- Rimosso `fresh-start.sh` (era costruito sull'import dei manager).
- Il catalogo nativo è l'unico percorso per aggiungere fonti.
- Gli URL di Jackett/Prowlarr vengono rifiutati alla creazione e alla modifica
  di un indexer.
- Istantanea storica dei metadati mantenuta solo per generare il catalogo;
  nessun collegamento a runtime.

### Prestazioni
- Risposte HTML, CSS, JS e SVG compresse con gzip e file statici con ETag:
  una visita successiva riceve un 304 invece del file intero.
- Normalizzatori dei titoli con tabelle `strings.Replacer` precompilate: meno
  CPU e fino al 76% di allocazioni in meno per pagina Torznab.
- Snapshot delle righe per i feed piccoli, con slice compattate, e finestra
  limitata anche per le query non selettive: la ricerca `1080p` non
  materializza più l'archivio.
- Latenza p95 della query stress a 50.000 release: da ~1.981 ms a ~61 ms;
  RSS dopo il carico: da ~112 MiB a ~66 MiB.
- Cache della provenienza per release (TTL 2 minuti, 1.024 voci): una pagina
  Torznab ripetuta non riesegue il join su `release_sources`.
- Indice composito `(hidden, category, seeders, published_at, id)` per i filtri
  categoria + seeder minimi: il conteggio usa un indice covering.
- Cache bounded (2.048 titoli) dei tratti usati dalla fusione: il passaggio
  regex sui titoli non viene ripetuto a ogni ricerca.
- Conteggio delle release visibili senza predicato (`COUNT(*) - COUNT(hidden)`):
  elimina una scansione dell'indice da ~4,6 ms per ricerca testuale.

### Ricerca
- Ricerca nei nomi dei file dei torrent salvati, con FTS5 trigram.
- Ordinamento per «più visti» (`times_seen`) e «più presi» (`grab_count`).
- Filtro per escludere la categoria XXX.

### Compatibilità dashboard
- API Prowlarr v1 in sola lettura: `system/status`, `health`, `indexer`,
  `indexer/{id}` per Homepage/Organizr.

### App sync
- Mylar3 e LazyLibrarian gestiscono i provider Torznab/Newznab via API comandi:
  elenco, aggiunta, modifica, rimozione. Sette applicazioni su sette.

### Gestione indexer
- Scheda della sorgente: in Panoramica l'indirizzo è sempre visibile, con il
  verdetto «funziona / non funziona», il motivo, l'ultimo errore e il registro
  delle ultime operazioni, comprese le righe del log del processo.
- Le credenziali richieste (utente, password, chiave API) sono in cima al
  modulo, nel riquadro «Accesso al sito»; la scheda dice quali mancano e porta
  direttamente al modulo. Nel catalogo i siti che richiedono un accesso hanno
  il pulsante «Inserisci credenziali».
- Eliminando o disattivando una sorgente si torna al filtro di partenza, per
  esempio «Disattivati».
- In «Aggiungi indexer» tipo e lingua sono elenchi a discesa; i bisogni
  (Film, Libri, …) si sommano e restano evidenziati.
- Il catalogo mostra solo le fonti già utilizzabili: quelle «da scrivere» non
  compaiono più.
- Nelle ricerche, accanto a «Scarica» c'è «Copia magnet».
- Frequenza personale per sorgente visibile nel form (fuori da Avanzate) e
  nell'elenco, con scelte rapide in minuti/ore e il valore effettivo
  («predefinita globale» o «impostata per questa sorgente»).
- «Riduci la frequenza», sul problema 429, raddoppia l'intervallo della sola
  sorgente che ha risposto troppo spesso e conferma il nuovo valore. Le
  ricerche dal vivo delle app non vengono rallentate.
- Proxy HTTP/HTTPS/SOCKS5 con pagina dedicata e API `/api/v2/proxies`.
- Limiti query/grab e seed criteria (seeder minimi, ratio, seed time, season
  pack) nel form, nel DB e nel sync verso le \*arr.

### Catalogo
- Nuove definizioni native pubbliche **DivxTotal**, **Mac Torrents**,
  **nekoBT**, **TheRARBG**, **AniLiberty**, **0Magnet**, **Torrent Kitty**,
  **TorrentProject2**, **RuTracker.RU**, **AniBT**, **Sukebei**,
  **PandaCD**, **Torrentsome**, **Torrenttip**, **UIndex**, **TorrentByte**,
  **BTdirectory**, **EBookBay**, **Sexy-Pics** e **RinTor.NeT**, con fixture
  reali. TorrentByte, BTdirectory e Sexy-Pics passano da FlareSolverr;
  EBookBay usa l'endpoint HTTP perché il certificato HTTPS del dominio non
  corrisponde; RinTor usa una ricerca POST e risolve il magnet dal dettaglio.
- `search.preflight` estrae token CSRF dalla pagina e li inietta nella
  ricerca; Torrentsome e Torrenttip lo usano.
- UIndex è protetto da Cloudflare e passa da FlareSolverr quando configurato.
- Il parser RSS legge le estensioni `torrentUrl` e `fileSize` usate dal feed
  AniBT.
- AniLiberty usa l'API JSON: ricerca dei release e magnet/dimensione
  dall'endpoint torrents.
- La sezione `download` può leggere anche la dimensione dalla pagina di
  dettaglio (`size`), per i listing che non la espongono nella riga.
- Rilevazione del software dal vivo con marcatori forti; 155 fonti con
  software noto.
- Coda «da scrivere» visibile con filtro e contatore di copertura.
- Le fonti con software noto sono sorgenti periodiche native.

## 2.0.0-rc1

Prima release candidata di indexarr 2.

### Fonti e import
- Tutte le fonti gextto: uploader TorrentGalaxy ed ExtTo, Knaben, TorrentLeech RSS.
- Import «da gextto» in sola lettura, ripetibile, senza doppioni; chiavi nel vault.
- Import da Jackett e Prowlarr.

### Doppioni e cache
- Fusione conservativa delle copie prima della paginazione.
- Pagine Torznab piene, `total` costante, nessun buco tra le pagine.
- Pulizia una tantum delle righe TGx senza hash quando esiste la copia hashata.

### Sorgenti memorizzate
- Avviso misurato al quarto sito, contatore di crescita e retention esplicita.
- Liste novità lette per intero, uploader esclusi dalla ricerca dal vivo.

### Ricerca dal vivo
- Worker condivisi per indexer/query, coda limitata, timeout per ricerca.
- ExtTo segue la categoria della richiesta: Movies per i film, TV per le serie.
- Cache dei risultati e memoria dei timeout senza tagliare release.
- Le varianti di titolo che Sonarr manda per lo stesso episodio condividono un
  budget: la prima attende la finestra piena, le successive rispondono appena i
  siti veloci hanno finito. Le ricerche lente continuano in background e le
  loro righe restano nella cache. Severance passa da ~20 s a ~10 s.
- Conteggi di Home, `/healthz` e metriche in cache per 15 s, invalidati dalle
  scritture (rilasci, ricerca app, stato indexer).

### Interfaccia
- Percorso guidato «Aggiungi sorgente», schede per uploader e fonti sperimentali.
- Nella pagina App le tre modalità di collegamento sono schede con margini e
  spaziatura corretti, non più testo appiccicato al bordo del pannello.
- Sette lingue per le parti nuove.

### Limiti noti
- MirCrew nativo e Smartfeed non sono verificati dal vivo (CAPTCHA); restano
  marcati sperimentali.
- La ricerca di Severance con Sonarr resta intorno ai 10 s misurati: le
  varianti sono quattro richieste e la prima attende la finestra piena.
- La prova di durata delle sorgenti memorizzate va completata in produzione.
