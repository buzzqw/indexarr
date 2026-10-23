# Simulazione end-to-end di indexarr 2

Questa directory contiene una simulazione deterministica e autosufficiente di
indexarr 2. Non contatta tracker reali, Prowlarr, Sonarr o client di download:
tutto è servito da `simmock`, un unico processo Go che implementa le superfici
HTTP finte.

L'obiettivo è verificare il percorso completo:

1. definizione nativa e indexer creati via `/api/v2`;
2. run dell'indexer contro un indexer Torznab finto;
3. release nel database e ricerca Torznab di indexarr;
4. download del payload `.torrent` e `.nzb`;
5. applicazione Sonarr collegata via `/api/v2`, sync e indexer creato in Sonarr;
6. invio di payload ai client SABnzbd, NZBGet, Deluge e rTorrent.

## Porte e chiavi

| Servizio | Porta host | URL |
|---|---|---|
| indexarr | 18484 | `http://127.0.0.1:18484` |
| simmock | 19080 | `http://127.0.0.1:19080` |

Chiavi fisse della simulazione:

| Superficie | Chiave |
|---|---|
| Torznab/Newznab (`apikey`) | `demo-key` |
| Sonarr (`X-Api-Key`) | `demo-sonarr` |
| Prowlarr (`X-Api-Key`) | `demo-prowlarr` |
| Deluge (`auth.login`) | `demo` / `demo` |

Le porte 8484 e 9117 non vengono mai usate. In Docker indexarr ascolta `:8484`
nel container, pubblicato su `18484`; simmock ascolta `:19080`.

## Esecuzione locale

```bash
./sim/run.sh
```

Lo script:

- compila `cmd/simmock`, `cmd/indexarr` e `sim/seed` in `sim/.bin/`
  (ignorata da git);
- crea una directory temporanea con database, chiave del vault, configurazione
  e log;
- esegue `sim/seed` per impostare `public_url`, abilitare il fetch dei torrent
  e sincronizzare il catalogo built-in;
- avvia simmock su `127.0.0.1:19080` e indexarr su `127.0.0.1:18484`;
- legge la API key di indexarr dal log (`apikey=...`), con fallback
  `sim/seed -print-key`;
- esegue tutte le asserzioni e termina con exit code 1 se ne fallisce almeno
  una.

Per conservare database e log di un'esecuzione:

```bash
KEEP=1 ./sim/run.sh
```

## Esecuzione con Docker

```bash
./sim/run-docker.sh
```

Il comando usa `sim/docker-compose.yml`:

- `simmock`, costruito da `../sim/Dockerfile.mock` con contesto la root del
  repository (multi-stage `golang:1.26-alpine`), pubblicato su `19080`;
- `seed`, una one-shot con la stessa immagine, che prepara il volume
  `sim-data` prima di indexarr;
- `indexarr`, costruito dal `Dockerfile` esistente del repository, pubblicato
  su `18484`, con `INDEXARR_MASTER_KEY` fissa e healthcheck.

I container si chiamano `indexarr-sim`, `indexarr-sim-mock` e
`indexarr-sim-seed`; l'immagine è `indexarr-sim:local`, quindi l'immagine
`indexarr:latest` e i container già presenti non vengono toccati.

`run-docker.sh` fa `down -v` del solo progetto `indexarr-sim` all'inizio e alla
fine (trap), quindi non lascia volumi orfani. Per fermare a mano:

```bash
docker compose -f sim/docker-compose.yml down -v --remove-orphans
```

Dentro la rete Docker indexarr raggiunge simmock su `http://simmock:19080`. Le
impostazioni pubbliche usano invece `http://127.0.0.1:18484`, così i link
`.torrent` restituiti da indexarr sono scaricabili dall'host. Il Sonarr finto
registra l'indexer ma non richiama indexarr.

## Cosa è finto e cosa è reale

Reale:

- il binario indexarr e tutti i package usati in produzione;
- SQLite, migrazioni, catalogo, vault, `/api/v2`, scheduling nativo;
- il client Torznab di famiglia, la ricerca, il download e il proxy payload;
- il client Sonarr di `appsync` e la riconciliazione di `syncjob`;
- i client di download di `internal/downloadclient`.

Finto:

- l'indexer Torznab/Newznab che produce tre release sintetiche;
- il tracker HTML con form di login, CSRF, cookie e tabella risultati;
- Prowlarr e Sonarr, che tengono lo stato in memoria;
- SABnzbd, NZBGet, Deluge e rTorrent, che registrano gli add in memoria;
- i payload: un `.torrent` bencodificato minimale e un `.nzb` XML.

`GET /debug/state` su simmock espone lo stato osservabile: contatori Torznab,
download del tracker, indexer presenti in Sonarr e add ricevuti da ogni client.
Tutte le risposte sono deterministiche; nessuna richiesta esce dalla macchina.

## Fixture deterministiche

| # | Titolo | Tipo | GUID |
|---|---|---|---|
| 1 | `The.Simpsons.S01E01.1080p.WEB-DL` | torrent con magnet + infohash | `demo-magnet-1` |
| 2 | `Sim.Demo.Movie.2026.1080p.BluRay.x264` | enclosure `.torrent` | `demo-torrent-2` |
| 3 | `Sim.Demo.Album.2026.FLAC` | Usenet `.nzb` | `demo-nzb-3` |

Le release hanno categorie 5040, 2040 e 3040, dimensioni, seeder e date fisse.
`t=get` accetta sia gli id numerici sia i GUID.

La definizione è in `sim/definitions/torznab-demo.yaml` (`connector: torznab`,
categorie 2040/3040/5040). L'indexer creato dallo script riceve sia `apikey`
(impostazione della definizione, cifrata nel vault) sia `api_key` (il nome letto
dal connettore di famiglia) e `allow_private=true`, necessario perché simmock
ascolta su loopback/rete privata.

## Asserzioni eseguite

Lo script condivide le asserzioni in `sim/assertions.sh` e verifica:

**simmock**

- `/healthz` restituisce `{"status":"ok"}`;
- `t=caps` è XML valido con categorie e modalità (`tv-search` inclusa);
- `t=search` su Torznab e Newznab restituisce 3 item con attributi
  `torznab`/`newznab`, infohash, magnet e enclosure `.nzb`;
- `t=get` restituisce un `.torrent` bencodificato con content type
  `application/x-bittorrent` e un `.nzb` con content type `application/x-nzb`;
- senza `apikey` la ricerca Torznab risponde 401;
- il tracker mostra il form con CSRF, il login imposta il cookie, la ricerca
  contiene 2 righe con `size`, `seeders`, `date`, `category` e 2 link di
  download; il download funziona solo con il cookie (senza cookie: 401);
- Prowlarr restituisce indexer (Torznab/Newznab) e applicazioni (Sonarr) con
  `X-Api-Key: demo-prowlarr`;
- Sonarr richiede `X-Api-Key: demo-sonarr` e il suo stato contiene
  `appName: Sonarr`.

**indexarr**

- `POST /api/v2/definitions` crea `sim-torznab` (201);
- `POST /api/v2/indexers` crea l'indexer nativo (201);
- `POST /api/v2/indexers/{id}/run` riporta `seen=3` e `new=3`;
- `/api?t=search&apikey=...` restituisce `total="3"` e 3 item;
- il link `.torrent` restituito da indexarr si scarica con content type
  `application/x-bittorrent`;
- `POST /api/v2/applications` collega Sonarr e il sync riporta `added=1`;
- l'indexer `indexarr · Sim Torznab` è presente in Sonarr con base URL
  contenente `sim-torznab/api`.

**download client e stato**

- SABnzbd `mode=addurl` risponde `status:true`;
- NZBGet `append` risponde `result:1`;
- Deluge `auth.login` e `core.add_torrent_magnet` funzionano con cookie;
- rTorrent `system.listMethods` e `load.start` rispondono via XML-RPC;
- `/debug/state` contiene gli add di tutti e quattro i client, il download del
  tracker e l'indexer sincronizzato in Sonarr.

## Test Go e controlli

```bash
gofmt -l cmd/simmock internal/simmock sim/seed
go vet ./cmd/simmock ./internal/simmock ./sim/seed
go test ./internal/simmock
go run ./cmd/indexarr-def validate sim/definitions/torznab-demo.yaml
bash -n sim/run.sh sim/run-docker.sh sim/assertions.sh
```

I test di `internal/simmock` usano `httptest` e coprono caps/search/get,
tracker, Prowlarr, Sonarr (add/update/delete/list) e ogni client di download,
incluso `/debug/state`.
