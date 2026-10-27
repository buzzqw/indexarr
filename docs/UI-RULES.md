# Regole dell'interfaccia

Queste regole valgono per ogni pagina dell'interfaccia web di indexarr e per
ogni modifica ai template. La mappa delle route e dei redirect è in
[internal/UI-MAP.md](internal/UI-MAP.md); `scripts/ui-smoke.sh` la verifica.

## 1. Regole

1. **Un concetto: l'indexer, cioè un sito.** Le parole *definizione*,
   *famiglia*, *connettore*, *slug*, *SSRF* non compaiono fuori da "Avanzate"
   e dagli strumenti per chi scrive definizioni.
2. **Un'azione principale per pagina**, in alto a destra, pulsante pieno. Le
   altre azioni sono secondarie (contorno) o in un menu "⋯".
3. **Al massimo 5 campi visibili in un form.** Il resto va in un blocco
   `<details>` "Avanzate", chiuso, con valori già sensati.
4. **Mai chiedere ciò che si può dedurre.** Protocollo, nome nell'URL,
   categorie, URL di un sito noto, user agent: si derivano dalla definizione.
5. **Ogni pagina vuota dice cosa fare** con un pulsante, non con un paragrafo.
6. **Ogni errore ha causa e rimedio in una frase**, con l'azione a portata di
   clic ("Il sito risponde 429: troppe richieste. [Riduci la frequenza]"). Il
   rimedio cambia solo la fonte che ha il problema.
7. **Un numero, una fonte.** Se due parti della pagina parlano dello stesso
   stato leggono lo stesso valore dal modello. Niente "169 errori" e "tutto
   ok" sulla stessa schermata.
8. **I dati mancanti si scrivono "—"**, mai `0`, `?` o vuoto ambiguo.
9. **I segreti non si mostrano finché l'utente non li chiede** (pulsante
   "Mostra" o "Copia"). Mai nei link, nei title, negli URL di esempio.
10. **Ogni flusso finisce con il passo successivo**: messaggio di conferma e
    pulsante verso la cosa che verosimilmente serve dopo.
11. **Problema ≠ caratteristica.** Rosso, triangoli e "Problemi" solo per
    errori, login scaduto, sito irraggiungibile o zero risultati quando il
    sito dovrebbe averne. Data, dimensione, seeder, magnet o infohash mancanti
    sono caratteristiche del sito: stanno in una sezione neutra "Cosa fornisce
    questo sito", mai nella Home o nel badge. Una fonte lenta che finisce in
    background non è un errore.
12. **Categorie per nome.** L'utente non vede mai "Category N" o un ID nudo:
    le categorie standard si mostrano raggruppate per famiglia con il nome, e
    il numero solo sotto "Avanzate".
13. **Una barra azioni.** Nelle schede e nei form i bottoni stanno sulla
    stessa linea di base; l'azione primaria è a destra, le secondarie a
    sinistra; sotto 600 px vanno in colonna a tutta larghezza. Nelle tabelle
    le azioni hanno una colonna allineata. Bottoni, link-bottone e select usano
    un solo token di altezza.
14. **Un paragrafo esplicativo in fondo a ogni pagina.** Ogni pagina con una
    funzione ha, sotto il contenuto, il componente `page-help`: tradotto in
    tutte e 7 le lingue, comprimibile (`<details>`), aperto di default solo la
    prima volta e ricordato per browser (`localStorage`, chiave
    `indexarr.help.<pagina>`). Dice in parole semplici **a cosa serve** la
    pagina, **come si usa** e **perché**. Le chiavi sono `pageHelp.<pagina>`;
    un test fallisce se una pagina o una lingua non le ha. Ogni campo, badge,
    stato e bottone non ovvio ha un `title` preso dal catalogo, non testo
    letterale.
15. **Non si toglie niente.** Una pagina riscritta contiene almeno tutto
    quello che c'era: funzioni, pulsanti, form, campi, link, colonne, tooltip
    e testi di aiuto. Ogni azione gestita da un handler è raggiungibile dalla
    UI.

Regole di testo:

- Pulsanti con un verbo: "Aggiungi indexer", "Prova", "Salva", "Collega
  Sonarr". Mai "OK", "Invia", "Submit".
- Frasi brevi, seconda persona, niente gergo. Il testo d'aiuto sta sotto il
  campo, una riga.
- Tutte le stringhe passano da `T` (i18n) e sono tradotte davvero in it, en,
  de, fr, es, pl, zh. Nessun testo letterale nei template.
- I log e le notifiche parlano in frasi: cosa è successo, cosa non va, cosa
  fare. Il testo tecnico resta nei dettagli.

## 2. Struttura del menu

| Voce | Contenuto |
|---|---|
| Home | Stato, endpoint da copiare, prossimo passo |
| Cerca | Ricerca nelle release |
| Indexer | Elenco unico dei siti, dettaglio, aggiunta guidata dal catalogo |
| App | App \*arr e client di download |
| Attività | Esecuzioni, release nuove, ricerche ricevute |
| Log | Log interni, richieste delle app, statistiche, download |
| Impostazioni | Sezioni, regole, manutenzione, definizioni |

Gli URL vecchi rispondono con un redirect verso la nuova posizione. Il badge
accanto a una voce compare solo quando c'è qualcosa da fare.

## 3. Aggiunta di un indexer

Il gesto naturale è "cerco il sito": prima la ricerca nel catalogo, poi un
form piccolo.

- Il form mostra **solo** i `settings` della definizione con `required: true`,
  più Nome (precompilato), Categorie (caselle con i nomi, precompilate dalla
  definizione) e Frequenza (0 o vuoto usa il predefinito; minimo 60 secondi).
- Non mostra mai il protocollo, l'URL di un sito con `meta.urls` (si sceglie
  da un menu in Avanzate solo se ce n'è più d'uno) né ID numerici di
  categoria.
- Un indirizzo incollato apre la definizione verificata che lo riconosce,
  oppure il form Torznab/Newznab (`/api`, `t=caps`) o il form Feed (RSS/Atom).
- **Aggiungi e prova** salva, esegue subito il test per fasi (raggiungibile,
  ricerca, download) e porta al dettaglio con il risultato visibile.
- L'ordine dei campi è quello di questo elenco, non quello del file di
  definizione.

## 4. Componenti riusati

| Componente | Uso | Note |
|---|---|---|
| `status-dot` | stato di indexer, app, client | 5 stati: ok, degradato, problema, disattivato, in prova; testo per screen reader |
| `problem` | riga problema con causa e azione | `Problem{Text, ActionLabel, ActionURL}` |
| `secret-field` | API key, password | mascherato, "Mostra", "Copia"; il valore non è nell'HTML finché non si preme "Mostra" |
| `copy-field` | URL degli endpoint | pulsante Copia con conferma "Copiato" |
| `advanced` | `<details>` "Avanzate" | chiuso di default; aperto se contiene un errore di validazione |
| `empty` | stato vuoto | titolo, una frase, un pulsante |
| `next-step` | messaggio dopo un'azione | flash con pulsante |
| `category-picker` | categorie | caselle con nomi localizzati, mai ID; Adulti non spuntato di default |
| `action-bar` | azioni di form e schede | primaria a destra, secondarie a sinistra, stessa altezza; colonna sotto 600 px |
| `site-picker` | ricerca del sito | rendering lato server e un piccolo filtro JS; funziona anche senza JS |
| `confirm` | azioni distruttive | dice cosa si perde ("Elimina ExtTo e 312 release?") |
| `log_row` | una riga di log | frase, area, "Cosa non va", "Cosa fare", dettagli tecnici richiudibili |
| `.skip` | link "Salta al contenuto" | visibile solo al focus |

Niente framework JS: rendering lato server, `fetch` per i frammenti dove
serve, miglioramento progressivo. Le pagine restano utilizzabili senza JS.

## 5. Come si verifica

- `make ci` esegue i test dei template (paragrafo d'aiuto, traduzioni,
  tooltip, form della manutenzione), lo smoke test delle route e il test nel
  browser (`scripts/ui-e2e.py`), compresa la larghezza di 390 px senza
  scorrimento orizzontale e i bersagli tattili di almeno 44 px.
- Per ogni template toccato si confrontano prima e dopo i valori `action=`,
  `name=` e `value=`, i form, i paragrafi `<p>`, i `title` e i link: nessun
  conteggio deve scendere.
