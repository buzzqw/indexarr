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
- Normalizzatori dei titoli con tabelle `strings.Replacer` precompilate: meno
  CPU e fino al 76% di allocazioni in meno per pagina Torznab.
- Snapshot delle righe per i feed piccoli e finestra limitata anche per le
  query non selettive: la ricerca `1080p` non materializza più l'archivio.
- Latenza p95 della query stress a 50.000 release: da ~1.981 ms a ~61 ms;
  RSS dopo il carico: da ~112 MiB a ~66 MiB.

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
- Proxy HTTP/HTTPS/SOCKS5 con pagina dedicata e API `/api/v2/proxies`.
- Limiti query/grab e seed criteria (seeder minimi, ratio, seed time, season
  pack) nel form, nel DB e nel sync verso le \*arr.

### Catalogo
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
- Sette lingue per le parti nuove.

### Limiti noti
- MirCrew nativo e Smartfeed non sono verificati dal vivo (CAPTCHA); restano
  marcati sperimentali.
- La ricerca di Severance con Sonarr resta intorno ai 10 s misurati: le
  varianti sono quattro richieste e la prima attende la finestra piena.
- La prova di durata delle sorgenti memorizzate va completata in produzione.
