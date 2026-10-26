# Guida all'uso di indexarr

Questa guida accompagna dall'installazione all'uso quotidiano. Per il dettaglio
tecnico delle API vedi [API.md](API.md) (Torznab) e
[ADMIN-API.md](ADMIN-API.md) (JSON). Per installazione, systemd, Docker e
configurazione di base vedi il [README](../README.md).

---

## 1. Primo avvio

1. Apri `http://<host>:8788`.
2. Il percorso guidato chiede l'indirizzo con cui le app devono raggiungere
   indexarr. Se le app sono su un'altra macchina, metti l'IP della macchina
   indexarr (per esempio `http://192.168.1.161:8788`), non `localhost`.
3. La chiave del vault viene creata al primo avvio. **Conservala**: senza di
   essa le password e le API key salvate non si possono riaprire. Il percorso è
   indicato in Impostazioni → Vault.
4. Completa o salta il percorso: potrai riaprirlo da Impostazioni.

La dashboard mostra sorgenti, release, problemi e lo stato delle app. La voce
**Attività** raccoglie esecuzioni, nuove release e ricerche delle app. **Log**
ha i log interni, le richieste ricevute con esito e tempi, e le statistiche per
app e per indexer.

---

## 2. Aggiungere una sorgente

### Percorso guidato

1. **Indexer → Aggiungi**.
2. La ricerca nel catalogo è in cima alla pagina. Sotto ci sono le schede dei
   bisogni: **Serie TV, Film, Anime, Musica, Libri, Italiano, Altro**.
3. Le schede sono **cumulative**: puoi attivare Film e Libri insieme e vedrai
   l'unione dei risultati. Una scheda attiva ha sfondo pieno e segno di spunta.
4. **Mostra tutti i siti** apre il catalogo completo, con due elenchi a discesa
   per **tipo** (pubblico / semi-privato / privato) e **lingua**, più i filtri
   per categoria, protocollo, stato e percorso.
5. Il catalogo mostra solo le fonti già utilizzabili: le voci «da scrivere»
   non compaiono.

### Tipi di sorgente

| Tipo | Quando usarlo | Cosa chiede |
| --- | --- | --- |
| **Nativa** | Sito con definizione inclusa (la maggior parte del catalogo) | URL e, se privato, credenziali |
| **Famiglia** | Sito con software noto (NexusPHP, Unit3D, Gazelle, …) | URL e credenziali/API key |
| **RSS / Atom** | Feed con link diretti a `.torrent`/`.nzb` | URL del feed |
| **Torznab / Newznab** | Servizio Torznab esterno (non Jackett/Prowlarr) | URL completo e API key |
| **MirCrew (Smartfeed)** | URL personale `…/smartfeed/feed?u=…&e=…` | URL Smartfeed e, per il grab, l'indexer MirCrew |

### Credenziali

Nel modulo di modifica, i campi obbligatori di accesso (utente, password, API
key, cookie, token) sono in cima, nel riquadro **Accesso al sito**. Non sono
nascosti in Avanzate.

- I campi con `*` sono obbligatori.
- Password e chiavi sono cifrate nel vault.
- **Cambia** non salva: prepara il campo e mostra un pulsante **Salva** accanto.
  Puoi anche scrivere direttamente e premere Salva.
- La scheda della sorgente mostra **Configurato / Mancante** e, per i segreti,
  **ultimo aggiornamento …**: serve a verificare che il salvataggio sia
  arrivato.

Se una sorgente privata non chiede utente e password (per esempio un feed RSS
con rsskey), il modulo lo dice: l'accesso è nell'indirizzo personale.

### MirCrew e Smartfeed

- **MirCrew** (definizione nativa) fa ricerca, apre il topic al download e
  ringrazia **solo** in quel momento. Una ricerca non ringrazia mai.
- **MirCrew (Smartfeed)** elenca i topic nuovi dal tuo URL personale e li
  abbina all'indexer MirCrew per il grab. L'URL Smartfeed scade: se la scheda
  avvisa «Smartfeed scaduto», apri `/smartfeed/ui` sul sito e incolla il nuovo
  URL.
- Se incolli un URL Smartfeed nel modulo RSS generico, indexarr lo riconosce e
  propone il percorso corretto; una sorgente RSS già creata con quell'URL mostra
  **Passa a Smartfeed**.

---

## 3. La scheda di una sorgente

### Panoramica

- **Indirizzo**: sempre visibile sotto il nome, con credenziali e query
  oscurate.
- **Salute della sorgente**: verdetto in una frase («Funziona bene», «Non
  funziona: …», «Da configurare»), ultimo errore con data e ora, registro delle
  ultime operazioni e righe del log del processo.
- **Accesso al sito**: quali credenziali sono configurate o mancanti e quando
  sono state salvate.
- **Stato**: ultimo aggiornamento, prossimo, frequenza, query, latenza,
  grab.
- **Filtri**, **Ultime release**, collegamento alle app.

### Dati

Grafico delle esecuzioni, tabella delle ultime corse con motivi di scarto,
completezza dei campi (size, seeders, categoria, data, infohash) e ultimo
campione grezzo/normalizzato. Serve a capire perché una release non arriva.

### Azioni

- **Prova**: esegue raggiungibilità, capacità, login e ricerca. Se riesce,
  registra un controllo di salute e toglie il vecchio errore.
- **Aggiorna ora**: esegue subito un aggiornamento (per le sorgenti con lista
  ultime release).
- **Modifica**, **Disattiva/Attiva**, **Duplica**, **Elimina**.
- Eliminazione e disattivazione tornano al filtro da cui sei partito (per
  esempio **Disattivati**).

La frequenza è personale per sorgente e si imposta nel modulo, fuori da
Avanzate. «Riduci la frequenza» sul problema 429 raddoppia solo quell'intervallo.

Nel filtro **Problemi** c'è **Prova i problemi**: esegue la prova su tutte le
sorgenti segnalate (problema, degradate, bloccate) in background e aggiorna il
loro stato. Dopo aver corretto le credenziali o un indirizzo, un solo click
verifica i casi rimasti; le sorgenti sane non vengono toccate.

Una sorgente che fallisce non viene ritentata ogni ora: la riprova automatica
segue la scala **1, 2, 3, 6, 12, 24, 48, 96 ore**. Dopo l'ultimo tentativo
fallito il scheduler si ferma e aspetta l'utente: la scheda mostra «Riprova
automatica sospesa». Una prova riuscita o una modifica alla sorgente azzerano
la scala e riprendono la frequenza normale. Un aggiornamento manuale
(«Aggiorna ora») funziona comunque.

---

## 4. Cercare release

**Cerca** (o `/releases`) filtra la cache locale. Per ogni risultato:

- **Scarica** apre il `.torrent` o il magnet;
- **Copia magnet** copia il link negli appunti;
- il titolo apre la scheda della release, con provenienza, download, invio al
  client, preferiti, nascosti e blocco.

Filtri avanzati: categoria, seeder minimi, dimensione, qualità, lingua,
includi/escludi, regex, solo file, nascondi XXX, preferiti, nascosti e
duplicati. Le ricerche possono essere salvate.

Le regole (titolo, regex, categoria, infohash, fonte) bloccano le release prima
del salvataggio; preferiti e nascosti restano locali.

---

## 5. Collegare le app

### Sonarr / Radarr / Lidarr / Readarr / Whisparr / Mylar3 / LazyLibrarian

1. **App → Collega un'app**.
2. Inserisci tipo, nome, URL dell'app e API key dell'app.
3. Scegli il livello di sincronizzazione:
   - **Disattivata**: nessun contatto;
   - **Solo aggiunta/rimozione**;
   - **Completa**: aggiorna anche gli indexer esistenti.
4. Salva e premi **Sincronizza** (o sincronizza tutte).

indexarr crea nell'app solo gli indexer il cui nome inizia con `indexarr · `;
il resto non viene toccato. Il filtro per tag decide quali sorgenti mandare a
quale app.

### Torznab manuale

Per un client che accetta un solo URL Torznab:

- endpoint aggregato: `http://<host>:8788/api`
- API key: quella di indexarr
- per un solo indexer: `http://<host>:8788/torznab/<slug>/api`

Sonarr/Radarr creati da indexarr usano invece la base
`http://<host>:8788/torznab/<slug>`: sono loro ad aggiungere `/api`.

### gextto

In gextto aggiungi una sorgente **indexarr** con URL base
`http://<host>:8788` (senza `/api`): gextto aggiunge da solo il percorso
Torznab. Se scegli «Altro», usa l'endpoint completo `/api`.

### Client di download

**App → Client di download**: qBittorrent, Transmission, Deluge, SABnzbd,
NZBGet e gli altri supportati. Il client predefinito riceve i grab; il pulsante
**Invia** nella scheda release usa quello scelto.

---

## 6. Notifiche

**Impostazioni → Notifiche**:

- webhook e Telegram (token + chat id);
- eventi: nuove release, errori, grab falliti, catalogo aggiornato, backup
  fallito;
- cooldown anti-spam.

**Invia notifica di prova** verifica subito i canali. Se Telegram non arriva,
controlla token, chat id e che il bot sia stato avviato nella chat.

---

## 7. Manutenzione e sicurezza

- **Backup**: Impostazioni → Manutenzione. Il backup include database e stato
  del vault; conserva anche la chiave maestra separatamente.
- **Ripristino**: dalla stessa pagina. Dopo un ripristino, la sincronizzazione
  successiva riallinea le app allo stato attuale di indexarr.
- **Retention**: le release e lo storico vengono potati secondo i giorni
  configurati. Una release già scaricata non viene rimossa.
- **Log**: voce **Log** nella barra laterale → tab **Log interni** (la stessa vista resta in Attività → Log). Il livello si cambia da Impostazioni → Avanzate o direttamente dalla pagina.
  Scarica il log dal pulsante dedicato.
- **Fonti più utili**: in Manutenzione, gli scarichi serviti negli ultimi 30
  giorni per sorgente, con tipo (Feed, Web, Indexer), numero e percentuale.
  Serve a vedere quali fonti portano risultati che vengono davvero scaricati.
- **API key**: Impostazioni → API Torznab. Puoi ruotare la primaria e creare
  chiavi separate per client, revocabili.
- **Vault**: password e chiavi sono cifrate. L'interfaccia non le mostra mai in
  chiaro; un campo vuoto significa «non cambiare».
- **Accesso UI**: utente e password in Impostazioni → Sicurezza. Dietro un
  reverse proxy imposta `public_url` e inoltra `X-Forwarded-Proto`/`Host`.

---

## 8. Problemi frequenti

| Sintomo | Cosa controllare |
| --- | --- |
| «Il sito non risponde» | rete, DNS, proxy, FlareSolverr; poi **Prova** |
| «Il sito rifiuta le richieste» (403) | Cloudflare/challenge: configura FlareSolverr |
| «Il sito limita le richieste» (429) | usa **Riduci la frequenza**; non è un guasto |
| «La ricerca configurata non ha restituito risultati» | la query o il filtro del sito sono sbagliati: apri Modifica |
| Login rifiutato | password aggiornata? controlla **Accesso al sito** e l'ultimo aggiornamento |
| CAPTCHA al login | FlareSolverr non risolve i CAPTCHA: attendi o fai un login manuale |
| Smartfeed senza release | URL Smartfeed scaduto o sorgente RSS generica: usa **Passa a Smartfeed** |
| Ultime release vuote | la sorgente è «solo ricerca» o l'aggiornamento è fallito: guarda il registro |
| 404 su un vecchio link | la release è stata rimossa dalla retention: cerca di nuovo |
| Pulsanti troncati | usa una finestra più larga o il tema mobile; il titolo va a capo |

Per un problema di una singola sorgente, la Panoramica contiene già causa,
ultimo errore e registro: non serve aprire i log di sistema.

---

## 9. Comandi utili

```bash
# stato del servizio
systemctl --user status indexarr.service

# salute e versione
curl -s http://127.0.0.1:8788/healthz

# capacità Torznab
curl -s 'http://127.0.0.1:8788/api?t=caps'

# elenco indexer dall'API JSON
curl -s -H "X-Api-Key: $KEY" http://127.0.0.1:8788/api/v2/indexers

# log in tempo reale
journalctl --user -u indexarr.service -f
```

---

## 10. Dove trovare altro

- [API Torznab](API.md) — endpoint, parametri, download, errori.
- [API amministrativa JSON](ADMIN-API.md) — definizioni, indexer, proxy, app.
- [Definizioni](DEFINITIONS.md) — come scrivere una sorgente nativa.
- [Catalogo](CATALOG.md) — siti, famiglie, stati e prove.
- [MirCrew](MIRCREW.md) — sessione, ringraziamenti e Smartfeed.
- [Vault](VAULT.md) — chiave maestra, backup, Docker secrets.
- [Architettura](ARCHITECTURE.md) — pacchetti, flusso e database.
- [QA](QA.md) — test e checklist.
