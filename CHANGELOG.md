# Changelog

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
