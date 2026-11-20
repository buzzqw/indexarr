# Installation

Four ways to run indexarr. The first three need no Go toolchain; building from
source is for development.

| Path | When to choose it | Command |
| --- | --- | --- |
| **Prebuilt archive** | you only want to run it | download, unpack, `./install.sh` |
| **install.sh** | from a checkout | `./install.sh` |
| **Docker** | container, NAS, everything in one volume | `docker compose up -d --build` |
| **From source** | development, or a host without systemd | `make build && ./bin/indexarr` |

## Requirements

- **Linux with systemd** to use `install.sh`, or **Docker with Compose** for
  the container image. The binary itself has no runtime dependencies.
- **Go 1.26 or later** only to build from source.
- A writable filesystem for the database.
- Optional: FlareSolverr for Cloudflare, a torrent/Usenet/eD2k client for
  sending releases.

## From a prebuilt archive

Ready-to-run archives of the current code are published manually on the
[continuous release](https://github.com/buzzqw/indexarr/releases/tag/continuous):
`indexarr-linux-amd64.tar.gz` and `indexarr-linux-arm64.tar.gz`. Publishing is
not automatic; a maintainer refreshes the rolling release with
`make publish PUBLISH_ARGS=--yes` (or after every commit, with the opt-in
`scripts/publish-release.sh --install-hook`). There is nothing to compile and
nothing to clone:

```bash
curl -LO https://github.com/buzzqw/indexarr/releases/download/continuous/indexarr-linux-amd64.tar.gz
curl -LO https://github.com/buzzqw/indexarr/releases/download/continuous/SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
tar xzf indexarr-linux-amd64.tar.gz
cd indexarr-*-linux-amd64
./install.sh
```

The archive holds the static binary, `install.sh`, the installer in
`scripts/`, the systemd unit, the example definitions and this documentation in
`docs/`; `INSTALL.md` repeats the two commands. Right after a refresh GitHub's
CDN can still serve the previous archive for about a minute; `sha256sum -c`
tells you which one you got, download it again if the checksum fails. It is the
**current code**, not a fixed version: for production pin a versioned release
when there is one, or update an existing installation from a newer archive.

## Install with install.sh

Clone the repository, then from its root run the installer. One command
builds the binary (when Go and `make` are available, otherwise it installs an
existing `bin/indexarr` or the file given with `--binary`), installs a
systemd unit, starts it and checks `/healthz`:

```bash
git clone https://github.com/buzzqw/indexarr.git
cd indexarr
./install.sh
./install.sh --listen 127.0.0.1:8788   # local only
./install.sh --binary /tmp/indexarr    # prebuilt binary, no Go needed
./install.sh --system                  # system service in /var/lib/indexarr
./install.sh --status
./install.sh --uninstall               # data is kept
```

By default it is a **user service** started by the account that runs the
installer, without `sudo`; the data directory is `<repo>/data` and is never
deleted by `--uninstall`. `--system` installs a system-wide unit for the
dedicated `indexarr` account with data in `/var/lib/indexarr`, and
`--linger` keeps the user service running after logout.

## Install with Docker

From the same checkout the image is built locally and started in the
background:

```bash
docker compose up -d --build
```

The page is on port **8788**, the container runs as the non-root UID 10001 and
the database, the configuration and the vault key live in the named volume
`indexarr-data`. `INDEXARR_PORT` changes the host port, `INDEXARR_VERSION`
tags the image (Compose defaults to `dev`; `make docker` uses the version from
`scripts/version.sh`). To keep the vault key outside the volume, mount it and
set `INDEXARR_MASTER_KEY_FILE` (the line is already in `docker-compose.yml`).
The prebuilt archives are for `install.sh`, not a container image: Docker
builds from the checkout.

## Upgrading and rollback

`scripts/upgrade.sh` updates a live installation without wiping the database:
dry run by default, `--apply --yes` to execute, `--rollback --yes` to restore
the latest backup. Reinstalling a newer archive with `./install.sh` keeps the
data directory too. With Docker, rebuild the image and recreate the service
(`docker compose up -d --build`); the volume is untouched.

Installer flags, production upgrade and rollback, the system-wide unit and
Docker: [Installation](https://github.com/buzzqw/indexarr/wiki/Installation).

## From source

For development. To run indexarr for real, use `install.sh` or Docker as shown
above.

```bash
make build
./bin/indexarr
```

On first start, in the current directory, the following are created:

- `indexarr.json` — minimal configuration (port, database, logs);
- `indexarr.db` — SQLite database;
- a random Torznab API key, printed in the log and visible in **Settings**.

Open <http://localhost:8788>, add a source and press **Run**. In **Settings**
you find the Torznab URL to give to the apps.

Available flags:

```text
-config string   JSON configuration file (default "indexarr.json")
-listen string   HTTP address, overrides the file
-db string       SQLite path, overrides the file
-version         print the version
```

## Configuration

`indexarr.json` contains only the startup settings: `listen`, `db_path`,
`vault_key_path`, `memory_limit_mb` and the log options. Everything else —
API keys, intervals, retention, UI credentials, notifications, clients,
backups — is in the database and is changed from the **Settings** page, without
restarting.

`memory_limit_mb` is a soft cap on the Go heap, not on the resident set: the
binary, the SQLite driver pools and the runtime add about 40 MB, and the
daemon hands the free pages of a burst back to the operating system every five
minutes. On a host with transparent huge pages set to `always`, the installer
and the container also set `GODEBUG=disablethp=1`, which keeps the small heap
on ordinary pages instead of rounding it up to 2 MiB pages.

The file, the vault key lookup and every setting with its default:
[Configuration](https://github.com/buzzqw/indexarr/wiki/Configuration).
