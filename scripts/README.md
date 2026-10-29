# scripts

## Updating production

The upgrade script preserves the database and live data. By default it is a
**dry run**: it validates the prerequisites, builds `cmd/indexarr` into a
temporary directory and prints the plan, without stopping the service and
without installing anything.

Exact command, on a single line:

```bash
cd /path/to/indexarr && ./scripts/upgrade.sh --apply --yes
```

Before that, you can run the check without changes:

```bash
./scripts/upgrade.sh
```

What `--apply` does, in order:

1. builds the binary **before** touching the service; if the build fails it
   stops nothing;
2. creates a consistent DB backup with `VACUUM INTO` in
   `data/backups/pre-upgrade-<timestamp>.db` and saves the previous binary
   as `bin/indexarr.old-<timestamp>`;
3. stops the service, installs the new binary and restarts it;
4. checks `/healthz` with the freshly built version, `t=caps` and an
   authenticated Torznab search with the API key read from the DB (the key is
   never printed);
5. if a check after installation fails, it automatically restores the DB and
   the binary and restarts the previous service.

Rollback to the last pre-upgrade backup:

```bash
./scripts/upgrade.sh --rollback --yes
```

Options for a test on a copy: `--data-dir`, `--binary`, `--port 18510`,
`--no-systemd`, `--unit`. The test never uses `:8788`, `data/`,
`bin/indexarr` or systemd.

## Offline test

```bash
./scripts/upgrade_test.sh
```

Outcomes of the three tests:

- **successful upgrade**: builds the binary, creates the
  `pre-upgrade-*.db` and `indexarr.old-*` backup, checks `/healthz` with the
  new version, `t=caps` and a Torznab search, and preserves the API key;
- **broken build**: the failure of `cmd/indexarr` happens before stopping the
  service, without a backup and without modifying the binaries;
- **failed health check**: a wrong version in `/healthz` triggers the
  automatic restore of the DB and binaries and the restart of the previous
  service; the backup remains available.
