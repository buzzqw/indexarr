# MirCrew: ponte Torznab e definizione nativa

MirCrew è un forum phpBB italiano. Ogni release è un **topic** e i magnet del
primo post sono **nascosti finché non si ringrazia** l'autore. Cloudflare
protegge tutte le pagine, non solo alcune. Per questo con Cardigann non era
possibile usarlo come indexer: servono una sessione browser persistente, un
login verificato su una pagina protetta e un'azione di «grazie» eseguita solo
al momento del download.

Questo documento descrive le due strade disponibili in indexarr:

1. il **ponte** con il servizio Python `mircrew-indexer` (pronto, verificato);
2. la **definizione nativa `mircrew`** (bozza finché non viene provata dal vivo).

La regola non cambia in nessuna delle due: **una ricerca non ringrazia mai**.
Il ringraziamento è un'azione visibile sul forum, fatta con l'account del
coordinatore, e avviene solo quando un client sceglie una release e ne chiede il
download.

---

## 1. Ponte Torznab (soluzione immediata)

Il servizio `/home/andres/mircrew-indexer` espone un'API Torznab su
`http://127.0.0.1:9118/api`. indexarr lo aggiunge come **indexer Torznab
generico**: non serve una definizione né un convertitore.

| Campo | Valore |
|---|---|
| URL base | `http://<host>:9118/api` (l'endpoint Torznab completo, `/api` compreso) |
| API key | quella in `config.json` del servizio, campo `api_key` |
| Protocollo | Torrent (anche magnet) |
| Categorie | 2000 Film, 5000 Serie TV |

Comportamento verificato il 2026-10-23 contro il servizio in esecuzione:

- `t=caps` risponde `200 application/xml` e dichiara `search`,
  `tv-search` (`q,season,ep`) e `movie-search` (`q`);
- `t=tvsearch&q=Reacher&season=3&ep=1` risponde `200` con 8 release e magnet;
- `GET /dl/<topic>.torrent?k=…&season=3&ep=1` risponde **302** con
  `Location: magnet:?xt=urn:btih:…`; il client (Sonarr/gextto) segue il
  redirect e riceve il magnet. Il topic usato nella prova era già indicizzato e
  già ringraziato: **nessun nuovo ringraziamento** è stato inviato dalla prova.

Verificato anche **da indexarr**: su un'istanza temporanea (DB e porta dedicati,
nessuna scrittura in produzione) è stato creato un indexer con la definizione
`torznab-generic`, URL `http://127.0.0.1:9118/api` e la API key del servizio.
Il test dell'indexer è verde (`reach`, `caps`, `search`) e `tvsearch`
`Reacher S03E01` ha restituito 8 release con magnet. L'URL **deve includere
`/api`**: il connettore Torznab generico non lo aggiunge da solo; senza `/api`
la radice del servizio risponde con la web UI e il test fallisce con «invalid
caps XML».

Il ponte è la strada da usare finché la definizione nativa è `bozza`. Non va
fermato né modificato: il servizio gestisce da solo sessione FlareSolverr,
login, smartfeed, catalogo e la scelta del magnet per stagione/episodio.

### Compatibilità Sonarr: `pubDate` mancante

Sonarr **rifiuta** il ponte in aggiunta come indexer Torznab:

```text
Indexer feed is not supported: Each item in the RSS feed must have a
pubDate element with a valid publish date.
```

La causa è nel servizio Python, non in indexarr: i topic non ancora
ringraziati compaiono nell'XML come `<item>` con titolo, link `/dl/…`,
categoria e stagione, ma **senza `<pubDate>`** (`core.torznab_search`,
ramo «pending item»). Sonarr valida il feed RSS prima di salvare l'indexer e
quindi non lo accetta; `enableRss: false` e `forceSave` non aggirano il
controllo. Gextto e i client che non richiedono `pubDate` funzionano.

La ricerca e il grab di Sonarr funzionano se il feed viene normalizzato: nella
prova del 2026-10-23 è stato interposto un piccolo proxy di test che aggiunge
`<pubDate>` agli item che ne sono privi. Con quel proxy Sonarr ha accettato
l'indexer, `tvsearch` di Reacher S03E01 ha restituito 198 release (117 con
`magnetUrl`) e il grab si è fermato solo perché nel banco non è configurato un
client di download (`Torrent Download client isn't configured yet`), cioè
**dopo** aver risolto il magnet. La correzione definitiva appartiene al servizio
Python: aggiungere una data agli item in attesa. Fino ad allora, per Sonarr usare
un indexer che non è il ponte diretto o accettare il limite.

### Perché il ponte è sicuro per la ricerca

Il servizio separa da sempre due momenti:

| Azione | Apre il topic | Ringrazia |
|---|---|---|
| smartfeed, catalogo, ricerca Torznab | no | no |
| `/dl/<topic>.torrent` o pulsante «Scarica» | sì | sì, solo se i magnet sono ancora nascosti |

Il link `/dl/` contiene un token HMAC per il singolo topic, non la API key. Un
topic già ringraziato non viene ringraziato di nuovo.

### Verifica offline del ponte in indexarr

Il percorso Torznab generico è coperto da `internal/feed`:

```bash
go test ./internal/feed -run MirCrew -v
```

I test usano una risposta XML identica a quella del servizio: magnet in
`<link>` e `<enclosure>`, nessun attributo `seeders` (MirCrew non li pubblica,
quindi restano sconosciuti e non zero) e data del topic.

---

## 2. Definizione nativa `mircrew`

La definizione è in
`internal/catalog/definitions/mircrew.yaml`, con fixture in
`internal/catalog/definitions/fixtures/mircrew/`. È nel catalogo con stato
**`bozza`**: non va presentata come verificata finché non viene provata con un
account reale.

### Come è fatta

| Ostacolo MirCrew | Estensione v1 usata |
|---|---|
| Cloudflare su tutto il sito | `session: { mode: flaresolverr, … }`: ogni richiesta passa da `sessions.create` + `request.get`/`request.post` |
| Login phpBB da verificare su pagina protetta | `auth.strategy: form` con `creation_time`, `form_token`, `sid`, `redirect`; `success` su `a[href*="mode=logout"]` (ucp.php) |
| Magnet nascosti finché non si ringrazia | `download.before`: apre il topic, esegue il link «Grazie» solo se i magnet mancano, riapre e legge |
| Un topic con molti magnet (episodi, pack, risoluzioni) | `download.extract` con `all: true` + `download.select: season_episode` |
| Ricerca phpBB HTML | `search.request` su `/search.php` con `sf=titleonly&sr=topics` e paginazione `start=0,25,…` |

La sessione viene riciclata dopo 12 ore e le sessioni orfane con prefisso
`mircrew-idx-` vengono chiuse alla prima richiesta. La modalità è documentata in
[`DEFINITIONS.md`](DEFINITIONS.md#session). L'azione prima del download e la
scelta del magnet sono documentate in
[`DEFINITIONS.md`](DEFINITIONS.md#downloadbefore) e
[`DEFINITIONS.md`](DEFINITIONS.md#download).

### Fixture e privacy

Le fixture sono state registrate dal sito il 2026-10-23 e poi **sanitizzate**:

- `login.html` — form di login pubblico (per il test «login fallito»);
- `login-ok.html` — estratto della pagina `ucp.php` da loggato, con il link di
  logout;
- `search.html` — due righe reali della ricerca `Reacher`;
- `topic.html` — primo post di un topic **già ringraziato**, con i magnet;
- `topic-locked.html` — derivato dal precedente togliendo i magnet e aggiungendo
  il link «Grazie»: serve al test di `download.before`.

Nessuna fixture contiene username, password, cookie, `sid`, `form_token`,
avatar o API key reali: i valori sono sostituiti con `synthetic-*`. Il test
offline non esegue alcuna richiesta al forum e non ringrazia nessuno.

### Test offline

```bash
go run ./cmd/indexarr-def validate -catalog internal/catalog/definitions/mircrew.yaml
go run ./cmd/indexarr-def check-fixtures internal/catalog/definitions/mircrew.yaml
go test ./internal/catalog -run TestMirCrewFixtures -v
go test ./internal/engine -run 'Session|Before|ReleaseName' -v
```

`TestMirCrewFixtures` disattiva di proposito la sessione FlareSolverr: verifica
solo i selettori, la categoria, la data e la scelta del magnet S03E02 fra i
tre del topic. Le meccaniche della sessione (login via `request.post`, pulizia
per prefisso, riciclo a 12 ore) hanno test dedicati in `internal/engine` con un
FlareSolverr finto.

### Stato e limiti

- **Stato: bozza.** La definizione è validata e coperta da test offline; la
  prova live ha coperto login, ricerca e download in sola lettura, ma non il
  ringraziamento, quindi non viene ancora promossa a «verificata».
- **Prova live in sola lettura (2026-10-23).** Con la definizione vera, la
  sessione FlareSolverr e l'account del coordinatore: login riuscito, ricerca
  `Reacher` con 10 risultati, download del topic 165159 (già ringraziato) con
  magnet `magnet:?xt=urn:btih:4e9f844c…` e scelta corretta di S03E01. Nessun
  ringraziamento è stato inviato. Resta `bozza` perché il ramo
  `download.before` (il «grazie» su un topic non ancora ringraziato) non è stato
  eseguito dal vivo: è coperto solo dai test offline.
- La ricerca nativa è solo phpBB HTML: niente smartfeed. `meta.latest: false`,
  quindi non entra nei feed periodici; risponde alle ricerche.
- Il login dipende dal tema phpBB `prosilver` (nomi dei campi e link «Grazie»).
  Un cambio di tema richiede di aggiornare i selettori.
- Il numero di ringraziamenti dal vivo va tenuto al minimo: la prova reale deve
  usare **al massimo 3 topic scelti apposta**, come da incarico.

### Promozione a «verificata»

Solo dopo una prova reale andata a buon fine:

1. aggiungere l'indexer `mircrew` con username, password e URL FlareSolverr;
2. eseguire il test dell'indexer: login su `ucp.php`, ricerca, download;
3. verificare nel log che la ricerca non abbia aperto topic e che il grab abbia
   prodotto un magnet `magnet:?xt=urn:btih:…`;
4. contare i ringraziamenti (massimo 3) e annotare i topic usati;
5. cambiare `state: bozza` in `state: verificata` in
   `internal/catalog/catalog.yaml`, rigenerare l'indice con
   `go generate ./internal/catalog` e aggiornare questo documento.

---

## 3. Prova dal banco Sonarr

Il banco è `indexarr-test-sonarr` su `127.0.0.1:18989` (container, senza client
di download). La produzione su `:8484` non va toccata.

Con il ponte:

1. aggiungere in Sonarr un indexer Torznab generico con URL
   `http://<host>:9118` e la API key del servizio;
2. cercare una serie italiana presente su MirCrew (per esempio `Reacher`,
   `Silo`, `FBI Most Wanted`);
3. aprire i risultati: le release già note hanno il magnet; i topic non ancora
   ringraziati hanno un link `/dl/…`;
4. fare un grab di prova e verificare che Sonarr riceva il magnet. Senza client
   di download il grab si ferma al link restituito.

La prova non deve generare ringraziamenti: usare un topic già indicizzato
(come `Reacher - Stagione 3`, topic 165159) oppure fermarsi prima del grab.
Qualsiasi prova che apra un topic non ancora ringraziato va contata.
