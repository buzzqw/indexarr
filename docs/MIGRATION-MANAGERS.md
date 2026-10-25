# Migrazione da Jackett/Prowlarr a indexarr

Data: 2026-10-25. Scopo: sostituire i due manager con le sorgenti native
indexarr. indexarr **non crea più proxy Torznab verso Jackett o Prowlarr**:
ogni fonte o è sua, oppure resta in coda e non viene creata.

## Tre vie, nessun ponte

La pagina `/import` legge la configurazione dei manager in sola lettura e
classifica ogni fonte:

| Via | Quando | Cosa crea indexarr |
| --- | --- | --- |
| **Nativa** | esiste una definizione di catalogo con lo stesso id o host | un indexer nativo con quella definizione |
| **Software noto** | la riga del catalogo ha un connettore di famiglia (NexusPHP, UNIT3D, Gazelle, XBTIT, TorrentTrader) | un indexer nativo con la definizione di famiglia e l'URL del sito |
| **Da scrivere** | nessuna delle due | **niente**: la fonte resta nel catalogo con il filtro «da scrivere» |

Il terzo caso non è un errore nascosto: è la coda di lavoro. Non esiste un
quarto caso con un endpoint Torznab del manager. Un indexer che punta a
Jackett/Prowlarr resta possibile solo se l'utente lo aggiunge a mano come
sorgente Torznab, non come risultato dell'import.

## Cosa succede alle credenziali

- **Prowlarr**: i campi `username`, `password` e `apiKey` sono letti e
  sigillati nel vault. Se una definizione nativa li richiede, l'indexer nasce
  già configurato.
- **Jackett**: l'API espone solo nome, URL, categorie e tipo; non restituisce
  le credenziali dei tracker. Una fonte privata senza credenziali viene
  creata **disattivata**, con il motivo «credenziali da inserire». Non si
  aggira il problema con un proxy: si aprono le impostazioni dell'indexer in
  indexarr e si inseriscono una volta.
- Una fonte con definizione in **bozza** è comunque nativa; la UI la marca
  «bozza». Non torna più al ponte.

## Misura reale del catalogo Jackett

Istantanea dei metadati del 23/10, 638 fonti di catalogo (6 configurate):

| Via | Fonti |
| --- | ---: |
| Definizione nativa (id o host) | 19 |
| Software noto (famiglia rilevata) | 155 |
| Da scrivere | 464 |
| **Totale** | **638** |

Le 155 fonti con software noto: NexusPHP 76, UNIT3D 59, XBTIT 12, Gazelle 6,
TorrentTrader 2. La rilevazione viene da un `GET` della home page con marcatori
forti; le pagine ambigue restano senza famiglia.

### Il catalogo completo nell'interfaccia

La pagina `/import` accetta la directory dati di Jackett oppure URL e API key
dell'istanza in esecuzione. Con **Includi il catalogo completo di Jackett**
legge `t=indexers&configured=true` e `configured=false`, unisce le due liste e
mostra tutte le fonti. Ogni riga dichiara la via (nativa, software noto, da
scrivere) e il motivo. Le fonti senza via nativa non sono selezionabili.

Il catalogo dei siti (`/indexers/new`) contiene ora **tutte** le 568 righe
uniche, private comprese, con:

- contatore di copertura in testa: pronte, software noto, da scrivere;
- filtro **Modo** (pronte / software noto / da scrivere);
- 155 righe con un software noto, 413 in coda;
- nessuna riga sbagliata spacciata per «verificata»: le private restano
  `da-provvi` finché non c'è un account.

## Mappatura verificata delle fonti dell'utente

| Jackett | Prowlarr | Definizione indexarr | Stato |
|---|---|---|---|
| Knaben | Knaben | `knaben`, `knaben-search` | verificata |
| LimeTorrents | LimeTorrents | `limetorrents` | verificata |
| The Pirate Bay | The Pirate Bay | `thepiratebay` | verificata |
| TorrentLeech | TorrentLeech | `torrentleech`, `torrentleech-rss` | bozza / sperimentale |
| TorrentsCSV | TorrentsCSV | `torrentscsv` | verificata |
| TorrentGalaxyClone | — | `torrentgalaxyclone` | verificata |
| showRSS | — | `showrss` | verificata |
| ilDraGoNeRo | — | `ildragonegro` | bozza / cookie richiesto |

Le fonti pubbliche dell'utente hanno una definizione nativa. TorrentLeech e
ilDraGoNeRo restano in prova finché non vengono provati con un account; per
TorrentLeech esiste anche il feed personale `torrentleech-rss`, che usa la
rsskey nel vault.

## Ordine di migrazione

1. Aprire `/import`, leggere Prowlarr e Jackett, generare il piano.
2. Selezionare le fonti con via **nativa** o **software noto**. Quelle già
   configurate arrivano attive; le altre arrivano disattivate.
3. Inserire le credenziali mancanti negli indexer privati e abilitarli.
4. Passare le \*arr a indexarr; lasciare Jackett/Prowlarr fermi ma presenti.
5. Lavorare la coda «da scrivere» una famiglia o un sito per volta. Quando una
   definizione è pronta, la fonte si adotta senza toccare le \*arr.

## Controlli dopo la migrazione

- ogni query Gextto deve restituire almeno le stesse release distinte;
- controllare `app_searches` per `go-http-client` e gli errori;
- monitorare coda live, timeout e RAM di indexarr;
- verificare che Sonarr/Radarr continuino a usare solo `indexarr` su `:8484`;
- controllare che non esistano indexer con definizione `torznab-generic`
  diversi da una sorgente Torznab scelta a mano.

## Nota

`indexarr` non incorpora Cardigann. Le definizioni Jackett non vanno copiate:
le sorgenti native si mantengono e si provano singolarmente. La coda «da
scrivere» è il prezzo onesto di questa regola; il ponte non è più la scorciatoia.
