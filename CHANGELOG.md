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

### Interfaccia
- Percorso guidato «Aggiungi sorgente», schede per uploader e fonti sperimentali.
- Sette lingue per le parti nuove.

### Limiti noti
- MirCrew nativo e Smartfeed non sono verificati dal vivo (CAPTCHA); restano
  marcati sperimentali.
- La ricerca di Severance può richiedere più richieste di Sonarr e ~14–20 s.
- La prova di durata delle sorgenti memorizzate va completata in produzione.
