# Vault e chiave maestra

Il vault di indexarr cifra con AES-256-GCM i segreti degli indexer nativi e le
credenziali delle sorgenti legacy (`api_key`, `password` e l'intero URL, che su
alcuni tracker contiene la `rsskey`). La chiave maestra **non** sta nel database
e **non** è inclusa nei backup.

## Ordine di risoluzione della chiave

All'avvio la chiave viene cercata nell'ordine seguente:

1. `INDEXARR_MASTER_KEY` — valore (base64 o esadecimale a 64 caratteri);
2. `INDEXARR_MASTER_KEY_FILE` — percorso di un file, convenzione Docker secrets;
3. `vault_key_path` in `indexarr.json`;
4. `<db>.key` accanto al database **solo se esiste già** (compatibilità con le
   installazioni precedenti). Viene emesso un warning: spostare la chiave;
5. `os.UserConfigDir()/indexarr/master.key` (nuovo default; di solito
   `~/.config/indexarr/master.key`), creato con permessi `0600`;
6. se la directory di configurazione utente non è disponibile (container senza
   `HOME`), `<db>.key` accanto al database, con warning. In Docker si consiglia
   il punto 2.

Il file creato da indexarr contiene 32 byte casuali. Un file scritto a mano può
contenere 32 byte grezzi, base64 (anche senza padding) o 64 caratteri
esadecimali. La chiave non compare mai nei log, negli errori o nella UI.

`vault_key_path` relativo è risolto rispetto alla directory di `indexarr.json`.

## Comandi

```bash
# Stampa la chiave risolta in base64 su stdout (l'avviso va su stderr).
indexarr vault export-key -config /etc/indexarr/indexarr.json

# Sposta il file legacy <db>.key in una directory fuori dai dati.
indexarr vault move-key -config /etc/indexarr/indexarr.json -to /etc/indexarr/master.key
```

Entrambi i comandi accettano `-db <path>` per sovrascrivere il percorso del
database. `move-key` rifiuta di sovrascrivere un file esistente e crea il nuovo
file con permessi `0600`: dopo lo spostamento impostare `vault_key_path` o
`INDEXARR_MASTER_KEY_FILE`.

## Backup e restore

Il backup del database (`backups/*.db`) contiene solo il database: la chiave
maestra va salvata separatamente (secret manager, Docker secret o copia offline
del file `master.key`). Per ripristinare su un'altra macchina servono **database
e chiave**.

Dopo un restore, al primo avvio indexarr verifica che la chiave corrente apra
almeno un segreto cifrato. Se la chiave è sbagliata lo scrive nel log con un
errore esplicito e continua ad avviarsi: il percorso legacy resta disponibile,
ma i segreti cifrati non sono leggibili. Il log non contiene mai il valore dei
segreti.

## Docker

```yaml
services:
  indexarr:
    environment:
      INDEXARR_MASTER_KEY_FILE: /run/secrets/indexarr_master_key
    secrets:
      - indexarr_master_key
secrets:
  indexarr_master_key:
    file: ./indexarr_master_key
```

Il volume dati contiene `indexarr.json` e il database, non la chiave. Chi copia
il volume senza il secret ottiene un database i cui segreti non sono
decifrabili.

## Segreti delle sorgenti legacy

Con un vault disponibile, `CreateSource`/`UpdateSource` cifrano URL, API key e
password con il prefisso versionato `vault:v1:`. All'avvio la migrazione
`SealLegacySecrets` cifra una volta i valori ancora in chiaro; è idempotente.
Senza chiave i valori restano in chiaro e il percorso legacy continua a
funzionare: la cifratura non è un requisito per l'avvio.

## Cosa non fa

- Non protegge da un attaccante con accesso completo alla macchina.
- Non ruota la chiave e non cifra il database: cifra i singoli campi sensibili.
- Non include la chiave nei backup né nelle risposte HTTP.
