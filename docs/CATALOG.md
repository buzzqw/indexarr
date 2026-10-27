# Catalogo dei siti

Il catalogo ha due livelli, con scopi diversi.

| Livello | Cosa contiene | Dove vive | Stato |
| --- | --- | --- | --- |
| Definizione nativa | Istruzioni complete di ricerca, categorie, login e download di un sito | `internal/catalog/definitions/*.yaml` | `verificata`, `bozza`, `stabile`, `degradata` |
| Voce leggera | Nome, URL, tipo, lingua, categorie, famiglia e stato di un sito | `internal/catalog/sites.yaml` | `verificata`, `da-provvi`, `morta` |

Le voci leggere derivano da un'**istantanea storica dei metadati pubblici**
dei manager: nome, URL, privacy, lingua, categorie e, quando la pagina del sito
lo dimostra, la famiglia software. Non sono una connessione: indexarr non
interroga Jackett o Prowlarr a runtime e non ne importa la configurazione.
Non vengono mai generate da una definizione Cardigann.

## Numeri al 2026-10-27

Valori contati su `index.json` dopo `go generate ./internal/catalog`.

| Misura | Valore |
| --- | ---: |
| Totale voci nel catalogo (definizioni + voci leggere) | **627** |
| Definizioni native | 110 (8 aiutanti generici inclusi) |
| Voci leggere | **517** (512 dall'istantanea Jackett, 5 manuali) |
| Verificate | **84** |
| Bozze | **24** |
| Da provare (`da-provvi`) | **496** |
| Bloccate (`bloccata`) | **14** |
| Morte (`morta`) | **7** |
| Pubbliche verificate (host unici) | **92** |
| Voci italiane | **14** |

Le pubbliche verificate sono conteggiate per host unico, quindi una
definizione nativa con un mirror conta due host. Le voci leggere private
restano tutte `da-provvi`: senza un account non si può verificare la ricerca.

### eD2k: DDUnlimited

`ddunlimited` è la prima definizione `protocol: ed2k`: il forum phpBB italiano
pubblica `ed2k://|file|...|/` e solo una minoranza di topic ha anche magnet o
`.torrent`. Il 27/10 sono stati provati dal vivo login (FlareSolverr), ricerca,
lettura di una pagina di forum e grab; una pagina di «Movies · High Definition»
ha prodotto 53 release in 69 s, 49 con link eD2k, e un topic di stagione è
stato espanso in un file per episodio. I pack di stagione nascondono i link in
un array JavaScript (`filearray[...]`): l'estrattore `body: true` li legge tutti
e li deduplica per MD4; verificato sul topic di «The Last of Us» stagione 1
(10 episodi, S01E01 da 15.928.363.670 byte). La ricerca del forum ha un flood control
(«attendi qualche minuto»): la ricerca live usa un **indice locale** invece di
`search.php`, l'elenco alfabetico delle serie (`t=61463`, 6.521 link) e le 27
pagine «Lista Titoli Globale» dei film (`f=1988`, una per lettera). Il motore
legge la pagina TV e quella della lettera cercata, confronta il titolo in
locale e apre solo i primi 5 topic; `search.php` resta il ripiego. La lista TV
è l'elenco delle **segnalazioni**, non comprende tutte le release: per le serie
vecchie o non segnalate il ripiego è la ricerca del forum (con il suo flood
control), mentre la lista film `f=1988` è completa per lettera. L'archivio
periodico legge quattro liste (Movies HD, Movies UHD, SerieTV HD ITA, SerieTV
UHD), due pagine da 25 topic ciascuna, e non la ricerca. Il protocollo eD2k è escluso dalla sincronizzazione con Sonarr,
Radarr e gli altri programmi Servarr; il client aMule (`kind: amule`) riceve i
link dalla Web API.

Il catalogo ora contiene **tutte** le fonti dell'istantanea, comprese le
private senza una famiglia riconosciuta. Una fonte senza via nativa resta
nell'indice come **da scrivere** e non produce alcun proxy; non compare
nell'elenco utente.

### Cosa vede l'utente, cosa tiene il sistema

L'elenco «Aggiungi indexer» mostra solo le fonti che già funzionano: le
definizioni native `verificata`/`stabile` e i connettori generici. Le bozze, le
voci `da-provvi`, le `bloccata` e le `morta` restano in `index.json` e in
`sites.yaml`, perché servono alla manutenzione, ma non vengono proposte.

La coda non viene cancellata: viene classificata dallo stato. Per rivederla
tutta c'è la vista di manutenzione `/indexers/new?queue=1`, non linkata dalla
UI, che mostra anche i badge «Da scrivere», «In prova», «Problemi» e
«Bloccato».

L'elenco di ciò che resta aperto, con la prova e il modo di verificarlo, è in
[BACKLOG.md](BACKLOG.md).

La testata della vista di manutenzione riassume la composizione della coda
(`Queue: N to write, N drafts, N blocked, N dead`, tradotta nelle 7 lingue) e
ogni riga `bloccata` o `morta` porta la ragione misurata presa da
`metadata/blocked.yaml` (`note` in `index.json`), così la prova è visibile
senza aprire il manifest. Nell'elenco utente né il conteggio né la ragione
compaiono: la classe `.site-note` è resa solo nella coda.

| Stato | Significato | Visibile all'utente |
| --- | --- | --- |
| `verificata`, `stabile` | pronto e provato dal vivo | sì |
| `bozza` | definizione da finire o da verificare | no |
| `degradata` | definizione che funziona con problemi, da migliorare | no |
| `da-provvi` | fonte da provare o definizione da scrivere | no |
| `bloccata` | non gestibile per un motivo che non è codice: fuori coda | no |
| `morta` | indirizzo sparito: fuori coda | no |

### Copertura nativa dell'istantanea Jackett

| Via | Fonti | Note |
| --- | ---: | --- |
| Definizione nativa (id o host) | **126** | fonti dell'istantanea il cui host coincide con una definizione nativa |
| Software noto (connettore di famiglia) | **154** | NexusPHP 76, UNIT3D 59, XBTIT 12, Gazelle 6, TorrentTrader 2 |
| Da scrivere | **338** | 282 private, 56 semiprivate |
| Fuori coda (bloccate o morte) | **20** | 13 bloccate, 7 morte |
| **Totale istantanea Jackett** | **638** | metadati del 23/10 |

Il conteggio è per `source_id` dell'istantanea: una fonte il cui host coincide
con una definizione nativa non ha una riga in `sites.yaml` e conta come nativa.
Le 126 fonti coperte sono più delle definizioni perché una definizione può
coprire più id della stessa istantanea. La somma non include le 6 fonti già
configurate in Jackett quando coincidono con una definizione nativa.

### Fonti dei manager assenti dall'istantanea

Il 27/10 l'elenco attuale di Jackett e lo schema di Prowlarr (solo `GET`) sono
stati confrontati con il catalogo per nome e host. Mancavano cinque fonti
torrent, ora voci leggere in `metadata/manual.yaml` con la prova del `GET`:

| Voce | Tipo | Stato | Nota |
| --- | --- | --- | --- |
| Anidex (`mg-anidex`) | pubblica | da provvi | indexer nativo di Prowlarr; il sito risponde con un controllo DDoS-Guard |
| BinaryBay (`mg-binarybay`) | privata | da provvi | API key; famiglia non dimostrata |
| Pornorip (`mg-pornorip`) | privata | da provvi | login con cookie |
| RevolutionTT (`mg-revolutiontt`) | privata | da provvi | indexer nativo di Prowlarr |
| SAPIbr (`mg-sapibr`) | privata | da provvi | UNIT3D dal titolo della pagina di login |

Restano fuori, di proposito, Magnetico e BitMagnet (DHT locali, vedi sotto),
il Torznab generico, TorrentPotato e il feed RSS generico di Prowlarr, che in
indexarr sono già tipi di sorgente e non siti.

### Coda pubblica: esito del 27/10

Quindici fonti pubbliche della coda hanno ora una definizione nativa
verificata dal vivo (ricerca reale e, dove serve, grab del magnet o del
`.torrent`) con fixture reali: TorrentDownload, MagnetDownload, BlueRoms,
XXXClub, Free JAV Torrent, PornXLab, BigFANGroup, Catorrent, SkidrowRepack,
PC-torrent, Zamunda.LIFE, MyPornClub, PornoTorrent, MixtapeTorrent e sosulki.
`animetosho.net` è un mirror della definizione AnimeTosho.

Nello stesso giorno è stato ripreso il gruppo Cloudflare: cinque nuove
definizioni (BT.etree, ExtraTorrent.st, kickasstorrents.to, Magnet Cat,
DaMagNet) e il mirror `kickass.ws` sono descritti nella sezione
[Cloudflare](#cloudflare-esito-del-2710).

Le sette fonti non gestibili per un motivo che non è codice sono uscite dalla
coda e sono `bloccata` (52BT è `morta`); la prova è in
`metadata/blocked.yaml`.

### Secondo giro sulla coda: esito del 27/10

Lo stesso giorno le undici fonti pubbliche rimaste sono state riprese una per
una, con una GET reale e, dove la pagina risponde, il grab. Quattro hanno ora
una definizione nativa:

| Fonte | Ricerca | Grab |
| --- | --- | --- |
| E-Hentai | GET `/?f_search=` | `.torrent` da `gallerytorrents.php`: 200 `application/x-bittorrent`, 18212 byte |
| XXXTor | GET `/b.php?search=` attraverso FlareSolverr | magnet nella riga |
| U3C3 | GET `/?search2=<token fisso>&search=` | magnet e `.torrent` nella riga |
| torrent.by | GET `/search/?search=`, con `meta.insecure_tls` | magnet nella riga (100 magnet su 100 righe) |

Cinque sono uscite dalla coda come `bloccata` e due come `morta`, con la prova
misurata in `metadata/blocked.yaml`:

| Fonte | Stato | Motivo misurato |
| --- | --- | --- |
| FileMood | `bloccata` | la challenge Cloudflare non si apre sulla pagina di ricerca: tre tentativi via solver restituiscono `One moment...` (3620 byte) |
| VSTHouse | `bloccata` | non è un indexer BitTorrent: le pagine offrono un download diretto uCoz, 0 magnet e 0 `.torrent` |
| EpubLibre | `bloccata` | non è un indexer BitTorrent: il catalogo serve EPUB, 0 magnet e 0 `.torrent` |
| torrent-pirat | `bloccata` | portale phpBB senza magnet; `/tracker/` resta una challenge Cloudflare vuota (718 byte) |
| Anidex | `bloccata` | DDoS-Guard `403` al client, `Error 502` del origin attraverso il solver |
| OpenSharing | `morta` | timeout su IPv4 e IPv6, il solver risponde `500` |
| AniSource | `morta` | nessun record A, connessione rifiutata su 443 e 80 |

**torrent.by** merita una nota a parte: il server invia una catena TLS
incompleta (manca l'intermedio Let's Encrypt `YE1`) e sia
Go sia `openssl` rifiutano il certificato, quindi la definizione usa
`meta.insecure_tls: true`. L'eccezione vale solo per quella fonte pubblica, il
trasporto condiviso non viene toccato, e la UI mostra il badge «TLS non
verificato» con l'avviso nel form. La ricerca è stata provata dal vivo
(`/search/?search=1080p`, 100 righe con magnet, dimensione, data e semi) e il
grab restituisce il magnet della riga.


| Bloccata | Motivo misurato |
| --- | --- |
| MejorTorrent | `.torrent` dietro Cloudflare; il solver restituisce solo testo, la ricerca funziona e il grab no |
| OneJAV | come MejorTorrent |
| Torrent[CORE] | `/search` vuoto (40 byte), handle opachi, nessun magnet |
| DonTorrent | download con proof-of-work JavaScript |
| Byrutor | `.torrent` dietro captcha; il link non restituisce il file nemmeno via solver |
| HDRTorrent | la pagina non espone magnet né `.torrent`, rimanda a archive.org |
| World-torrent | il dominio serve un altro portale (Darkfox) |
| FileMood | la challenge Cloudflare non si apre sulla ricerca (3 tentativi via solver) |
| VSTHouse | non è un indexer BitTorrent: download diretto uCoz, nessun magnet né `.torrent` |
| EpubLibre | non è un indexer BitTorrent: il catalogo serve EPUB |
| torrent-pirat | portale phpBB senza magnet; `/tracker/` dietro challenge Cloudflare |
| Anidex | DDoS-Guard `403` e origin `502` attraverso il solver |
| comicat | ricerca dietro un visitor test JavaScript (`captcha/addon.php`), 1132 byte senza righe |
| miobt | come comicat: stesso visitor test, stesso corpo da 1132 byte |
| OpenSharing (`morta`) | timeout IPv4/IPv6, solver `500` |
| AniSource (`morta`) | nessun record A, connessione rifiutata |
| 52BT (`morta`) | HTTP 410 «The address is dead.» |

### Cloudflare: esito del 27/10

La riga precedente diceva «non verificabili senza FlareSolverr». La prova del
27/10 dice il contrario: con il FlareSolverr locale attivo gli **undici** siti
pubblici che il 25/10 avevano risposto `403 "Just a moment..."` si risolvono
tutti. Cinque hanno ora una definizione nativa nuova (BT.etree, ExtraTorrent.st,
kickasstorrents.to, kickass.ws, Magnet Cat), tre erano già nativi e restano
MejorTorrent, OneJAV e Torrent[CORE]. A questi si aggiunge DaMagNet, che aveva
la home 200 ma il dettaglio dietro challenge e ora è nativo anche lui.

| Fonte | Ricerca | Grab |
| --- | --- | --- |
| BT.etree | FlareSolverr | `.torrent` diretto: `download.php` risponde 200 `application/x-bittorrent` anche senza solver |
| ExtraTorrent.st | FlareSolverr | magnet nella riga |
| kickasstorrents.to | FlareSolverr | magnet nella riga |
| kickass.ws | FlareSolverr | magnet nella riga (mirror della definizione Kickass) |
| Magnet Cat | FlareSolverr | infohash nel link `/hash/<40-hex>.html` |
| DaMagNet | FlareSolverr | magnet nella pagina di dettaglio |

Le definizioni non usano una sessione persistente: il solver a una richiesta
basta per la ricerca e lascia il grab dei magnet al transport normale. Dove il
sito protegge anche il file, il grab non funziona e la definizione non viene
scritta a metà.

Restano tre, ora `bloccata` e fuori coda, per motivi misurati il 27/10:

- **MejorTorrent e OneJAV**: la challenge copre anche il `.torrent` (`403
  text/html` sia a un client normale sia ripetendo i cookie `cf_clearance` del
  solver con lo stesso User-Agent). Cloudflare lega il lasciapassare al
  fingerprint TLS del browser, che curl/Go non riproducono, e FlareSolverr non
  restituisce corpi binari: il grab non è possibile.
- **Torrent[CORE]**: `/search` risponde un corpo vuoto di 40 byte e le pagine
  usano handle base64 opachi senza magnet. Non c'è una ricerca HTML da
  descrivere.

### Semi-private: censimento del 27/10

Le 57 fonti semi-private rimaste in coda sono state passate una per una con una
richiesta reale, leggendo la definizione storica di Jackett **solo come aiuto
di scrittura** per conoscere il percorso di ricerca (nessuna definizione
Cardigann viene importata o distribuita). L'esito è netto e non dipende dal
codice:

| Esito | Fonti | Dettaglio |
| --- | ---: | --- |
| Serve un account | **52** | la definizione storica dichiara `username`/`password` (o cookie): senza credenziali non c'è nulla da provare |
| Pagina di login | **4** | AniDUB, Erai-Raws, RuTracker.org, UniOtaku: `type="password"` sulla pagina, ricerca non disponibile da ospite |
| Visitor test JavaScript | **2** | comicat e miobt: corpo da 1132 byte che carica `captcha/captcha.js` e posta su `/addon.php`; ora `bloccata` |
| Serie TV senza torrent da ospite | **1** | LostFilm: `/search/?q=` risponde 200 ma elenca serie, i torrent richiedono l'accesso |

Nessuna di queste può essere verificata senza un account, quindi restano
`da-provvi`: è lo stato giusto per una fonte raggiungibile ma non dimostrabile.
Non vanno marcate `bloccata`, perché il motivo non è il sito ma l'assenza delle
credenziali.

Un secondo giro ha provato comunque la ricerca da ospite su tutte e 57, anche
dove la definizione storica dichiara credenziali. Sei fonti dietro Cloudflare
(helltorrents, kinozal, seatracker/Marine Tracker, musebootlegs, MVGroup Forum,
UzTracker) superano la challenge con FlareSolverr — 41-204 KB di pagina vera —
ma rispondono con un form di login; le altre restituiscono direttamente la
pagina di accesso. Nessuna fonte semi-private espone righe con magnet da
ospite, quindi il blocco non è tecnico: serve un account.

Se un account arriva (vault dell'indexer), il percorso è quello solito: si
osservano ricerca, dettaglio e grab con la sessione, si salvano le fixture e si
scrive la definizione. Senza, l'unica cosa onesta è lasciarle in coda.

### Salute delle definizioni: prova dal vivo del 27/10

Ogni definizione con una sezione `health` è stata eseguita davvero (reach +
auth + ricerca) con il FlareSolverr locale. Su **109** definizioni:

| Esito | Definizioni | Dettaglio |
| --- | ---: | --- |
| Ricerca riuscita | **69** | contando `rutracker` dopo la correzione |
| Nessuna sezione `search` | **20** | definizioni feed/latest o solo API: la ricerca non fa parte del contratto, non sono errori |
| Servono impostazioni | **16** | private o parametriche (`username`, `cookie`, `url`): senza non si aprono |
| Lente ma funzionanti | **2** | `btdirectory` e `mixtapetorrent` risolvono il dettaglio durante la ricerca e superano i 90 s; le pagine hanno 20 righe e 10 risultati |
| Non funzionanti | **2** | `torrentbyte` e `torrentsome`, ora `degradata` |

Le due regressioni misurate:

- **torrentbyte**: la home risponde `403` anche attraverso il solver, che
  restituisce una pagina `403 Forbidden` da 698 byte; la ricerca non produce più
  righe. La definizione resta nel catalogo come `degradata` finché qualcuno non
  rilegge il sito.
- **torrentsome**: il `POST /topic/index` con `_csrf` e `keywords` ora risponde
  `400` (funzionava il 25/10); il sito è vivo (108 KB sulla home), è cambiata la
  ricerca. Anche questa è `degradata`.

Corretta invece **rutracker**: il dominio risponde `403` a un client normale ma
la ricerca è leggibile da un browser (112 KB, 27 magnet). La definizione ora
dichiara una sessione FlareSolverr (`name_prefix: indexarr-rutracker`) e il test
dal vivo passa in 4,1 s.

## Stati onesti

- **`verificata`** (voce leggera): è stato fatto un `GET` reale sulla home page,
  il sito ha risposto 2xx/3xx con una pagina vera e non è un login o una
  schermata anti-bot. La prova è in `metadata/verification.yaml`, con data,
  HTTP, byte e titolo. Non significa che la ricerca sia stata provata: per
  quello servono una definizione nativa e la sua fixture.
- **`da-provvi`**: nessuna prova, oppure sito privato raggiungibile ma senza
  account, oppure `403`/`5xx`/login/anti-bot. È il badge giusto per un privato
  che non è stato provato dall'interno.
- **`bloccata`**: il sito non è implementabile per un motivo che non è una riga
  di codice mancante (captcha, proof-of-work, binario dietro Cloudflare,
  ricerca inesistente, portale diverso). La riga resta nel catalogo con la
  prova e il motivo in `metadata/blocked.yaml`, ma è fuori dalla coda
  `da-provvi`, da `proposable: false` e non viene mostrata all'utente.
- **`morta`**: il `GET` è fallito perché il nome non risolve o la connessione
  è stata rifiutata. La riga resta nel catalogo con la prova del fallimento,
  ma `proposable: false` e non deve essere proposta. Un timeout, una rotta
  IPv6 irraggiungibile o un errore TLS sono **inconcludenti**: la voce resta
  `da-provvi` e viene riprovata, perché non dimostrano che il sito sia morto.
- **`bozza`** (definizione nativa): definizione scritta ma non ancora provata
  dal vivo. Per `ilDraGoNeRo` significa anche che serve un account.

Nessuna voce privata è `verificata` in questo ciclo. Nessuna voce è stata
marcata viva senza un `GET` registrato.

## Famiglie coperte

Le famiglie sono registrate con una prova nella pagina del sito, nel nome
stesso del manager o in un marcatore forte osservato dal vivo (per esempio
`nexusphp`, `unit3d`, `xbtit`, `gazelle`, `torrent trader`). I marcatori
generici condivisi da più software (`getrss.php`, `ajax.php?action`,
`download.php?id=`) non sono più usati: una famiglia sbagliata è peggio di una
fonte in coda. Se una pagina mostra due famiglie, la voce resta senza famiglia
finché non viene osservata meglio.

| Famiglia | Voci | Esempi e prova |
| --- | ---: | --- |
| UNIT3D | 59 | HDTorrents.it, ShareIsland, ItaTorrents, JME-REUNIT3D, HD-UNiT3D e altre rilevate dal vivo; `unit3d-generic` |
| Gazelle | 6 | GazelleGames e altre rilevate dal vivo; `gazelle-generic` |
| NexusPHP | 76 | SewerPT, HDU, HHanClub, OpenCD, HDSky, CHDBits, PterClub, …; `nexusphp-generic` |
| TorrentTrader | 2 | The Torrent Trader (`takelogin.php`, logo); `torrenttrader-generic` |
| XBTIT | 12 | il CorSaRo Blu, ArabaFenice, Girotorrent, Fenyarnyek e altre; `xbtit-generic` |
| Torznab | 4 | MirCrew (ponte locale), Torlock, TorrentFunk; `torznab-generic` |
| custom | 2 | ExtTo (HTML pubblico), ilDraGoNeRo (forum phpBB) |

## Lista italiana

`italian: true` è un segnale esplicito per il filtro rapido «Italiani»,
indipendente dalla lingua dichiarata dal sito. Include la lista prioritaria dei
siti italiani.

| Sito | Famiglia | Stato | Note |
| --- | --- | --- | --- |
| il CorSaRo Blu (`corsaro`) | XBTIT | verificata | Definizione nativa; lista ospite e magnet verificati, ricerca ospite disabilitata dal sito |
| ItaTorrents (`jk-itatorrents`) | UNIT3D (probable) | da provvi | Privato; login 200, `/api/torrents` 401; nessun account |
| ArabaFenice (`jk-arabafenice`) | XBTIT | da provvi | Privato; login xbtitFM 200; nessun account |
| FutureTorrent (`jk-futuretorrent`) | — | da provvi | Privato; famiglia non dimostrata dal HTML (TSSE), nessun account |
| HDTorrents.it (`jk-hdtorrentsit`) | UNIT3D | da provvi | Privato; login UNIT3D 200; nessun account |
| ShareIsland (`jk-shareisland-api`) | UNIT3D | da provvi | Privato; login UNIT3D 200; nessun account |
| Girotorrent (`jk-girotorrent`) | XBTIT | da provvi | Privato; footer xbtitFM 4.2.14; nessun account |
| TrackerZero (`jk-trackerzero`) | — | da provvi | Privato; frontend SvelteKit, famiglia non dimostrata |
| ilDraGoNeRo (`ildragonegro`) | custom (phpBB) | bozza | Privato; definizione nativa in prova, serve un account |
| MirCrew (`mircrew`) | Torznab | da provvi | Ponte locale del blocco B (`localhost:9118`), non un tracker pubblico |
| ExtTo (`extto`) | custom (HTML) | verificata | Definizione nativa pubblica, fixture e parità verificate |

**UNIT3D:** HDTorrents.it, ShareIsland e, con confidenza `probable`,
ItaTorrents. MirCrew non è UNIT3D: è un servizio Torznab locale.

## Come si rigenera

Tutto è GET, tranne la scrittura dei file generati.

```sh
# 1. Prova live di tutto il catalogo, private comprese. Solo GET sulla home
#    page, nessun login, nessuna scrittura sui siti (-all). Non contatta
#    Jackett o Prowlarr: legge solo i metadati già committati.
go run ./internal/catalog/genverify -all -workers 16 -timeout 10s

# 2. Rigenera sites.yaml e index.json (non tocca la rete)
go generate ./internal/catalog

# 3. Controlli
make def-validate
go test ./...
```

`verification.yaml` è scritto da `genverify`; `gensites` legge l'istantanea,
le famiglie, le prove e le voci manuali e produce `sites.yaml`; `genindex`
unisce definizioni native e voci leggere in `index.json`. L'istantanea
`metadata/jackett.json` è congelata: non c'è più un comando che contatta i
manager.

### File

| File | Sorgente | Modificabile a mano |
| --- | --- | --- |
| `metadata/jackett.json` | istantanea storica | no |
| `metadata/prowlarr.json` | istantanea storica | no |
| `metadata/families.yaml` | curatela con prova GET | sì, aggiungendo la prova |
| `metadata/verification.yaml` | `genverify` (GET) | no |
| `metadata/blocked.yaml` | curatela con prova misurata | sì, aggiungendo la prova |
| `metadata/manual.yaml` | lista prioritaria | sì |
| `sites.yaml` | `gensites` | no |
| `index.json` | `genindex` | no |

## Metadati per la UI

`index.json` espone per ogni riga:

- `italian` — filtro rapido «Italiani»;
- `usage` — numero di configurazioni osservate nei manager (Jackett
  `configured=true`, Prowlarr `enable=true`, più lo snapshot di produzione);
  è il segnale per l'ordinamento «più usati»;
- `family` — famiglia software, vuota se non dimostrata;
- `url` — indirizzo primario, per mostrarlo e per il pulsante «Prova»;
- `source` — `native`, `jackett` o `prowlarr`;
- `state`, `evidence`, `proposable` — stato onesto e prova.

Le voci leggere hanno sempre almeno un campo obbligatorio (`url` o le
credenziali), quindi il vecchio percorso di aggiunta rapida non le aggiunge
per errore: l'integrazione UI deve usare `catalog.DefinitionForSite` per
costruire la definizione della famiglia scelta.

## Limiti e blocchi

- **403 Cloudflare**: 11 siti pubblici dell'istantanea hanno risposto 403
  «Just a moment...» al probe del 25/10. Otto hanno oggi una definizione nativa
  (tre lo erano già, cinque sono stati aggiunti il 27/10 con FlareSolverr);
  MejorTorrent, OneJAV e Torrent[CORE] sono `bloccata` e fuori coda, con il
  motivo nella sezione Cloudflare qui sopra. DaMagNet, che aveva la home 200 ma
  il dettaglio dietro challenge, è ora nativo. Il probe non usa FlareSolverr:
  per questo registra 403 anche quando la ricerca funziona con il solver.
- **Timeout**: `NewStudio` non ha risposto entro 45 s; è `morta` con la prova.
  `xxxtor` richiede ~35 s ed è verificato con timeout 45 s.
- **I2P**: Postman usa un indirizzo `.i2p` non risolvibile qui; è `morta`.
- **Local DHT**: `Magnetico` e `BitMagnet` sono servizi self-hosted, non siti
  pubblici; sono esclusi dal catalogo.
- **Privati**: senza account non si può verificare la ricerca. Le 45 voci
  private restano `da-provvi`.
- **Definizioni native**: non è stato aggiunto un parser nativo per ogni sito
  pubblico; le voci leggere con famiglia nota possono essere aggiunte tramite
  il connettore della famiglia. Un sito pubblico senza famiglia dimostrata
  resta `da-provvi` finché non si scrive una definizione o si prova la
  famiglia.
- **Uso**: quasi tutti i nuovi siti hanno `usage: 0`; il segnale dei manager è
  reale ma scarso (Prowlarr ha 3 indexer abilitati, Jackett nessuno
  configurato in questa istantanea). L'ordinamento «più usati» funziona ma
  distingue solo le voci native già configurate in produzione.
