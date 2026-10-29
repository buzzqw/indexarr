# scripts

## Aggiornamento della produzione

Lo script di aggiornamento conserva il database e i dati vivi. Di default è un
**dry run**: valida i prerequisiti, compila `cmd/indexarr` in una directory
temporanea e stampa il piano, senza fermare il servizio e senza installare
nulla.

Comando esatto, su una sola riga:

```bash
cd /percorso/di/indexarr && ./scripts/upgrade.sh --apply --yes
```

Prima si può lanciare il controllo senza modifiche:

```bash
./scripts/upgrade.sh
```

Cosa fa `--apply`, nell'ordine:

1. compila il binario **prima** di toccare il servizio; se il build fallisce
   non ferma nulla;
2. crea un backup coerente del DB con `VACUUM INTO` in
   `data/backups/pre-upgrade-<timestamp>.db` e salva il binario precedente
   come `bin/indexarr.old-<timestamp>`;
3. ferma il servizio, installa il nuovo binario e lo riavvia;
4. verifica `/healthz` con la versione appena compilata, `t=caps` e una ricerca
   Torznab autenticata con la API key letta dal DB (la chiave non viene mai
   stampata);
5. se un controllo dopo l'installazione fallisce, ripristina automaticamente
   DB e binario e riavvia il servizio precedente.

Rollback all'ultimo backup pre-aggiornamento:

```bash
./scripts/upgrade.sh --rollback --yes
```

Opzioni per una prova su una copia: `--data-dir`, `--binary`, `--port 18510`,
`--no-systemd`, `--unit`. La prova non usa mai `:8788`,
`data/`, `bin/indexarr` o systemd.

## Test offline

```bash
./scripts/upgrade_test.sh
```

Esiti delle tre prove:

- **aggiornamento riuscito**: costruisce il binario, crea il backup
  `pre-upgrade-*.db` e `indexarr.old-*`, verifica `/healthz` con la versione
  nuova, `t=caps` e una ricerca Torznab, e conserva la API key;
- **build rotto**: il fallimento di `cmd/indexarr` avviene prima di fermare il
  servizio, senza backup e senza modificare i binari;
- **controllo health fallito**: una versione sbagliata in `/healthz` provoca il
  ripristino automatico di DB e binari e il riavvio del servizio precedente; il
  backup resta disponibile.
