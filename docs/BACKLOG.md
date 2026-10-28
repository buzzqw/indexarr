# Lavori da fare

Elenco tenuto aggiornato di ciò che resta aperto sul catalogo e sulle
definizioni. Le voci di stato (`da-provvi`, `bloccata`, `morta`, `degradata`)
vivono in `index.json` e `sites.yaml`; qui c'è solo quello che **non** è già
classificato, con la prova e il modo di verificarlo.

Ultimo aggiornamento: **2026-10-28**.

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
| `ddunlimited` | il forum risponde `200` con la pagina «Informazione» quando il flood control blocca la ricerca. La ricerca live ora usa l'indice locale (TV `t=61463`, film `f=1988` per lettera) e `search.php` resta solo il ripiego; la lista TV è però l'elenco delle segnalazioni, non tutte le release: le serie vecchie ricadono su `search.php`; resta da misurare quante query restano scoperte. Dal 2026-10-27 il confronto è per inizio di parola (prima era per sottostringa: «It» trovava «Hitman» e saltava il ripiego) e le ricerche libri/musica non leggono l'indice. Dal 2026-10-28 una ricerca TV con stagione salta i topic dell'indice che nominano un'altra stagione (anche una ricerca libera come «Bones S06E05», quella della ricerca manuale di Gextto: prima dava 0 eD2k, ora apre «Stagione 6»), i nomi dei file letti dallo script `filearray` non portano più entità HTML (`L&#39;amore`), un file che nomina l'episodio (`Bones.S06E12…`, `Bones - 6x12 - …`) è pubblicato con il solo nome del file (con il topic davanti Gextto leggeva la serie «BONES - Stagione 6 - Bones» e non l'abbinava mai alla serie seguita), e una ricerca libera con `S06E12` o `6x12` usa lo stesso filtro stagione/episodio di `tvsearch` («Fallout 1x01» 0 → 131 titoli, «Bones S06E12» 40 → 74, A/B contro `d7837d9` 119% sulla media di due giri), e se il forum fa cadere la sessione (stesso account entrato da un altro browser: il topic risponde `200` da ospite, senza link) `auth.logged_in` fa rifare il login una volta e rilegge il topic; resta da vedere dal vivo un login doppio nel log quando l'utente entra dal browser | due `search.php` ravvicinate: la seconda contiene «Non ti è permesso di utilizzare il sistema di ricerca» e 0 `a.topictitle`; la pagina `viewforum.php?f=440` produce 25 topic e 49 link eD2k in 69 s; test `TestDDUnlimitedLogsInAgainWhenTheForumDropsTheSession` e `TestIndexSkipsTopicsOfAnotherSeason`; l'elenco TV contiene 6.521 link `a.postlink-local` |
| copertura DDUnlimited | rassegna del 2026-10-28 sui 26 titoli mancanti in Gextto: eD2k solo per 9-1-1, Dark Matter, FBI, FBI: International; 0 per Scrubs S02, New Tricks, I misteri di Murdoch, Walker, Strike. Cause misurate il 2026-10-28: (1) New Tricks e Murdoch non sono nella lista TV `t=61463`, quindi passano da `search.php`; (2) il flood control di `search.php` risponde `200` con «Al momento non ti è permesso fare ricerche»; (3) lo stesso account entrato da un altro posto fa cadere la sessione. Dal 2026-10-28 `search.cache: 6h`, `search.blocked` (errore visibile invece di 0 risultati muti), nuovo login anche dalla pagina di ricerca e niente topic di altre stagioni. Dal vivo dopo l'upgrade (04:10–06:52): 222 ricerche DDUnlimited, 37 ok, 185 errori; 166 erano flood control, ma 20 erano la pagina «Nessun argomento o messaggio con questo criterio di ricerca» e 1 «le seguenti parole sono state ignorate», presi per rifiuti perché usano lo stesso riquadro `#message p`: ora `blocked.contains: "non ti è permesso"`. Le ricerche libere di Gextto per i mancanti («I misteri di Murdoch S13E01», «… S13») mandavano il marcatore a `search.php` con `terms=all` e davano sempre «Nessun argomento»: ora la richiesta usa `query.keywords_title` (titolo senza marcatore) e i filtri stagione/episodio leggono la query completa. Intervallo misurato il 2026-10-28 alle 07:40 con ricerche di parole vere (le parole inventate o troppo comuni non contano per il forum): accettata a 58 s dalla precedente, rifiutata a 55 s, accettata a 70 s; un rifiuto non fa ripartire il conto (le 3 contemporanee delle 06:30:45 passano perché il forum controlla l'ora dell'ultima ricerca prima di aggiornarla). Da qui `search.interval: 65s` con coda in background e `cache: 12h`: le ricerche di un ciclo di Gextto senza turno non vanno più al sito ma in coda (al massimo 55 l'ora), e il ciclo successivo le legge dalla cache. Resta aperto: (a) misurare dal vivo, dopo due cicli di Gextto, quante ricerche DDUnlimited sono «ok» dalla cache rispetto alle 37 su 222 di prima; (b) «Murdoch Mysteries» (titolo inglese, quello di Sonarr) dà 0 perché il forum usa il titolo italiano; (c) Scrubs, Walker e Strike sono da riprovare | `TestDDUnlimitedSearchCacheFloodControlAndGuestSearch` (rifiuto, nessun risultato, marcatore tolto), `TestDDUnlimitedFloodIntervalQueuesTheSearchesWithoutASlot`, `TestKeywordsTitleDropsTheEpisodeMarker`, `TestDropTopicsOfAnotherSeason`; dal vivo: nella cronologia dell'indexer DDUnlimited nessun errore «search refused by the site: Nessun argomento…» e le query dei mancanti senza `S13E01`; conteggio `select status,count(*) from indexer_history where indexer_id=51 and started_at > …` (le «canceled» sono le ricerche in coda) |
| file senza risoluzione | i file `…ITA.DLMux.x264-UBi.mkv` non dicono la risoluzione. Dal 2026-10-28 (Gextto 1.1.2060) Gextto li prende come «🆘 ultima risorsa» quando un episodio mancante non ha nessuna copia nel profilo; resta da valutare se indexarr debba riportare nel titolo la risoluzione del topic | sonda Gextto del 2026-10-28: `FBI.International.3x01.June.ITA.DLMux.x264-UBi.mkv` → `lastresort=true`, le altre 38 copie inglesi scartate; dal vivo: riga «🆘» nel log di Gextto |
| refresh seeders | dal 2026-10-28 `SeedersCandidates` sceglie solo righe `protocol = 'torrent'`: le release eD2k/Usenet non hanno swarm e il refresh rifaceva una ricerca per titolo su DDUnlimited (flood control). Resta da vedere nel log che il refresh non tocchi più la fonte DDUnlimited | `TestSeedersCandidatesSkipReleasesWithoutSwarm`; dal vivo: nessuna richiesta `search.php` di DDUnlimited dal job seeders dopo l'upgrade |
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
