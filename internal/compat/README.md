# Suite di compatibilità (`internal/compat`)

Questa suite prova il contratto esterno di indexarr 2 verso le \*arr, non le
funzioni interne. Usa solo API esportate, fixture locali e `httptest`: non fa
chiamate di rete reali, non modifica i package di produzione e non aggiunge
dipendenze.

## Come si esegue

```bash
gofmt -l internal/compat
go vet ./internal/compat
go test ./internal/compat
go test -race ./internal/compat
```

La suite completa richiede meno di un secondo (con `-race` circa 8 secondi).

Per rigenerare i golden dopo una modifica **voluta** del protocollo:

```bash
UPDATE_GOLDEN=1 go test ./internal/compat -run 'TestCapsGolden|TestSearchGolden'
```

I golden non vanno aggiornati per far passare un test: un diff è un segnale di
incompatibilità.

## Fixture

| File | Uso |
| --- | --- |
| `testdata/canonical.json` | Quattro release canoniche: magnet, torrent, nzb Usenet e release multi-indexer. Contiene anche un riferimento di download con passkey, che non deve mai comparire nell'XML. |
| `testdata/caps.golden.xml` | Snapshot esatto di `t=caps` (categorie, limiti, parametri dichiarati, ordine stabile). |
| `testdata/search.golden.xml` | Snapshot esatto di una ricerca aggregata: escaping XML, ordine, attributi Torznab, provenance e link proxy. |

Le date sono fisse e il fuso è forzato a UTC in `TestMain`, quindi i golden sono
riproducibili su qualsiasi macchina.

## Matrice di copertura

| Area | Coperto | Note |
| --- | --- | --- |
| XML Torznab/Newznab | Root `rss` 2.0, namespace `torznab` e `newznab`, `<newznab:response offset total>`, `<item>`, `<title>`, `<guid isPermaLink="false">`, `<link>`, `<comments>`, `<pubDate>`, `<size>`, `<category>`, `<enclosure>` | Well-formed XML verificato con `encoding/xml` |
| Modalità | `search`, `tvsearch`, `movie`, `music`, `book` | `music` e `book` sono alias di `search`, come documentato |
| Parametri | `q`, `season`, `ep`, `imdbid` (con e senza `tt`), `tmdbid`, `tvdbid`, `limit`, `offset`, `cat`, `extended`, parametri sconosciuti | `rid`, `artist`, `album`, `author`, `title` sono tollerati e ignorati dove non supportati |
| Limiti | `limit` default 50, massimo 100, valori negativi/zero, `offset` negativo/fuori pagina | Il tetto di 100 è provato con 109 release |
| Categorie | Radici 1000–8000 e sottocategorie, filtro per radice e sottocategoria, valori fuori range | Un `cat` non numerico viene ignorato (comportamento attuale) |
| Attributi Torznab | `category`, `size`, `seeders`, `leechers`, `peers`, `infohash`, `magneturl`, `publishdate`, `downloadvolumefactor`, `uploadvolumefactor`, `protocol=usenet`, `indexers`, `provenance` | Per Usenet assenti `infohash`/`magneturl` |
| Errori | Codici 100, 101, 200, 201, 300, 400, 500 | Il codice 900 non ha percorsi raggiungibili in questo handler |
| Download | `/dl/{id}.torrent`, `/dl/{id}.nzb`, `t=get`, `HEAD`, API key obbligatoria, chiave aggiuntiva | Link sempre proxy: nessun URL del tracker, passkey o `download_ref` |
| Alias | `/api`, `/torznab/api`, `/newznab/api`, `/api/v2.0/indexers/all/results/torznab/api`, `/torznab/{slug}/api`, `/api/v2.0/indexers/{slug}/results/torznab/api` | Risposte byte-identiche a parità di selezione |
| Selezione per indexer | Due sorgenti legacy, slug case-sensitive, indexer sconosciuto → Torznab 201 | Il filtro aggregato per `tag`, `status`, `indexer_type` è coperto |
| \*arr payload | Sonarr, Radarr, Lidarr, Readarr, Whisparr × Torznab torrent/Newznab usenet | `implementation`, `configContract`, `protocol`, `fields.baseUrl/apiKey/categories`, `priority`, `tags`, `enableRss`, `enableAutomaticSearch`, `enableInteractiveSearch` |
| Fake Servarr | Integrazione con `internal/simmock` | Verifica che il payload venga accettato e memorizzato |
| Concorrenza e header | 20 ricerche concorrenti con la stessa chiave, risposte limitate, `Cache-Control: no-store`, `X-Content-Type-Options: nosniff`, content type | Eseguito anche con `-race` |

## Fuori perimetro

- Scraping, login e connettori di famiglia: coperti dai test dei rispettivi
  package.
- Ricerca live (`torznab_live_search`), rate limit distribuito e refresh della
  provenance: richiedono scheduler o servizi esterni.
- Newznab nativo completo: questa suite verifica la forma Newznab compatibile
  con le \*arr e il download `.nzb`, non un protocollo Usenet separato.
- Download client (qBittorrent, Transmission, SABnzbd, NZBGet, Deluge,
  rTorrent): coperti da `internal/downloadclient`; qui si verifica solo il
  payload servito da indexarr.
- Compatibilità con versioni reali delle \*arr: la suite usa lo schema e le
  richieste delle API v3/v1, non binari Sonarr/Radarr/Lidarr reali.

## Problemi noti

| Test | Stato | Dettaglio |
| --- | --- | --- |
| `TestUnknownSeedersAreOmitted` | `SKIP` | **BUG**: una release con `Seeders=-1` e nessuna riga `release_sources` viene resa con `seeders=0`, `leechers=0`, `peers=0`. `writeItem` confronta la provenance a valore zero (0) con il sentinella “sconosciuto” (-1) e lo sovrascrive. Contraddice `docs/API.md`, che prescrive l’omissione degli attributi. Evidenza nel golden: `<torznab:attr name="seeders" value="0"/>` sull’item `Compat.NZB.Album.2026.FLAC`. |
| `TestErrorCode900` | `SKIP` | Non è un bug: il codice 900 è definito ma nessun percorso di richiesta lo produce. I codici raggiungibili sono 100, 101, 200, 201, 300, 400 e 500. |
