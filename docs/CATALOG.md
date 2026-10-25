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

## Numeri al 2026-10-25

| Misura | Valore |
| --- | ---: |
| Totale voci nel catalogo (definizioni + voci leggere) | **622** |
| Definizioni native | 67 (8 aiutanti generici inclusi) |
| Voci leggere | **555** |
| Verificate | **120** |
| Da provare (`da-provvi`) | **498** |
| Morte (`morta`) | **4** |
| Pubbliche verificate (native + leggere, host unici) | **83** |
| Voci italiane | **13** |

Le 83 pubbliche verificate sono conteggiate per host unico, quindi una
definizione nativa e una voce leggera dello stesso sito non si sommano. Le
voci leggere private sono 438 e restano tutte `da-provvi`: senza un account
non si può verificare la ricerca.

Il catalogo ora contiene **tutte** le fonti dell'istantanea, comprese le
private senza una famiglia riconosciuta. Una fonte senza via nativa non viene nascosta:
resta in coda con il filtro **da scrivere** e non produce alcun proxy.

### Copertura nativa dell'istantanea Jackett

| Via | Fonti | Note |
| --- | ---: | --- |
| Definizione nativa (id o host) | **29** | definizioni verificate o in prova |
| Software noto (connettore di famiglia) | **155** | NexusPHP 76, UNIT3D 59, XBTIT 12, Gazelle 6, TorrentTrader 2 |
| Da scrivere | **454** | 329 private, 62 pubbliche, 63 semiprivate |
| **Totale istantanea Jackett** | **638** | metadati del 23/10 |

La somma non include le 6 fonti già configurate in Jackett quando coincidono
con una definizione nativa.

## Stati onesti

- **`verificata`** (voce leggera): è stato fatto un `GET` reale sulla home page,
  il sito ha risposto 2xx/3xx con una pagina vera e non è un login o una
  schermata anti-bot. La prova è in `metadata/verification.yaml`, con data,
  HTTP, byte e titolo. Non significa che la ricerca sia stata provata: per
  quello servono una definizione nativa e la sua fixture.
- **`da-provvi`**: nessuna prova, oppure sito privato raggiungibile ma senza
  account, oppure `403`/`5xx`/login/anti-bot. È il badge giusto per un privato
  che non è stato provato dall'interno.
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
indipendente dalla lingua dichiarata dal sito. Include la lista prioritaria del
coordinatore.

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

- **403 Cloudflare**: 14 siti pubblici hanno risposto 403 «Just a moment...».
  Sono raggiungibili dal browser ma non dal probe; restano `da-provvi`, non
  `verificata` e non `morta`. Tra questi diversi mirror Kickass/ExtraTorrent.
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
