# Simulazione con applicazioni reali

Questa directory avvia **indexarr 2 vero** contro **Prowlarr, Sonarr e Radarr
veri** (immagini LinuxServer.io), con `simmock` come unico servizio finto.
Serve a verificare ciò che la simulazione di `sim/` non può verificare: il ponte
Torznab attraverso Prowlarr e la scrittura degli indexer nelle \*arr reali.

## Cosa è reale e cosa è finto

Reale:

- il binario `indexarr` costruito dal `Dockerfile` del repository;
- Prowlarr, Sonarr e Radarr, nelle immagini `lscr.io/linuxserver/*:latest`;
- le API v1 di Prowlarr (`/api/v1/indexer`, `/api/v1/indexer/test`,
  `/api/v1/appprofile`) e le API v3 delle \*arr (`/api/v3/system/status`,
  `/api/v3/indexer`, `/api/v3/indexer/schema`);
- SQLite, vault, `/api/v2`, syncjob e appsync di indexarr.

Finto:

- `simmock`: l'indexer Torznab che restituisce tre release deterministiche
  (due torrent e un `.nzb`), con API key `demo-key`;
- il tracker HTML e i client di download di `simmock`, non usati da questa
  simulazione.

Non viene contattato alcun tracker reale e non vengono configurate librerie
multimediali o client di download nelle \*arr.

## Porte

| Servizio | Porta host | Porta container | URL |
|---|---:|---:|---|
| indexarr | 18484 | 8788 | `http://127.0.0.1:18484` |
| simmock | 19080 | 19080 | `http://127.0.0.1:19080` |
| Prowlarr | 19696 | 9696 | `http://127.0.0.1:19696` |
| Sonarr | 18989 | 8989 | `http://127.0.0.1:18989` |
| Radarr | 17878 | 7878 | `http://127.0.0.1:17878` |

Le porte **8788** e **9117** dell'host non vengono mai usate: l'indexarr e il
Jackett già attivi sulla macchina non vengono toccati. Anche la porta 9696
dell'host (un Prowlarr preesistente) non viene usata: il Prowlarr della
simulazione è pubblicato su 19696.

Dentro la rete Compose i nomi sono `simmock`, `indexarr`, `prowlarr`, `sonarr`
e `radarr`. `public_url` di indexarr è `http://indexarr:8788`, così gli indexer
scritti nelle \*arr usano un URL raggiungibile dai container.

## Immagini

- `indexarr-sim-mock:local`, costruita da `sim/Dockerfile.mock` con contesto la
  root del repository (contiene `simmock` e il comando `seed`);
- `indexarr-real:local`, costruita dal `Dockerfile` della root;
- `lscr.io/linuxserver/prowlarr:latest`;
- `lscr.io/linuxserver/sonarr:latest`;
- `lscr.io/linuxserver/radarr:latest`.

Il runner costruisce le prime due. Se il working tree non compila (per esempio
per modifiche non ancora complete in altri package), `run.sh` ripiega su uno
snapshot pulito di `HEAD` creato con `git archive`. `CLEAN=1` forza lo snapshot
pulito; `REBUILD=1` ricostruisce le immagini; `PULL=1` forza il pull delle
immagini \*arr.

## Scoperta delle API key

Le \*arr non accettano chiamate API prima di aver creato la configurazione
iniziale, ma il file `config.xml` viene scritto subito. Il runner attende il
file e legge la chiave senza fare setup via browser:

```sh
docker compose -f sim/real/docker-compose.yml exec -T prowlarr \
  sh -c 'cat /config/config.xml'
```

La chiave è nel nodo `<ApiKey>`. Lo stesso vale per Sonarr e Radarr
(`/config/config.xml` nei rispettivi container).

La API key di indexarr viene generata da indexarr al primo avvio. Il runner la
legge con il comando `seed` già presente nell'immagine mock:

```sh
docker compose -f sim/real/docker-compose.yml run --rm --no-deps -T seed \
  -db /data/indexarr.db -print-key
```

Il database di indexarr sta nel volume nominato `indexarr-real_indexarr-data`.
Le configurazioni delle \*arr stanno in `sim/real/.data/{prowlarr,sonarr,radarr}`
(ignorate da git).

## Esecuzione

```sh
cd /percorso/di/indexarr
./sim/real/run.sh
```

Comandi utili:

```sh
# conserva container, volumi, configurazioni e log
KEEP=1 ./sim/real/run.sh

# ricostruisce le immagini da zero
REBUILD=1 ./sim/real/run.sh

# forza il pull delle immagini LinuxServer
PULL=1 ./sim/real/run.sh

# forza la build da HEAD pulito
CLEAN=1 ./sim/real/run.sh
```

Il runner:

1. valida `docker compose config`;
2. costruisce `simmock` e `indexarr` (se mancano o con `REBUILD=1`);
3. scarica le immagini \*arr se mancano (o con `PULL=1`);
4. avvia lo stack con `docker compose up -d`;
5. attende `/healthz`, `/ping` e i file `config.xml`;
6. crea la definizione `sim-torznab` e l'indexer nativo, lo esegue e verifica
   le tre release;
7. crea un indexer Torznab in Prowlarr verso `http://simmock:19080/torznab/api`,
   lo testa con `POST /api/v1/indexer/test` e verifica il feed per-indexer;
8. crea un indexer Torznab nativo in indexarr verso il feed di Prowlarr
   (`http://prowlarr:9696/{id}/api?apikey=...`), la esegue e verifica
   l'ingestion nel log di indexarr;
9. collega Sonarr e Radarr via `/api/v2/applications` e chiama il sync;
10. termina con `docker compose down -v --remove-orphans` tranne con
    `KEEP=1`.

Reset manuale:

```sh
docker compose -f sim/real/docker-compose.yml down -v --remove-orphans
rm -rf sim/real/.data
```

Senza `KEEP=1` il runner fa già il reset di `.data` all'inizio e il `down -v`
alla fine.

## Asserzioni

La simulazione verifica:

- compose valido e tutti i servizi raggiungibili;
- API key lette da `config.xml` per Prowlarr, Sonarr e Radarr e da `seed` per
  indexarr;
- definizione nativa, indexer nativo, run con `seen=3`/`new=3` e ricerca
  Torznab con `total="3"`;
- Prowlarr: test dell'indexer Torznab verso simmock, `baseUrl` conservato,
  `enable=true`, API key mascherata, feed per-indexer con 3 item;
- via Prowlarr: indexer Torznab nativo creato, run completato e `seen=2` (i due
  torrent; il `.nzb` non fa parte del percorso torrent), ricerca ancora con 3 item;
- app sync: applicazioni Sonarr/Radarr create, chiamata di sync osservata e,
  quando il sync riesce, presenza dell'indexer con nome `indexarr · Sim
  Torznab`, `implementation=Torznab`, `configContract=TorznabSettings`,
  `protocol=torrent`, flag RSS/automatic/interactive, `baseUrl`, `apiKey` e
  categorie `[2040,3040,5040]`.

## Limiti noti e risultato reale

### Due difetti reali bloccano oggi l'import in Sonarr/Radarr

La simulazione ha trovato due incompatibilità nel codice attuale:

1. **La rotta per-indexer non è montata.** Il web handler espone
   `/torznab/api` e `/api`, ma non `/torznab/{slug}/api`. La rotta esiste nel
   package `torznabapi` ed è coperta dai test unitari, ma `internal/web` non la
   inoltra: una richiesta a
   `http://indexarr:8788/torznab/sim-torznab/api?t=caps` risponde
   `404 page not found`.
2. **`apiPath` viene duplicato.** `syncjob` costruisce
   `public_url + "/torznab/" + slug + "/api"`, mentre Sonarr/Radarr aggiungono
   il campo `apiPath` (default `/api`) del loro schema Torznab. L'URL tentato
   diventa quindi
   `http://indexarr:8788/torznab/sim-torznab/api/api?t=caps`, che risponde 404.

Di conseguenza il sync restituisce HTTP 502 con `added=0` e nelle \*arr non
compare alcun indexer. Il runner **non simula un successo**: verifica l'errore
esatto, la rotta per-indexer 404 e l'assenza dell'indexer, stampa un messaggio
`BLOCCANTE` ed esce con codice diverso da zero. Le asserzioni complete sui
campi (`assert_remote_indexer`) sono già presenti e verranno eseguite
automaticamente quando i due difetti saranno corretti.

Per ottenere un exit 0 nonostante i difetti non esiste un'opzione: sarebbe una
falsa verifica. L'unico modo è correggere il codice Go (fuori dallo scope di
questa directory).

### Altri limiti

- Prowlarr, Sonarr e Radarr sono reali ma non hanno librerie multimediali,
  download client o account reali: non vengono testati grab e import.
- Il tracker è finto: simmock restituisce sempre le stesse tre release e non
  esiste alcuna autenticazione su tracker reali.
- Prowlarr valida la connettività già in `POST /api/v1/indexer`; per questo
  simmock deve essere healthy prima della creazione. L'indexer usa
  `baseUrl=http://simmock:19080/torznab/api` e `apiPath` vuoto, perché
  Prowlarr concatena `baseUrl` e `apiPath`.
- L'indexer Torznab vede 2 release su 3: il terzo risultato è un `.nzb` e non
  rientra nel percorso torrent.
- Le \*arr possono impiegare 10–30 secondi al primo avvio per creare il
  database; il runner attende `config.xml`, non solo la porta.
- Se il working tree ha modifiche non compilabili in altri package, la build
  usa `HEAD`. Questo è segnalato nel log; usare `CLEAN=1` per renderlo
  esplicito.
- Il runner non tocca i container o le porte dell'host (indexarr 8788,
  Jackett 9117, Prowlarr 9696).
