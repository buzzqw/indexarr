# scripts

## Installing

The installer is the same file for a user service and a system service; the
root `install.sh` is only a convenient path to it.

```bash
./install.sh                              # user service, build if Go is available
./install.sh --listen 127.0.0.1:8788      # local only
./install.sh --binary /tmp/indexarr       # prebuilt binary, no Go needed
./install.sh --dry-run                    # plan and unit, no changes
sudo ./install.sh --system                # /var/lib/indexarr, account indexarr
./install.sh --status
./install.sh --uninstall                  # data is kept
```

The user service writes `~/.config/systemd/user/<name>.service` and uses
`data/indexarr.json` unless `--data-dir` is given. `--system` writes
`/etc/systemd/system/<name>.service`, creates the service account when missing,
installs `/usr/local/bin/indexarr` and uses `/var/lib/indexarr`. Both modes
create the configuration with `0600` and the data directory with `0700`,
enable the unit, restart it and wait for `/healthz`.

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

## Packaging and the continuous release

`package-release.sh` builds the self-contained archives for `linux/amd64` and
`linux/arm64` in `dist/` (not committed). Each archive contains `bin/indexarr`,
`install.sh`, `scripts/install-user-service.sh`, `examples/` (configuration,
systemd unit, sample definitions), `README.md`, `SUPPORTED.md` (the generated
source list), `LICENSE` when present and a short `INSTALL.md`. Unpack it and `./install.sh` works with no Go toolchain:
the installer uses the shipped binary and only builds when it finds a source
tree (`Makefile`) plus Go and `make`.

```bash
./scripts/package-release.sh                      # both targets, version from git
./scripts/package-release.sh --targets linux/amd64
./scripts/package-release.sh --out /tmp/idx-dist --version v2.1.3
```

Output: `indexarr-linux-<arch>.tar.gz` (stable names), `SHA256SUMS` and
`indexarr-version`.

`publish-release.sh` packages the current commit and publishes it as the
rolling **continuous** release, a prerelease that never takes the `latest`
slot: the tag `continuous` is moved to the new commit and the assets are
replaced, so the download URLs stay the same.

```bash
./scripts/publish-release.sh                  # dry run: builds, uploads nothing
./scripts/publish-release.sh --yes            # move the tag, upload, update notes
./scripts/publish-release.sh --install-hook   # publish after every commit
```

`--install-hook` writes `.git/hooks/post-commit`: it publishes in the
background (`ci-logs/publish.log`), skips rebases and cherry-picks, and is
disabled with `INDEXARR_NO_PUBLISH=1`. A failed publish never blocks the
commit. The script refuses uncommitted tracked changes (`--allow-dirty` to
override), needs `gh` logged in and a lock so two runs do not race. The
stable URLs are
`https://github.com/buzzqw/indexarr/releases/download/continuous/indexarr-linux-<arch>.tar.gz`
and `.../SHA256SUMS`.
