# Lavori da fare

Elenco tenuto aggiornato di ciò che resta aperto sul catalogo e sulle
definizioni. Le voci di stato (`da-provvi`, `bloccata`, `morta`, `degradata`)
vivono in `index.json` e `sites.yaml`; qui c'è solo quello che **non** è già
classificato, con la prova e il modo di verificarlo.

Ultimo aggiornamento: **2026-10-27**.

## 1. Definizioni da rileggere

Siti vivi che hanno cambiato qualcosa: la definizione resta nel catalogo ma è
`degradata` (non proposta all'utente) finché qualcuno non rilegge la pagina.

| Definizione | Cosa è cambiato | Prova |
| --- | --- | --- |
| `torrentbyte` | Cloudflare non si apre più nemmeno col solver | home `403`, il solver restituisce una pagina `403 Forbidden` da 698 byte; 0 righe |
| `torrentsome` | la ricerca CSRF risponde `400` (funzionava il 25/10) | `POST /topic/index` con `_csrf` + `keywords` → `400`; la home risponde 200 con 108 KB |

Verifica: `go run ./cmd/...` non basta più, serve il test dal vivo della
definizione (reach + ricerca) con il FlareSolverr locale.

## 2. Incoerenze di catalogo

| Voce | Problema | Cosa serve |
| --- | --- | --- |
| `rintor` | la definizione funziona su `www.rintor.net` (POST `/`), la riga in coda usa `rintor.org`, che serve un tema diverso e la pagina di login | decidere se `rintor.org` è un mirror (allora va riletta e adattata) o un altro sito (allora la riga va lasciata com'è e la definizione resta su `.net`). Oggi la riga resta in coda per un errore di host, non di codice |

## 3. Definizioni lente

| Definizione | Perché | Cosa serve |
| --- | --- | --- |
| `btdirectory` | `download.resolve_on: search` apre fino a 4 pagine di dettaglio durante la ricerca: supera i 90 s | valutare `resolve_on: grab`, oppure ridurre `max_concurrent`, misurando prima/dopo |
| `mixtapetorrent` | stesso schema, 45-55 s per la ricerca | come sopra |

## 4. In attesa di credenziali

Nessuna di queste si può verificare senza un account: lo stato `da-provvi` è
quello giusto e non va forzato a `bloccata`.

| Gruppo | Fonti | Nota |
| --- | ---: | --- |
| Semi-private | **57** | 52 dichiarano `username`/`password` (o cookie), 4 mostrano la pagina di login (AniDUB, Erai-Raws, RuTracker.org, UniOtaku), 1 è un sito di serie senza torrent da ospite (LostFilm) |
| Private | **439** | raggiungibili o con login, nessuna prova possibile senza account |
| Bozze private | **11** | definizioni da completare, servono le impostazioni |

Con un account il percorso è: sessione (vault), osservazione di ricerca,
dettaglio e grab, fixture reali, definizione `verificata`.

## 4-bis. eD2k

| Voce | Cosa è aperto | Prova / verifica |
| --- | --- | --- |
| `ddunlimited` | il forum risponde `200` con la pagina «Informazione» quando il flood control blocca la ricerca. La ricerca live ora usa l'indice locale (TV `t=61463`, film `f=1988` per lettera) e `search.php` resta solo il ripiego; la lista TV è però l'elenco delle segnalazioni, non tutte le release: le serie vecchie ricadono su `search.php`; resta da misurare quante query restano scoperte. Dal 2026-10-27 il confronto è per inizio di parola (prima era per sottostringa: «It» trovava «Hitman» e saltava il ripiego) e le ricerche libri/musica non leggono l'indice | due `search.php` ravvicinate: la seconda contiene «Non ti è permesso di utilizzare il sistema di ricerca» e 0 `a.topictitle`; la pagina `viewforum.php?f=440` produce 25 topic e 49 link eD2k in 69 s; l'elenco TV contiene 6.521 link `a.postlink-local` |
| `gextto` | il parser Torznab legge magnet e `.torrent`, non il `<link ed2k://>` né l'attributo `ed2k`: le release eD2k arrivano a indexarr ma il client non le mostra ancora come rete eD2k | `POST /api/v2/indexers/{id}/search` restituisce `ed2k` e `ed2k_hash`; il client deve mapparli su `models.Release.Ed2kLink` |
| aMule | client HTTP aggiunto (`kind: amule`, `POST /api/v1/downloads`); il test unitario usa un server finto, la prova dal vivo con l'aMule dell'utente è da fare | `POST /api/v2/indexers/{id}/test` e un grab verso il client configurato |

## 5. Da monitorare

| Voce | Cosa guardare |
| --- | --- |
| `torrentby` | usa `meta.insecure_tls: true` perché il server non manda l'intermedio Let's Encrypt `YE1`. Se la catena torna completa, togliere il flag (e il badge) |
| `u3c3` | la ricerca dipende dal token fisso `search2` del sito: se cambia, la ricerca risponde la home invece dei risultati |
| `rutracker` | la ricerca passa da una sessione FlareSolverr: se il sito smette di rispondere anche al browser, la definizione va degradata |

## 6. Coda

| Coda | Fonti |
| --- | ---: |
| Pubbliche da scrivere | **0** |
| Pubbliche bloccate / morte | 12 / 3 |
| Bozze pubbliche | 12 |
| Semi-private da provare | 57 |
| Private da provare | 439 |

Come ricontrollare i numeri: `go generate ./internal/catalog` e poi
`docs/CATALOG.md` (tabella "Numeri al 2026-10-27") più la vista di
manutenzione `/indexers/new?queue=1`, che mostra la composizione della coda e
la ragione di ogni riga bloccata.
