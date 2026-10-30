#!/usr/bin/env bash
# package-release.sh — build the ready-to-run archives.
#
# Every archive is self-contained: unpack it and `./install.sh` starts a
# systemd user service, with no Go toolchain and nothing else to install.
#
#   scripts/package-release.sh                     # linux/amd64 + linux/arm64
#   scripts/package-release.sh --targets linux/amd64
#   scripts/package-release.sh --out /tmp/idx-dist --version v2.1.3
#
# Contents of indexarr-<version>-<os>-<arch>/:
#
#   bin/indexarr                     the static binary (CGO disabled)
#   install.sh                       wrapper to the installer
#   scripts/install-user-service.sh  the installer itself
#   examples/                        indexarr.json, the systemd unit, sample definitions
#   README.md, SUPPORTED.md (the generated source list), LICENSE (when present)
#   INSTALL.md                       the two commands to start from the archive
#
# Output: <out>/indexarr-<os>-<arch>.tar.gz, <out>/SHA256SUMS and
# <out>/indexarr-version (the version the archives were built from).
# <out> defaults to dist/ and is not committed.
set -euo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

OUT="${INDEXARR_DIST:-$ROOT/dist}"
VERSION="${VERSION:-}"
TARGETS=(linux/amd64 linux/arm64)

usage() {
	cat <<'TXT'
Usage: scripts/package-release.sh [options]

Options:
  --out DIR          Output directory (default: dist)
  --version VER      Version stamped into the binary (default: git describe)
  --targets LIST     Comma-separated GOOS/GOARCH pairs
                     (default: linux/amd64,linux/arm64)
  -h, --help         This help
TXT
}

die() {
	echo "error: $*" >&2
	exit 1
}

while (($#)); do
	case "$1" in
	--out)
		[[ $# -ge 2 ]] || die "--out requires a value"
		OUT="$2"
		shift 2
		;;
	--version)
		[[ $# -ge 2 ]] || die "--version requires a value"
		VERSION="$2"
		shift 2
		;;
	--targets)
		[[ $# -ge 2 ]] || die "--targets requires a value"
		IFS=',' read -r -a TARGETS <<<"$2"
		shift 2
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		die "unknown option: $1 (use --help)"
		;;
	esac
done

if [[ -z "$VERSION" ]]; then
	VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
fi
[[ "$VERSION" =~ ^[A-Za-z0-9._+-]+$ ]] || die "invalid version: $VERSION (letters, digits, . _ + - only)"

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
STAGE="${OUT}/.stage"
rm -rf "$STAGE"
mkdir -p "$STAGE"
# Old archives and checksums would otherwise mix versions in the same folder.
rm -f "${OUT}"/indexarr-*.tar.gz "${OUT}/SHA256SUMS" "${OUT}/indexarr-version"

# A fixed timestamp and sorted entries make two builds of the same commit
# produce the same bytes.
EPOCH="$(git log -1 --format=%ct 2>/dev/null || echo 0)"
[[ "$EPOCH" =~ ^[0-9]+$ ]] || EPOCH=0

build_one() {
	local target="$1"
	local os="${target%%/*}"
	local arch="${target##*/}"
	[[ -n "$os" && -n "$arch" && "$os" != "$target" ]] || die "invalid target: $target (expected GOOS/GOARCH)"
	local dir="${STAGE}/indexarr-${VERSION}-${os}-${arch}"

	echo "Building ${os}/${arch} ..."
	mkdir -p "${dir}/bin" "${dir}/scripts"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
		go build -buildvcs=false -trimpath \
		-ldflags "-s -w -X main.version=${VERSION}" \
		-o "${dir}/bin/indexarr" ./cmd/indexarr

	install -m 0755 install.sh "${dir}/install.sh"
	install -m 0755 scripts/install-user-service.sh "${dir}/scripts/install-user-service.sh"
	cp -a examples "${dir}/examples"
	cp README.md "${dir}/README.md"
	cp docs/SUPPORTED.md "${dir}/SUPPORTED.md"
	[[ -f LICENSE ]] && cp LICENSE "${dir}/LICENSE"
	cat >"${dir}/INSTALL.md" <<'MD'
# indexarr archive

This folder contains everything needed to run indexarr: the static binary, the
systemd installer and the example configuration. No Go toolchain is required.

```bash
./install.sh                      # systemd user service, data in ./data
./install.sh --listen 127.0.0.1:8788
sudo ./install.sh --system        # system service, data in /var/lib/indexarr
./install.sh --status
./install.sh --uninstall          # the data is kept
```

The installer uses `bin/indexarr` as it is. Open <http://localhost:8788>,
set a username and password in **Settings** before exposing it to the LAN, and
copy the Torznab URL for your apps. Docker users need the repository checkout
(`docker compose up -d --build`), not this folder.

Upgrades keep the database: see `scripts/upgrade.sh` in the repository, or
install a newer archive with the same command.
MD

	tar -C "$STAGE" \
		--sort=name --owner=0 --group=0 --numeric-owner --mtime="@${EPOCH}" \
		-czf "${OUT}/indexarr-${os}-${arch}.tar.gz" \
		"indexarr-${VERSION}-${os}-${arch}"
	echo "  ${OUT}/indexarr-${os}-${arch}.tar.gz"
}

for target in "${TARGETS[@]}"; do
	build_one "$target"
done

(
	cd "$OUT"
	sha256sum ./indexarr-*.tar.gz >SHA256SUMS
)
printf '%s\n' "$VERSION" >"${OUT}/indexarr-version"
rm -rf "$STAGE"

echo
echo "Version:  ${VERSION}"
echo "SHA256SUMS:"
sed 's/^/  /' "${OUT}/SHA256SUMS"
