# Manuale utente di indexarr

Questa guida accompagna dall'installazione all'uso quotidiano. Per i dettagli
tecnici delle API vedi [API.md](API.md) (Torznab) e
[ADMIN-API.md](ADMIN-API.md) (JSON). Per installazione, systemd, Docker e
configurazione di base vedi il [README](../README.md). In inglese:
[MANUAL.en.md](MANUAL.en.md).

---

## 1. Primo avvio

1. Apri `http://<host>:8788`.
2. Il percorso guidato chiede l'indirizzo che le app devono usare per
   raggiungere indexarr. Se le app sono su un'altra macchina, inserisci l'IP
   della macchina con indexarr (per esempio `http://192.168.1.10:8788`), non
   `localhost`.
3. Al primo avvio viene creata la chiave della cassaforte. **Conservala con
   cura**: senza di essa le password e le chiavi API salvate non si possono
   riaprire. Il percorso è indicato in Impostazioni → Cassaforte dei segreti.
4. Completa o salta il percorso guidato: puoi riaprirlo dalle Impostazioni.

La dashboard mostra le fonti, le release, i problemi e lo stato delle app. La
voce **Statistiche** raccoglie le richieste in arrivo con esito e tempi, la
panoramica per app e per indexer, i download, le esecuzioni, le nuove release e
i contatori di rete. **Log** contiene solo il log di sistema del processo.

La **versione** in fondo alla barra laterale apre il repository GitHub in una
nuova scheda; su uno schermo stretto lo stesso link si trova in alto a destra.
È il modo rapido per leggere il changelog o aprire una segnalazione.

---

## 2. Aggiungere una fonte

### Percorso guidato

1. **Indexer → Aggiungi**.
2. La ricerca nel catalogo è in cima alla pagina. Sotto ci sono le schede per
   esigenza: **Serie, Film, Anime, Musica, Libri, Italiano, Altre lingue**.
3. Le schede sono **cumulative**: puoi attivare insieme Film e Libri e vedere
   l'unione dei risultati. Una scheda attiva ha lo sfondo pieno e un segno di
   spunta.
4. **Mostra tutti i siti** apre l'elenco completo delle fonti già utilizzabili,
   con due menu a tendina per **tipo** (pubblico / semi-privato / privato) e
   **lingua**, più i filtri per categoria e protocollo.
5. Il catalogo mostra solo le fonti che funzionano: le voci "da scrivere", le
   bozze, le bloccate e le morte restano nell'indice per la manutenzione ma non
   compaiono. La vista di manutenzione `/indexers/new?queue=1` le elenca tutte,
   con i filtri per stato e percorso.

### Tipi di fonte

| Tipo | Quando usarlo | Cosa chiede |
| --- | --- | --- |
| **Nativa** | Sito con una definizione inclusa (gran parte del catalogo) | URL e, se privato, credenziali |
| **Famiglia** | Sito con un software noto (NexusPHP, Unit3D, Gazelle, …) | URL e credenziali/chiave API |
| **RSS / Atom** | Feed con link diretti a `.torrent`/`.nzb` | URL del feed |
| **Torznab / Newznab** | Servizio Torznab esterno (non Jackett/Prowlarr) | URL completo e chiave API |
| **MirCrew (Smartfeed)** | URL personale `…/smartfeed/feed?u=…&e=…` | URL Smartfeed e, per il grab, l'indexer MirCrew |

### Credenziali

Nel modulo di modifica i campi di accesso obbligatori (utente, password, chiave
API, cookie, token) sono in cima, nel riquadro **Accesso al sito**. Non sono
nascosti in Avanzate.

- I campi contrassegnati con `*` sono obbligatori.
- Le password e le chiavi sono cifrate nella cassaforte.
- **Cambia** non salva: prepara il campo e mostra un pulsante **Salva** accanto.
  Puoi anche digitare direttamente e premere Salva.
- La scheda della fonte mostra **Configurato / Mancante** e, per i segreti,
  **ultimo aggiornamento …**: serve a verificare che il salvataggio sia andato a
  buon fine.

Se una fonte privata non chiede utente e password (per esempio un feed RSS con
rsskey), il modulo lo dice: l'accesso è nell'indirizzo personale.

### MirCrew e Smartfeed

- **MirCrew** (definizione nativa) cerca, apre il topic al download e ringrazia
  **solo** in quel momento. Una ricerca non ringrazia mai.
- **MirCrew (Smartfeed)** elenca i nuovi topic dal tuo URL personale e li
  abbina all'indexer MirCrew per il grab. L'URL Smartfeed scade: se la scheda
  avverte "Smartfeed scaduto", apri `/smartfeed/ui` sul sito e incolla il nuovo
  URL.
- Se incolli un URL Smartfeed nel modulo RSS generico, indexarr lo riconosce e
  propone il percorso corretto; una fonte RSS già creata con quell'URL mostra
  **Passa a Smartfeed**.

---

## 3. La scheda di una fonte

### Panoramica

- **Indirizzo**: sempre visibile sotto il nome, con credenziali e query
  oscurate.
- **Stato della fonte**: verdetto in una frase ("Funziona bene", "Non funziona:
  …", "Da configurare"), ultimo errore con data e ora, log delle ultime
  operazioni e righe del log del processo. Una fonte fermata dal circuit breaker
  dopo errori ripetuti è segnata "In pausa": indexarr non la contatta finché
  l'attesa non è finita, poi riprova da sola.
- **Accesso al sito**: quali credenziali sono configurate o mancano e quando
  sono state salvate.
- **Stato**: ultimo aggiornamento, prossimo, frequenza, query, latenza, grab.
- **Filtri**, **Ultime release**, link alle app.

### Dati

Grafico delle esecuzioni, tabella delle ultime esecuzioni con i motivi di
scarto, completezza dei campi (dimensione, seeder, categoria, data, infohash) e
ultimo campione grezzo/normalizzato. Serve a capire perché una release non
arriva.

### Azioni

- **Test**: esegue raggiungibilità, capacità, login e ricerca. Se riesce,
  registra un controllo di salute e cancella il vecchio errore.
- **Aggiorna ora**: esegue subito un aggiornamento (per le fonti con un elenco
  di ultime release).
- **Modifica**, **Disattiva/Attiva**, **Duplica**, **Elimina**.
- Eliminare e disattivare riportano al filtro da cui sei partito (per esempio
  **Disattivati**).

La frequenza è per fonte e si imposta nel modulo, fuori da Avanzate. "Riduci la
frequenza" sul problema 429 raddoppia solo quell'intervallo.

Nel filtro **Problemi** c'è **Test dei problemi**: esegue il test su tutte le
fonti segnalate (problema, degradata, bloccata) in background e ne aggiorna lo
stato. Dopo aver corretto le credenziali o un indirizzo, un solo clic controlla
i casi rimasti; le fonti sane non vengono toccate.

Una fonte che fallisce non viene ritentata ogni ora: il tentativo automatico
segue la scala **1, 2, 3, 6, 12, 24, 48, 96 ore**. Dopo l'ultimo tentativo
fallito lo scheduler si ferma e aspetta l'utente: la scheda mostra "Tentativo
automatico sospeso". Un test riuscito o una modifica alla fonte azzerano la
scala e riprendono la frequenza normale. Un aggiornamento manuale ("Aggiorna
ora") funziona comunque. Una pausa del circuit breaker non è un fallimento: non
allunga la scala, non disattiva la fonte e non invia avvisi.

---

## 4. Cercare release

**Cerca** (o `/releases`) filtra la cache locale. Per ogni risultato:

- **Scarica** apre il `.torrent` o il magnet;
- **Copia magnet** copia il link negli appunti;
- il titolo apre la scheda della release, con provenienza, download, invio al
  client, preferiti, nascosti e blocco.

Filtri avanzati: categoria, seeder minimi, dimensione, qualità, lingua,
includi/escludi, regex, solo file, nascondi XXX, preferiti, nascosti e
duplicati. Le ricerche si possono salvare.

### Ricerca live manuale

Il pannello **Ricerca live** interroga subito i siti scelti invece di leggere
solo la cache. Scegli la modalità:

- **Ricerca libera**: qualsiasi titolo;
- **TV**: query, stagione, episodio, anno e id TVDb/IMDb/TMDb;
- **Film**: query, anno e id IMDb/TMDb;
- **Musica**: artista e album;
- **Libri**: autore e titolo.

Puoi selezionare uno o più indexer e una categoria. I risultati arrivano man
mano e vengono salvati; i siti che vanno in timeout continuano in background e
possono comparire dopo un ricaricamento. La vista live mostra al massimo 50
righe e non usa la paginazione normale. Con le caselle e **Invia selezionate**
mandi più release al client di download in un solo colpo.

Le regole (titolo, regex, categoria, infohash, fonte) bloccano le release prima
del salvataggio; preferiti e nascosti restano locali.

---

## 5. Collegare le app

### Sonarr / Radarr / Lidarr / Readarr / Whisparr / Mylar3 / LazyLibrarian

1. **App → Collega un'app**.
2. Inserisci tipo, nome, URL dell'app e chiave API dell'app.
3. Scegli il livello di sincronizzazione:
   - **Disattivata**: nessun contatto;
   - **Solo aggiunta/rimozione**;
   - **Completa**: aggiorna anche gli indexer esistenti.
4. In **Avanzate** imposta il profilo di questa app: RSS, ricerca automatica e
   ricerca interattiva, seeder minimi (0 = il valore dell'indexer) e le
   categorie Torznab da inviare (vuoto = tutte quelle supportate dall'app). Un
   indexer senza categorie in comune non viene sincronizzato con quell'app.
5. Salva e premi **Sincronizza** (o sincronizza tutto).

indexarr crea nell'app solo gli indexer il cui nome inizia con `indexarr · `;
il resto non viene toccato. Il filtro dei tag decide quali fonti vanno a quale
app.

### Torznab manuale

Per un client che accetta un solo URL Torznab:

- endpoint aggregato: `http://<host>:8788/api`
- chiave API: quella di indexarr
- per un singolo indexer: `http://<host>:8788/torznab/<slug>/api`

Sonarr/Radarr creati da indexarr usano invece la base
`http://<host>:8788/torznab/<slug>`: sono loro ad aggiungere `/api`.

### gextto

In gextto aggiungi una fonte **indexarr** con URL di base `http://<host>:8788`
(senza `/api`): gextto aggiunge da sé il percorso Torznab. Se scegli "Altro",
usa l'endpoint completo `/api`.

### Client di download

**App → Client di download**: qBittorrent, Transmission, Deluge, rTorrent,
SABnzbd, NZBGet, aMule, Aria2, uTorrent, Flood, Download Station, NZBVortex,
Hadouken e i blackhole torrent/Usenet. Il client predefinito riceve i grab; il
pulsante **Invia** nella scheda della release usa quello scelto, e **Invia
selezionate** nella pagina Cerca manda più release in un solo colpo. Per aMule
il protocollo della release deve essere `ed2k` e il client si configura con
l'URL della Web API (di solito `http://127.0.0.1:4711`) e la password
dell'interfaccia web.

In **Impostazioni → Avanzate**, "Invia i download eD2k tramite indexarr" è
attivo per impostazione predefinita: il feed consegna il link di download di
indexarr, che registra il grab e reindirizza a `ed2k://`; l'attributo `ed2k`
resta il link diretto per i client che lo leggono. Disattivalo solo se un client
non segue il redirect.

---

## 6. Notifiche

**Impostazioni → Notifiche**:

- webhook e Telegram (token + chat id);
- Apprise, Discord, Slack, Gotify, ntfy, Pushover, email SMTP e script
  personalizzato, ognuno con i propri segreti cifrati nella cassaforte;
- eventi: nuove release, errori, grab falliti, grab riuscito, catalogo
  aggiornato, backup fallito;
- le raffiche di grab dalla stessa applicazione sono raggruppate in un solo
  messaggio;
- cooldown anti-spam.

I messaggi sono scritti nella lingua scelta in **Impostazioni → Notifiche →
Lingua dei messaggi**: "Come l'interfaccia" (predefinita) segue il selettore in
alto, mentre una lingua fissa resta anche quando navighi in un'altra. Scegli la
lingua prima di **Invia notifica di prova**, che controlla subito i canali. Se
Telegram non arriva, controlla il token, la chat id e che il bot sia stato
avviato nella chat.

---

## 7. Manutenzione e sicurezza

- **Backup**: Impostazioni → Manutenzione. Il backup include il database e lo
  stato della cassaforte; conserva separatamente anche la chiave principale.
- **Ripristino**: dalla stessa pagina. Dopo un ripristino, la sincronizzazione
  successiva riallinea le app allo stato corrente di indexarr.
- **Retention**: le release e la cronologia vengono sfoltite secondo i giorni
  configurati. Una release già scaricata non viene rimossa.
- **Log**: la voce **Log** nella barra laterale mostra il log di sistema (il
  vecchio link Attività → Log reindirizza lì). Il livello si cambia da
  Impostazioni → Avanzate o direttamente dalla pagina.
  Scarica il log con il pulsante dedicato.
- **Fonti più utili**: in Manutenzione, i download serviti negli ultimi 30
  giorni per fonte, con tipo (Feed, Web, Indexer), numero e percentuale.
  Serve a vedere quali fonti portano risultati che vengono davvero scaricati.
- **Chiavi API**: Impostazioni → API e Torznab. Puoi ruotare quella principale e
  creare chiavi separate per client, revocabili.
- **Cassaforte**: password e chiavi sono cifrate. L'interfaccia non le mostra
  mai in chiaro; un campo vuoto significa "non cambiare".
- **Accesso alla UI**: utente e password in Impostazioni → Sicurezza. Dietro un
  reverse proxy imposta `public_url` e inoltra `X-Forwarded-Proto`/`Host`.

---

## 8. Problemi comuni

| Sintomo | Cosa controllare |
| --- | --- |
| "Il sito non risponde" | rete, DNS, proxy, FlareSolverr; poi **Test** |
| "Il sito rifiuta le richieste" (403) | Cloudflare/challenge: configura FlareSolverr |
| "Il sito limita le richieste" (429) | usa **Riduci la frequenza**; non è un fallimento |
| "In pausa dopo errori ripetuti" | il circuit breaker ha fermato la fonte dopo errori reali: non c'è nulla da fare, riprova da sola; se torna spesso, correggi la causa dell'ultimo errore |
| "La ricerca configurata non ha restituito risultati" | la query o il filtro del sito è sbagliato: apri Modifica |
| Login rifiutato | password aggiornata? controlla **Accesso al sito** e l'ultimo aggiornamento |
| CAPTCHA al login | FlareSolverr non risolve i CAPTCHA: aspetta o fai un login manuale |
| Smartfeed senza release | URL Smartfeed scaduto o fonte RSS generica: usa **Passa a Smartfeed** |
| Ultime release vuote | la fonte è "solo ricerca" o l'aggiornamento è fallito: guarda il log |
| 404 su un vecchio link | la release è stata rimossa dalla retention: cerca di nuovo |
| Pulsanti troncati | usa una finestra più larga o il tema mobile; il titolo va a capo |

Per un problema con una singola fonte, la Panoramica contiene già la causa,
l'ultimo errore e il log: non serve aprire i log di sistema.

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

# log in tempo reale (file a rotazione accanto al database, o il journal)
tail -f data/logs/indexarr.log
journalctl --user -u indexarr.service -f
```

---

## 10. Dove trovare altro

- [API Torznab](API.md) — endpoint, parametri, download, errori.
- [API amministrativa JSON](ADMIN-API.md) — definizioni, indexer, proxy, app.
- [Definizioni](DEFINITIONS.md) — come scrivere una fonte nativa.
- [Catalogo](CATALOG.md) — siti, famiglie, stati e test.
- [MirCrew](MIRCREW.md) — sessione, ringraziamenti e Smartfeed.
- [Cassaforte](VAULT.md) — chiave principale, backup, segreti Docker.
- [Architettura](ARCHITECTURE.md) — pacchetti, flusso e database.
- [QA](QA.md) — test e checklist.
