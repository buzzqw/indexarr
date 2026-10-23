# Guida al redesign dell'interfaccia

Data: 23 ottobre 2026 · Base: commit `4e429e5` (WP14 fase 1 fatta)
Collegati: [NEXT-STEPS WP14](NEXT-STEPS.md#wp14--interfaccia-da-pannello-tecnico-a-prodotto-semplice),
[manifesto](MANIFESTO.md), [mappa UI](UI-MAP.md).

Questo documento dice **come deve diventare** l'interfaccia, pagina per pagina,
e con quali regole. WP14 in NEXT-STEPS resta il work package (perché, fasi,
accettazione); qui c'è la specifica da seguire. In caso di conflitto vale
questo documento.

L'obiettivo del manifesto è: **prima ricerca funzionante in meno di due
minuti, senza leggere documentazione.** Ogni scelta qui sotto serve a quello.

---

## 1. Dove siamo (verifica del 23/10 pomeriggio)

Istanza di prova su copia del DB, desktop 1440 px, lingua italiana. La fase 1
ha tolto i difetti peggiori (definizioni `legacy-*` nei form, selettore
duplicato, `public_url`). Restano questi problemi, in ordine di impatto.

| # | Pagina | Cosa si vede | Perché è un problema |
|---|---|---|---|
| V1 | Tutte | Il link "Salta al contenuto" è **sempre visibile** in alto a sinistra: `layout.html` usa `class="skip"` ma `app.css` non definisce `.skip` | Sembra un difetto grafico su ogni pagina; va mostrato solo al focus da tastiera |
| V2 | Home | Sei contatori allo stesso livello (Release, Indexer nativi, Sorgenti legacy, Applicazioni, Errori 24h **169**, Database); il riquadro "Stato" dice insieme "169 errori in 24h" e "tutti sani ora" | L'utente non capisce se c'è un problema. Contatori tecnici (dimensione DB) accanto a quelli che contano |
| V3 | Home | Le azioni proposte sono "Nuovo indexer nativo" e "Aggiungi sorgente legacy" | Chiede all'utente di scegliere tra due concetti interni prima ancora di aver aggiunto un sito |
| V4 | Nuovo indexer | Scelto **Corsaro** (sito verificato, nessun campo obbligatorio) il form mostra comunque 12 campi: Protocollo, Slug, Priorità, Tag, URL con segnaposto `https://indexer.example/api`, Categorie come ID numerici, Query fissa, Categoria predefinita `5040`, User agent, Consenti indirizzi privati, più "Category id" in inglese con "0 means all categories" | Per un sito pubblico già noto non serve **nessun** campo: dovrebbe bastare "Aggiungi" |
| V5 | Indexer | Testo introduttivo che spiega definizioni, connettori e ponte; tab Nativi 0 / Ponte legacy 37; riquadro "Quick add" con un paragrafo di istruzioni; pagina metà italiano e metà inglese | Muro di testo, due mondi, lingua mista |
| V6 | Release | 13 controlli di filtro prima del primo risultato; due etichette "Source"/"Sorgente" con significati diversi (indexer e tipo di rip); la tabella esce dal bordo a 1440 px (l'ultima colonna è tagliata) | La pagina più usata è la più faticosa |
| V7 | Release | Righe con "0 B", seeders "?", leechers "?", categoria vuota | Dati mancanti mostrati come dati sbagliati: "0 B" non è "sconosciuto" |
| V8 | Impostazioni | La API key è stampata **in chiaro** appena si apre la pagina, anche dentro l'URL di esempio; testi in inglese ("Copy key", "Regenerate primary API key", "Backup and maintenance") | Chiunque guardi lo schermo la legge; lingua mista |
| V9 | Applicazioni | Prima di qualsiasi app, un riquadro spiega i tre livelli di sincronizzazione; il pulsante "Indexer nativi" porta altrove | Spiegazione prima del bisogno; azione secondaria che distrae |
| V10 | Sidebar | Voci 10; in fondo versione `vdev`, endpoint ripetuto, link rosso "imposta public_url", 7 pulsanti lingua sempre visibili | Il piede della sidebar è rumoroso quanto la navigazione |

Sotto tutto c'è la diagnosi di WP14: **la UI mostra l'architettura interna
invece dei compiti dell'utente.** WP16 (rimozione del percorso legacy) toglie
da solo V3, V5 e metà di V2: per questo le fasi 2–3 vengono dopo WP16.

---

## 2. Le dieci regole (valgono per ogni pagina e ogni PR)

1. **Un concetto: l'indexer, cioè un sito.** Le parole *definizione*,
   *famiglia*, *connettore*, *ponte*, *legacy*, *slug*, *SSRF* non compaiono
   fuori da "Avanzate" e dagli strumenti per chi scrive definizioni.
2. **Un'azione principale per pagina**, in alto a destra, pulsante pieno. Le
   altre azioni sono secondarie (contorno) o in un menu "⋯".
3. **Al massimo 5 campi visibili in un form.** Il resto va in un blocco
   `<details>` "Avanzate", chiuso, con valori già sensati.
4. **Mai chiedere ciò che si può dedurre.** Protocollo, nome nell'URL,
   categorie, URL di un sito noto, user agent: si derivano dalla definizione.
5. **Ogni pagina vuota dice cosa fare** con un pulsante, non con un paragrafo.
6. **Ogni errore ha causa e rimedio in una frase**, con l'azione a portata di
   clic ("Il sito risponde 429: troppe richieste. [Riduci la frequenza]").
7. **Un numero, una fonte.** Se due parti della pagina parlano dello stesso
   stato devono leggere lo stesso valore dal modello. Niente "169 errori" e
   "tutto ok" sulla stessa schermata.
8. **Dati mancanti si scrivono "—"**, mai `0`, `?` o vuoto ambiguo.
9. **I segreti non si mostrano finché l'utente non li chiede** (pulsante
   "Mostra" o "Copia"). Mai nei link, nei title, negli URL di esempio.
10. **Ogni flusso finisce con il passo successivo** (messaggio di conferma +
    pulsante verso la cosa che verosimilmente serve dopo).

Regole di testo:

- Pulsanti con un verbo: "Aggiungi indexer", "Prova", "Salva", "Collega
  Sonarr". Mai "OK", "Invia", "Submit".
- Frasi brevi, seconda persona, niente gergo. Il testo d'aiuto sta sotto il
  campo, una riga.
- Tutte le stringhe passano da `T` (i18n). Nessun testo letterale nei template
  (c'è già un test: va esteso a tutti i template).

---

## 3. Struttura: da 10 voci a 6

```text
┌──────────────────────┐
│ ● indexarr           │
│ [ Cerca release…   ] │   ← ricerca globale, invio porta a Cerca
│                      │
│ ⌂  Home              │
│ ⌕  Cerca             │
│ ☰  Indexer      2 ⚠  │   ← badge solo se c'è qualcosa da fare
│ ⇄  App               │
│ ◷  Attività          │
│ ⚙  Impostazioni      │
│                      │
│ ─────────────────    │
│ v0.9.0 · IT ▾        │   ← versione + selettore lingua compatto
└──────────────────────┘
```

| Voce | Contenuto | Da dove arriva |
|---|---|---|
| Home | Stato, endpoint da copiare, prossimo passo | Panoramica |
| Cerca | Ricerca release | Release |
| Indexer | Elenco unico dei siti, dettaglio, aggiunta | Indexer + sorgenti + Importa |
| App | App \*arr **e** client di download | Applicazioni + client da Impostazioni |
| Attività | Esecuzioni, ricerche ricevute, grab, log | Attività |
| Impostazioni | Sezioni (vedi §4.8) | Impostazioni, Regole, Manutenzione, Definizioni |

URL esistenti: redirect 302 verso la nuova posizione (come già fatto per
`/sources`, `/runs`, `/logs`). `scripts/ui-smoke.sh` va aggiornato con la
nuova mappa.

Il selettore lingua diventa un menu a tendina nel piede; l'endpoint esce dalla
sidebar (sta in Home e in App, dove serve).

---

## 4. Pagina per pagina

### 4.1 Home

Scopo: in 3 secondi rispondere a "funziona?" e "cosa devo fare adesso?".

```text
Home                                                     [Aggiungi indexer]

┌ Stato ─────────────────────────────────────────────────────────────────┐
│ ● Tutto funziona · 12 indexer attivi · ultima release 4 min fa         │
│   oppure                                                               │
│ ▲ 2 indexer hanno problemi                                  [Vedi]     │
│   TorrentGalaxy — il sito risponde 429 da 2 ore                        │
│   MirCrew — credenziali rifiutate                                      │
└────────────────────────────────────────────────────────────────────────┘

┌ Collega le tue app ────────────────────────────────────────────────────┐
│ URL per Sonarr/Radarr  [ http://192.168.1.50:8484/api      ] [Copia]   │
│ API key                [ ••••••••••••••••                  ] [Copia]   │
│ Oppure lascia fare a indexarr:                         [Collega un'app]│
└────────────────────────────────────────────────────────────────────────┘

┌ Ultime 24 ore ───────────────┐ ┌ Prossimo passo ─────────────────────┐
│ 312 release nuove            │ │ Nessuna app collegata: Sonarr non   │
│ 48 ricerche dalle app        │ │ vedrà questi indexer.  [Collega]    │
│ 3 grab                       │ │                                     │
└──────────────────────────────┘ └─────────────────────────────────────┘
```

- **Stato** ha tre livelli: "Tutto funziona" (verde), "N indexer hanno
  problemi" (giallo, con la lista), "indexarr non riesce a lavorare" (rosso:
  DB, vault, rete). Lo calcola **una** funzione `HomeStatus(ctx)` usata anche
  dal badge della sidebar.
- Gli errori delle ultime 24 h che si sono già risolti **non** sono un
  problema: vanno in Attività, non in Home. Il conteggio "169 esecuzioni
  fallite" sparisce dalla Home.
- Dimensione DB, numero di definizioni, contatori di .torrent salvati: in
  Impostazioni → Manutenzione.
- "Prossimo passo" sceglie **una** cosa da una lista ordinata: nessun indexer
  → "Aggiungi il primo indexer"; `public_url` assente e richieste da fuori →
  "Imposta l'indirizzo per le app"; nessuna app → "Collega un'app"; nessun
  client di download → "Aggiungi un client"; backup mai fatto → "Fai un
  backup". Se non manca nulla, il riquadro non appare.
- Se `public_url` non è impostato, il campo URL mostra l'indirizzo che il
  browser sta usando (`r.Host`) con la nota "Verifica che le app lo
  raggiungano" e il pulsante "Usa questo indirizzo".

### 4.2 Indexer — elenco

```text
Indexer                                   [Importa]  [Aggiungi indexer]

[ Cerca tra i tuoi indexer… ]   Tutti 12 · Problemi 2 · Disattivati 1

  ● ExtTo                    Pubblico · 3 filtri       4 min fa    ⋯
  ▲ TorrentGalaxy            Pubblico                  429 da 2 h  ⋯
     Il sito limita le richieste. [Riduci frequenza] [Dettagli]
  ● Corsaro                  Pubblico                  12 min fa   ⋯
  ● 1337x                    Via Prowlarr              8 min fa    ⋯
  ○ MirCrew                  Privato · disattivato     —           ⋯
```

- Una riga per sito. Badge di accesso: "Pubblico", "Privato", "Via Jackett",
  "Via Prowlarr", "Feed". Niente "nativo/legacy".
- Pallino di stato con la stessa scala della Home (● ok, ▲ problema, ○
  disattivato, ◌ in prova). Il problema è scritto sotto la riga, con
  l'azione.
- Menu "⋯": Prova, Aggiorna ora, Disattiva, Duplica, Elimina. Le azioni di
  massa (seleziona → disattiva/elimina) compaiono solo dopo aver
  selezionato.
- Clic sul nome → pagina dettaglio. Mobile: card con nome, badge, stato.
- **Più filtri sullo stesso sito** (oggi 19 "extto.org (n)") diventano un
  solo indexer con "3 filtri" (vedi 4.4). Fino a WP15 il dettaglio mostra i
  filtri come elenco di impostazioni; dopo WP15 come scelte guidate.

### 4.3 Indexer — aggiunta

Il gesto naturale è "cerco il sito". Il form arriva dopo, e piccolo.

```text
Aggiungi indexer

[ Cerca un sito o incolla un indirizzo…          ]

  Siti supportati
  ExtTo              Pubblico   Film, Serie, Musica     ✓ Verificato
  TorrentGalaxy      Pubblico   Film, Serie             ✓ Verificato
  Corsaro            Pubblico   Film, Serie (ITA)       ✓ Verificato
  Knaben             Pubblico   Meta-ricerca            ✓ Verificato

  Tipi di sito (per tracker non in elenco)
  UNIT3D             Privato    serve URL e API key     In prova
  Gazelle            Privato    serve URL e API key     In prova
  Newznab (Usenet)   —          serve URL e API key

  Non lo trovi?
  [Collega tramite Jackett o Prowlarr]   [Aggiungi un feed RSS]
```

Dopo la scelta:

```text
Aggiungi Corsaro                                         ← Cambia sito

Nome          [ Corsaro                    ]
Categorie     [✓] Film  [✓] Serie TV  [ ] Musica      (dalla definizione)

▸ Avanzate
    Priorità, tag, nome nell'URL, user agent, proxy,
    "Il sito è nella mia rete locale"

                                      [Annulla]  [Aggiungi e prova]
```

- Cosa mostra il form: **solo** i `settings` della definizione con
  `required: true`, più Nome (precompilato) e Categorie (caselle con nomi,
  precompilate dalla definizione). Se la definizione non ha campi
  obbligatori, il form ha solo Nome e Categorie.
- Mai mostrare: Protocollo (dalla definizione), URL per un sito con
  `meta.urls` (si sceglie da un menu solo se ce ne sono più d'uno, in
  Avanzate), Query fissa e Categoria predefinita (in Avanzate o eliminate),
  ID numerici di categoria.
- Incollare un indirizzo: se corrisponde a `meta.urls` di una definizione
  verificata → apre quella; se sembra Torznab/Newznab (`/api`, `t=caps`) →
  form Torznab; se è un feed (risposta XML RSS/Atom) → form Feed; se è un
  endpoint di Jackett/Prowlarr → "Collega tramite Jackett/Prowlarr".
- **Aggiungi e prova** salva ed esegue subito il test per fasi; si atterra
  sul dettaglio con il risultato visibile:

```text
Corsaro aggiunto.
  ✓ Sito raggiungibile      ✓ Ricerca: 25 risultati     ✓ Download: magnet ok
Prossimo passo: le tue app non vedono ancora questo indexer. [Sincronizza app]
```

- Stati del catalogo in parole: "Verificato", "Stabile", "In prova",
  "Problemi". Le definizioni "In prova" si possono aggiungere con un avviso
  di una riga, non nascoste.

### 4.4 Indexer — dettaglio (nuova centralità)

Oggi `GET /indexers/{id}` esiste ma il centro è il form di modifica. Il
dettaglio diventa la pagina di lavoro di un sito:

```text
◂ Indexer
ExtTo   ● Funziona · Pubblico                 [Prova]  [Aggiorna ora]  ⋯

┌ Stato ──────────────────────────────────────────────────────────────┐
│ Ultimo aggiornamento 4 min fa · 47 nuove · prossimo tra 56 min      │
│ Ricerche dalle app (24 h): 31 · tempo medio 0,8 s · grab: 2         │
└─────────────────────────────────────────────────────────────────────┘
┌ Filtri ─────────────────────────────────────────────────────────────┐
│ Serie TV · Film · uploader EZTV                       [Modifica]    │
└─────────────────────────────────────────────────────────────────────┘
┌ Ultime release ─────────────────────────────────────────────────────┐
│ Titolo…                              1,4 GB   312 seed   4 min fa   │
└─────────────────────────────────────────────────────────────────────┘
┌ Impostazioni ───────────────────────────────────────────── [Modifica]┐
▸ Avanzate e diagnostica (definizione, versione, hash, ultimo errore
  completo, richieste per host, origine)
```

Tutto ciò che oggi è "per sviluppatori" (hash, origine della definizione,
famiglia) vive nel blocco chiuso "Avanzate e diagnostica", non sparisce.

### 4.5 Cerca

```text
Cerca                                                [Ricerche salvate ▾]

[ dune 2160p                                    ]  [Cerca]
Film · Serie · Musica · Libri · Tutto        Ordina: Più recenti ▾
▸ Filtri (risoluzione, lingua, dimensione, seeders, indexer, nascosti)

312 risultati
  Dune.Part.Two.2024.2160p…    18,2 GB   1.204 seed   ExtTo   2 h fa  [⤓] ⋯
```

- Una casella, categorie come scelte rapide, il resto dei filtri in un
  pannello chiuso che **ricorda** l'ultimo stato (localStorage, non
  essenziale). Contatore "3 filtri attivi" quando il pannello è chiuso.
- Le etichette ambigue si separano: "Indexer" (da dove viene) e "Tipo di
  rip" (WEBRip, BluRay…).
- Colonne a 1440 px: titolo (si tronca con `…` e title), dimensione,
  seeders, indexer, età, azioni. Leechers e categoria in seconda riga o nel
  dettaglio. **Nessuna** tabella più larga del contenitore: verificato con
  `scrollWidth <= clientWidth` nello smoke test.
- Azioni: un pulsante primario "Scarica" (manda al client predefinito, o
  scarica il .torrent/magnet se non c'è client) e "⋯" per preferito,
  nascondi, blocca, elimina.
- Valori sconosciuti come "—" (V7). Se un indexer produce **sempre** valori
  sconosciuti (dimensione 0 su tutte le righe) è un difetto della
  definizione: va segnalato nel dettaglio dell'indexer, non nascosto in Cerca.
- Regex ed esclusioni restano, spiegate in un tooltip "?" accanto alla
  casella, non in un paragrafo.

### 4.6 App

```text
App                                   [Aggiungi client]  [Collega un'app]

App che usano indexarr
  ● Sonarr      http://192.168.1.20:8989   sincronizzata 3 min fa   ⋯
  (vuoto) → "Collega Sonarr, Radarr o un'altra app: indexarr aggiunge
            da solo gli indexer." [Collega un'app]

Client di download
  ● qBittorrent (predefinito)   192.168.1.30:8080              ⋯
  ● SABnzbd                     192.168.1.30:8085              ⋯

▸ Collegare un'app a mano (URL e API key da copiare)
```

- Il form "Collega un'app": tipo (icona + nome), indirizzo, API key, pulsante
  "Prova". Il livello di sincronizzazione ha un default ("Aggiunge e
  rimuove") e la spiegazione dei tre livelli sta **accanto alla scelta**, in
  Avanzate, non in cima alla pagina.
- Più client di download (oggi uno solo, in Impostazioni). Uno è il
  predefinito; una regola semplice per protocollo (torrent → qBittorrent,
  usenet → SABnzbd).

### 4.7 Attività

Tab: **Aggiornamenti** (run), **Ricerche** (richieste ricevute dalle app, con
indexer interrogati e tempi), **Grab**, **Log**. Ogni riga in errore ha la
frase causa+rimedio della regola 6. Aggiornamento automatico ogni 10 s solo
mentre la tab è visibile (`document.visibilityState`), con pausa.

### 4.8 Impostazioni

Sezioni come voci di una sotto-navigazione a sinistra (a destra il form),
salvataggio per sezione:

| Sezione | Contiene | Visibile senza "Avanzate" |
|---|---|---|
| Generale | Indirizzo pubblico, lingua, tema, nome istanza | tutto |
| Sicurezza | API key (mascherata, Mostra/Copia/Rigenera), chiavi aggiuntive, autenticazione UI, **chiave del vault** (dove sta, esporta, avviso se è nella cartella dati) | tutto |
| Rete | Proxy, FlareSolverr (con "Prova"), richieste al secondo per sito | proxy, FlareSolverr |
| Aggiornamenti | Intervallo predefinito, conservazione release | intervallo |
| Regole | Ex pagina Regole | tutto |
| Backup | Backup ora, pianificazione, ripristino, elenco | tutto |
| Manutenzione | Dimensione DB, pulizia, report | tutto |
| Avanzate | Worker, timeout, breaker, volume factor, livello log, **Definizioni** (editor e test), import manuale | — |

Le Definizioni non sono più una voce di primo livello: sono uno strumento per
chi scrive definizioni, raggiungibile da Avanzate e dal dettaglio di un
indexer.

### 4.9 Primo avvio

Quando non ci sono indexer, la Home è sostituita da una procedura di 4 passi,
saltabile, riapribile da Impostazioni → Generale:

```text
Benvenuto in indexarr                                       Passo 1 di 4
1 Indirizzo     Le tue app ti raggiungono a:  [ http://192.168.1.50:8484 ]
                                               [Verifica] ✓ raggiungibile
2 Importa       Hai già Prowlarr o Jackett?  [Importa da Prowlarr] [No]
                (sola lettura: non modifichiamo nulla)
3 Indexer       Scegli i siti (ricerca di §4.3, selezione multipla)
4 App           Collega Sonarr/Radarr                   [Fine]
```

L'import (passo 2) usa WP8: crea direttamente indexer funzionanti, con
riepilogo "4 siti nativi, 8 via Prowlarr" e possibilità di togliere quelli
non voluti prima di confermare.

---

## 5. Componenti da creare una volta e riusare

Tutti in `templates/partials/` (o equivalente) + classi in `app.css`, con un
test che li renderizza:

| Componente | Uso | Note |
|---|---|---|
| `status-dot` | stato di indexer/app/client | 4 stati: ok, problema, disattivato, in prova; testo per screen reader |
| `problem` | riga problema con causa e azione | prende `Problem{Text, ActionLabel, ActionURL}` |
| `secret-field` | API key, password | mascherato, "Mostra", "Copia"; il valore non è nell'HTML finché non si preme "Mostra" (fetch) |
| `copy-field` | URL endpoint | pulsante Copia con conferma "Copiato" |
| `advanced` | `<details>` "Avanzate" | chiuso di default; aperto se contiene un errore di validazione |
| `empty` | stato vuoto | titolo, una frase, un pulsante |
| `next-step` | messaggio dopo un'azione | flash con pulsante |
| `category-picker` | categorie | caselle con nomi localizzati, mai ID |
| `site-picker` | ricerca sito (§4.3) | SSR + piccolo JS di filtro; funziona anche senza JS (lista completa) |
| `confirm` | azioni distruttive | dice cosa si perde ("Elimina ExtTo e 312 release?") |
| `.skip` | link "Salta al contenuto" | visibile solo al focus (V1) |

Niente framework JS: SSR, `fetch` per frammenti dove serve, progressive
enhancement. Le pagine devono restare utilizzabili senza JS.

---

## 6. Cosa serve dal resto del sistema

La UI semplice dipende da dati che oggi non ci sono o sono sparsi:

1. **`HomeStatus(ctx)`** in un solo punto (regola 7): legge health degli
   indexer (WP9), stato vault, app, client. Prima di WP9 usa l'ultimo run.
2. **Messaggi di problema**: una funzione `ExplainError(err) Problem` che
   traduce gli errori noti (429, 403, Cloudflare, credenziali, DNS, timeout,
   selettore vuoto) in frase + azione. Errori non riconosciuti: "Errore
   imprevisto" + link al log, mai il testo grezzo come titolo.
3. **Etichette delle definizioni localizzate.** I `settings` delle
   definizioni hanno `label`/`help` in inglese. Proposta: nomi standard
   (`username`, `password`, `apikey`, `passkey`, `cookie`, `uploader`,
   `language`) tradotti dalla UI con chiavi i18n `setting.<name>.label`;
   `label` della definizione come fallback. Nessun campo multilingua nel
   formato v1.
4. **Categorie con nomi**: la UI usa i nomi Torznab standard tradotti, non gli
   ID; la mappatura per sito resta nella definizione.
5. **Ricerca siti**: `SiteChoice` di WP14 legge catalogo + definizioni locali;
   corrispondenza su nome, `meta.urls` (host) e descrizione.
6. **Indirizzo visto dal browser** per proporre `public_url`.

---

## 7. Fasi per gli agenti

| Fase | Quando | Contenuto | File principali |
|---|---|---|---|
| **1b** | subito, in parallelo a WP-R | V1 skip link; V6 tabella Release dentro il contenitore e etichette "Indexer"/"Tipo di rip"; V7 "—" per valori sconosciuti; V8 API key mascherata + `secret-field`; stringhe inglesi di Impostazioni/Release in i18n; selettore lingua compatto nel piede | `layout.html`, `app.css`, `releases.html`, `settings.html`, locales |
| **2** | dopo WP16 | Elenco Indexer unico (§4.2), aggiunta con ricerca sito (§4.3), dettaglio (§4.4), componenti §5 | `indexers*.html`, nuovi partial, `internal/web/indexers*.go` |
| **3** | dopo fase 2 | Navigazione a 6 voci con redirect (§3), Home (§4.1), Impostazioni a sezioni (§4.8), App con più client (§4.6) | `layout.html`, `dashboard.html`, `settings*.html`, `applications*.html`, store per i client |
| **4** | dopo WP9 | `ExplainError`, problemi in Home/Indexer/Attività, primo avvio (§4.9), Attività con Ricerche e Grab | `internal/web`, `internal/indexer` |
| **5** | dopo fase 4 | Test di usabilità (WP14) con persona nuova, correzioni, `docs/UI-USABILITY.md` | — |

**Stato 1b (23/10): fatta** (report in revisione, non committata). Restano,
da fare insieme a WP-R o all'inizio della fase 2 (**1c**):

- Cerca: il titolo è troncato a ~280 px mentre quattro colonne mostrano solo
  "—"; applicare §4.5 (leechers e categoria in seconda riga, data relativa
  "2 h fa", azione "Scarica" invece del link "mag…" troncato, intestazioni
  non troncate). Filtri numerici vuoti invece di `0`. "Tutte le sorgenti" →
  "Tutti gli indexer".
- Impostazioni: anche token Telegram e password del client di download sono
  nell'HTML come `value` dei campi password: stessa regola 9 della API key
  (campo vuoto = invariato, "Cambia" per sostituirlo). Nell'endpoint mascherare
  solo la chiave, non l'indirizzo.
- Messaggi di validazione di `formInt`/`formFloat` e flash di `saved.go`
  ancora in inglese.
- TorrentGalaxy legacy produce release senza dimensione/seeders/categoria:
  non è un difetto UI; verificare che la definizione nativa di WP7 li
  estragga (test con la fixture) prima di WP8.

La fase 1b non deve investire nelle pagine che WP16 elimina (sorgenti,
tab legacy, form sorgente): lì basta la redazione degli URL di WP-R.

---

## 8. Come si verifica

Per ogni fase:

- `scripts/ui-smoke.sh` e `UI_SMOKE_STRICT=1` verdi, aggiornati alla nuova
  mappa.
- Test sui modelli (Go, non sul DOM): `HomeStatus` coerente con il badge
  della sidebar; form di aggiunta per una definizione senza campi
  obbligatori = solo Nome e Categorie; nessuna API key nell'HTML iniziale di
  Impostazioni; nessuna stringa letterale nei template.
- Screenshot prima/dopo a 1440 e 390 px delle pagine toccate, allegati al
  report (in una cartella temporanea, non committati), con istanza su copia
  del DB e porta diversa da :8484.
- Controllo larghezza: per ogni pagina `document.documentElement.scrollWidth
  <= innerWidth` e nessuna tabella più larga del suo contenitore.
- Dalla fase 3: i cinque compiti cronometrati di WP14.

**Non fare:** togliere informazioni diagnostiche (si spostano in
"Avanzate"), rompere URL esistenti senza redirect, introdurre framework JS o
build step per il frontend, mostrare segreti per comodità.
