# Migrazione da Jackett/Prowlarr a indexarr

Data: 2026-10-25. Scopo: sostituire i due manager con le sorgenti native indexarr.

## Mappatura verificata

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

Non manca nessuna sorgente pubblica: tutte hanno una definizione nativa in
`internal/catalog/definitions`. Le uniche non verificabili senza account sono
TorrentLeech e ilDraGoNeRo.

## Ordine di migrazione

1. In Gextto, lasciare attivo `indexarr` e disattivare `prowlarr`.
2. Dopo una finestra di osservazione, disattivare `jackett`.
3. Non cancellare le configurazioni Gextto: restano per rollback.
4. Tenere i container Jackett/Prowlarr fermi ma presenti.
5. In indexarr abilitare `TorrentsCSV` se serve la ricerca pubblica generica.
6. TorrentLeech e ilDraGoNeRo restano sperimentali finché non vengono provati
   con un account; non sono necessari per la migrazione delle fonti pubbliche.

## Controlli dopo la migrazione

- ogni query Gextto deve restituire almeno le stesse release distinte;
- controllare `app_searches` per `go-http-client` e gli errori;
- monitorare coda live, timeout e RAM di indexarr;
- verificare che Sonarr/Radarr continuino a usare solo `indexarr` su `:8484`.

## Nota

`indexarr` non incorpora Cardigann. Le definizioni Jackett non vanno copiate:
le sorgenti native vanno mantenute e provate singolarmente.
