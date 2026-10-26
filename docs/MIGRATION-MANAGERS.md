# Sostituzione di Jackett e Prowlarr

Data: 2026-10-25. indexarr **sostituisce** i due manager: non li affianca e non
si collega a loro, né in ingresso né in uscita. Non esiste un import da
Jackett/Prowlarr: le fonti si aggiungono dal catalogo nativo.

## Cosa significa

| Collegamento | Stato |
|---|---|
| indexarr → Jackett/Prowlarr (ricerca, download, proxy) | **assente** |
| Jackett/Prowlarr → indexarr | **assente** |
| Import della configurazione dei manager | **rimosso** |
| Catalogo | nativo, embedded nel binario |

Le applicazioni \*arr parlano **solo** con indexarr. Jackett e Prowlarr si
spengono; non restano come fallback.

## Come si migra

1. Aprire `/indexers/new` e scegliere le fonti dal catalogo nativo. Le fonti
   con una definizione o un software noto si configurano lì; le altre restano
   in coda con «da scrivere».
2. Inserire le credenziali delle fonti private. Jackett non le esponeva e non
   c'è un file da copiare: si inseriscono una volta in indexarr.
3. Collegare Sonarr/Radarr/Lidarr a indexarr (app sync o a mano) e rimuovere i
   loro indexer puntati a Jackett/Prowlarr.
4. Fermare e rimuovere Jackett e Prowlarr.

Nessuno di questi passi legge il database o le API dei manager. Se un URL
inserito appartiene a un manager (per esempio
`.../api/v2.0/indexers/.../results/torznab/` o `/<id>/api`), indexarr lo
rifiuta con un messaggio esplicito.

## Copertura nativa (istantanea del 25/10)

Il catalogo contiene 568 righe uniche da una vecchia istantanea dei metadati
pubblici dei manager; non è una connessione viva. Le definizioni e i connettori
sono nostri.

| Via | Fonti |
|---|---:|
| Definizione nativa | 72 (54 verificate, 18 in bozza) |
| Software noto (NexusPHP, UNIT3D, Gazelle, XBTIT, TorrentTrader) | 155 |
| **Native o software noto** | **227 su 613** |
| In coda «da scrivere» | 386 |

Sulle 638 voci dell'istantanea Jackett: 29 definizioni native, 155 software
noto, 184 con via nativa (28,8%); il resto è coda. La differenza con le 403
righe reali è dovuta ai doppioni di host.

## Le fonti dell'utente

| Jackett/Prowlarr | Definizione indexarr | Stato |
|---|---|---|
| Knaben | `knaben`, `knaben-search` | verificata |
| LimeTorrents | `limetorrents` | verificata |
| The Pirate Bay | `thepiratebay` | verificata |
| TorrentLeech | `torrentleech`, `torrentleech-rss` | bozza / sperimentale |
| TorrentsCSV | `torrentscsv` | verificata |
| TorrentGalaxyClone | `torrentgalaxyclone` | verificata |
| showRSS | `showrss` | verificata |
| ilDraGoNeRo | `ildragonegro` | bozza / cookie richiesto |

Sei fonti sono verificate e utilizzabili subito; due richiedono una
configurazione una volta (TorrentLeech: account o rsskey; ilDraGoNeRo:
account/cookie).

## Perché non c'è più il ponte

Un indexer Torznab che punta al manager non è una fonte propria: lascia intatta
la dipendenza che indexarr deve togliere. Per questo:

- una fonte senza definizione o connettore **non viene creata**;
- resta nel catalogo con il filtro «da scrivere» e il motivo visibile;
- quando la definizione è pronta, la fonte si adotta senza toccare le \*arr;
- nessun URL di Jackett/Prowlarr viene accettato come sorgente.

## Controlli dopo la sostituzione

- ogni query delle \*arr deve restituire almeno le stesse release distinte;
- controllare `app_searches` per `go-http-client` e gli errori;
- monitorare coda live, timeout e RAM di indexarr;
- verificare che Sonarr/Radarr continuino a usare solo `indexarr` su `:8788`;
- controllare che non esistano indexer con URL di un manager.

## Nota

`indexarr` non incorpora Cardigann. Le definizioni non si copiano: si scrivono
osservando i siti. La coda «da scrivere» è il prezzo onesto di questa regola.
