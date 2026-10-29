# Vault and master key

The indexarr vault encrypts with AES-256-GCM the secrets of native indexers and
the credentials of the indexers (`api_key`, `password` and the URLs that can
contain them: on some trackers the URL contains the `rsskey`). The master key
**is not** in the database and **is not** included in the backups.

## Key resolution order

At startup the key is looked up in the following order:

1. `INDEXARR_MASTER_KEY` — value (base64 or 64-character hexadecimal);
2. `INDEXARR_MASTER_KEY_FILE` — path to a file, Docker secrets convention;
3. `vault_key_path` in `indexarr.json`;
4. `<db>.key` next to the database **only if it already exists** (compatibility
   with previous installations). A warning is emitted: move the key;
5. `os.UserConfigDir()/indexarr/master.key` (new default; usually
   `~/.config/indexarr/master.key`), created with `0600` permissions;
6. if the user configuration directory is not available (container without
   `HOME`), `<db>.key` next to the database, with a warning. In Docker, option 2
   is recommended.

The file created by indexarr contains 32 random bytes. A hand-written file can
contain 32 raw bytes, base64 (even without padding) or 64 hexadecimal characters.
The key never appears in logs, errors or the UI.

A relative `vault_key_path` is resolved relative to the directory of
`indexarr.json`.

## Commands

```bash
# Print the resolved key in base64 to stdout (the warning goes to stderr).
indexarr vault export-key -config /etc/indexarr/indexarr.json

# Move the old <db>.key file to a directory outside the data.
indexarr vault move-key -config /etc/indexarr/indexarr.json -to /etc/indexarr/master.key
```

Both commands accept `-db <path>` to override the database path. `move-key`
refuses to overwrite an existing file and creates the new file with `0600`
permissions: after moving it, set `vault_key_path` or
`INDEXARR_MASTER_KEY_FILE`.

## Backup and restore

The database backup (`backups/*.db`) contains only the database: the master key
must be saved separately (secret manager, Docker secret or offline copy of the
`master.key` file). To restore on another machine you need **both the database
and the key**.

After a restore, on first start indexarr verifies that the current key opens at
least one encrypted secret. If the key is wrong it writes an explicit error to
the log and keeps starting; indexers that have secrets remain unusable,
and the encrypted secrets cannot be read. The log never contains the value of the
secrets.

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

The data volume contains `indexarr.json` and the database, not the key. Anyone
who copies the volume without the secret gets a database whose secrets cannot be
decrypted.

## Indexer secrets

The indexer secrets live in `indexer_secrets`, encrypted with AES-256-GCM.
`indexersvc` decides which settings are secret (`api_key`, `apikey`,
`password`, `token`, `passkey`, `secret`, `cookie`) and never writes them to
`config_json`, in logs, in exports or in API responses. Without a vault,
indexers that require a secret cannot be created or updated; the rest of the
application keeps working.

The database backup contains the encrypted secrets but not the key: to restore on
another machine you need the same vault key.
