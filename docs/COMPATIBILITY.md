# Compatibilità con gli indexer

indexarr non contiene un parser per ogni tracker: parla **Torznab** per i
torrent e **Newznab** per Usenet. Tutto ciò che espone uno di questi protocolli
funziona senza modifiche. Per il resto indexarr adotta la fonte con una
definizione nativa o con un connettore di famiglia; se non ha né l'una né
l'altro, la fonte resta in coda e **non** viene creato un proxy verso Jackett o
Prowlarr.

## Matrice

| Sorgente | Stato | Note |
| --- | --- | --- |
| **Jackett / Prowlarr** | non supportati | indexarr li sostituisce: gli endpoint dei manager vengono rifiutati. Le fonti si aggiungono dal catalogo nativo. |
| **MirCrew** (`mircrew-indexer`) | completo | Base o `/api`; magnet in link/enclosure; seeder assenti; `/status` per il login al forum; risposte lente. |
| **Torznab diretto** | completo (torrent) | Qualunque URL di un tracker che contiene `/api` o `torznab` è usato così com'è; altrimenti viene aggiunto `/api`. Gli URL di Jackett/Prowlarr sono esclusi. |
| **Newznab / Usenet** | completo (generico) | Connettore `newznab` e definizione `newznab-generic`: caps, ricerca `search`/`tv`/`movie`/`music`/`book`, payload `.nzb` archiviato o risolto al grab, invio ai client SABnzbd/NZBGet. Definizioni native per singoli siti Usenet non ancora in catalogo. |
| **RSS / Atom** | completo | Titolo, magnet, enclosure `.torrent`, dimensione, categoria numerica, data; import massivo e OPML. |
| **ExtTo** | completo (HTML) | Parser dedicato: titolo e magnet dalla stessa riga; categoria dal filtro `c=`. |
| **TorrentGalaxy** | completo (HTML) | Parser dedicato delle pagine `post-detail` con cache dei magnet (6 ore). |
| **Corsaro** | completo (HTML) | Parser dedicato di link `/torrent/`, `/torrents/` e magnet diretti, con cache dei dettagli. |
| **HTTP Basic** | completo | Campi dedicati per sorgente, usati per ricerche e download. |
| **API key** | completo | Inviata come `apikey` e come header `X-Api-Key`; può essere vuota. |
| **Cloudflare / challenge** | con FlareSolverr | Retry automatico per RSS e Torznab diretto; indirizzi LAN esclusi. |
| **API proprietarie** | non gestite in automatico | La fonte resta in coda «da scrivere» finché non esiste una definizione nativa; non compare nell'elenco utente. Nessun proxy automatico verso Jackett/Prowlarr. |

## MirCrew

`mircrew-indexer` è il caso di riferimento di un servizio Torznab "artigianale":
merita l'elenco esplicito di ciò che indexarr fa per lui.

| Comportamento MirCrew | Gestione indexarr |
| --- | --- |
| Endpoint `<base>/api`, porta scelta dall'utente | Accetta sia la base sia l'URL con `/api` |
| Magnet in `<link>` e `<enclosure>` | Riconosciuto, nessun tentativo di scaricarlo via HTTP |
| Nessun attributo `seeders` | Seeder sconosciuti (`-1`), attributo omesso in uscita |
| `<response offset total>` senza namespace | Ignorato, la paginazione usa i parametri della richiesta |
| Data = data del topic del forum | Letta da `<pubDate>` |
| `/status` con `logged_in`, `login_error`, `last_run` | Il pulsante Test la interroga e distingue login fallito, in corso, perso |
| Ricerca lenta sul forum | Timeout predefinito 45 s, run con margine |
| XML con `&` grezzi o entità HTML | Sanitizzazione prima del parsing |

### Configurazione MirCrew

| Campo | Valore |
| --- | --- |
| Name | `MirCrew` |
| Type | Torznab / indexer |
| URL | `http://<host>:<porta>` oppure `http://<host>:<porta>/api` |
| Manager | MirCrew (o Direct Torznab, Auto va bene) |
| API key | quella del servizio, se presente |
| Categories | `5000` per la TV, `2000` per i film |
| HTTP username/password | solo se il servizio li richiede |

Il percorso è stato verificato contro un `mircrew-indexer` reale (versione 0.1):
caps, `/status` con login al forum attivo, ingestione di 100 release dalla lista
"latest" e ricerca Torznab con magnet e infohash.

### Diagnostica

| Sintomo | Significato |
| --- | --- |
| Test: «forum login failed: …» | Credenziali del forum MirCrew da rifare nel servizio; l'indice locale continua a rispondere |
| Test: «forum login in progress» | Servizio appena avviato, non è un errore |
| Test: «forum login lost» | Il servizio aveva completato un giro e ha perso il login |
| Test FlareSolverr fallito | Servizio non raggiungibile, URL errato o nessun accesso a Internet dal solver |
| HTTP 401 | API key o credenziali Basic errate |
| HTTP 404 sul download | Release salvata solo come magnet: usare il link magnet |
| HTML al posto di XML | L'indexer ha restituito una pagina di login o un blocco: controllare URL e credenziali |
| Nessun risultato con query vuota | Il Torznab diretto potrebbe non supportare la lista "latest": impostare un termine di ricerca nella sorgente |

## Liste HTML (ExtTo, TorrentGalaxy, Corsaro)

`extto.org`/`ext.to`, `torrentgalaxy.*` e i siti Corsaro non sono feed: sono
listing HTML. indexarr li riconosce dall'host e usa lo stesso approccio di
Gextto:

| Sito | Come |
| --- | --- |
| ExtTo | Accoppia il link `post-detail` e il magnet presenti nella stessa riga; nessun fetch di dettaglio. |
| TorrentGalaxy | Segue i link `post-detail` delle righe, estrae il magnet dalla pagina e lo mette in cache per 6 ore. |
| Corsaro | Segue i link `/torrent/` o `/torrents/` e i magnet diretti, estrae i magnet in parallelo e li mette in cache. |

Limiti: prima pagina, massimo 50 release per sorgente; se il layout cambia o il
sito richiede il login, il test riporta l'errore. **Quick add** riconosce l'host;
**Default category** assegna una categoria Torznab quando la pagina non la
fornisce. Gli altri siti senza RSS restano nella coda nativa: l'indice li
classifica come «da scrivere» (vista di manutenzione `/indexers/new?queue=1`) e
non crea un endpoint Torznab verso il manager.

## Cloudflare

Con un endpoint configurato in **Settings → Cloudflare / FlareSolverr**, una
risposta con challenge Cloudflare viene ripetuta tramite il servizio:

- header `cf-mitigated: challenge`, oppure pagina HTML con `Just a moment...`,
  `cf-chl`, `__cf_chl`, `Attention Required`, su 403/503 (e su 200);
- RSS, Torznab diretto e test di connessione;
- nessun retry per gli indirizzi LAN: un 403 locale è un errore di
  autenticazione/rete, non una challenge;
- un errore XML/JSON dell'indexer non viene trattato come challenge.

Limiti: i `.torrent` non vengono scaricati tramite FlareSolverr (risposta
binaria) e una risoluzione può richiedere fino a ~60 secondi. L'URL dell'indexer,
API key compresa, viene inviato al servizio FlareSolverr.

## Aggiungere un indexer non in elenco

1. Se espone **Torznab**, creare una sorgente con l'URL completo e la API key (se
   presente). Il tipo *Direct Torznab* evita ogni euristica sul gestore.
2. Se richiede **HTTP Basic**, compilare i campi dedicati.
3. Se parla solo la sua **API proprietaria**, la fonte resta in coda: si scrive
   una definizione nativa osservando il sito. indexarr non crea un proxy verso
   Jackett/Prowlarr, non li contatta e non implementa protocolli ad-hoc.
4. Se il servizio espone un **`/status`** accanto a `/api`, il test lo legge
   automaticamente quando il JSON contiene `ok` o `logged_in`.

Per verificare senza aspettare lo scheduler usare **Test** o **Run** nella pagina
Sources e controllare **Runs** per l'errore esatto restituito dall'indexer.
**Quick add** riconosce il tipo dall'URL; **Discover feeds** trova i feed
dichiarati da una pagina; i preset impostano tipo e manager senza vincolare
l'URL.

## Newznab / Usenet

Il percorso Usenet usa il connettore generico `newznab` e la definizione
`newznab-generic`. Non è una definizione nativa per un singolo sito: l'URL e la
API key li fornisce l'utente, quindi il catalogo non contiene ancora sorgenti
Usenet verificate sito per sito.

| Passo | Comportamento |
| --- | --- |
| Aggiunta | URL dell'API (con o senza `/api`) e API key; il protocollo è `usenet` e viene derivato dalla definizione |
| Caps | `t=caps` letto e messo in cache per un'ora; le modalità dichiarate sono `search`, `tv-search`, `movie-search`, `music-search`, `book-search` |
| Ricerca | parametri Newznab (`q`, `season`, `ep`, `imdbid`, `tvdbid`, `tmdbid`, `rid`, `artist`, `album`, `author`, `title`, `cat`); le voci senza link valido vengono scartate |
| Payload | `.nzb` scaricato con `t=get` o dall'enclosure `application/x-nzb`; se il budget di download è esaurito resta un riferimento sigillato, aperto al grab |
| Output | Torznab/Newznab con `protocol=usenet`, enclosure `application/x-nzb` e nome file `.nzb`; nessun magnet e nessun infohash |
| App | Sonarr, Radarr, Lidarr, Readarr e Whisparr ricevono `Newznab`/`NewznabSettings` con protocollo `usenet`; eD2k resta escluso |
| Download | la release viene inviata al client Usenet (SABnzbd o NZBGet) configurato per il protocollo `usenet` |

Limiti misurati: la ricerca live usa lo stesso fan-out degli altri protocolli e
non scarica l'NZB in anticipo; il payload arriva al primo grab. Le definizioni
native per singoli siti Usenet non fanno parte del catalogo attuale, quindi una
fonte che non espone Newznab resta in coda.

## Client torrent

L'invio ai client non fa parte del protocollo Torznab: è un'azione opzionale del
dettaglio release, configurata in **Settings → Download client**.

| Client | Integrazione |
| --- | --- |
| qBittorrent | Web API v2: login, `torrents/add` con magnet o file `.torrent`, categoria, percorso, pausa. |
| Transmission | RPC `/transmission/rpc`: `torrent-add`, `metainfo` base64 per i `.torrent`, `filename` per i magnet, sessione `409` gestita. |
| aMule | Web API `amuleapi` (`/api/v1`): login con password, `POST /downloads` con il link `ed2k://`. Protocollo `ed2k`. |

Sonarr/Radarr/Lidarr continuano a gestire il download per conto loro; questa
funzione serve per inviare una release scelta a mano dalla UI.
