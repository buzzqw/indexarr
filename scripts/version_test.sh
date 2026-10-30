#!/usr/bin/env bash
# Offline test for scripts/version.sh: the version comes from the versioned
# tags only, so the moving "continuous" tag never becomes a version.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/indexarr-version.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

fail() {
	echo "FAIL: $*" >&2
	exit 1
}

mkdir -p "${TMP}/repo/scripts"
cp "${ROOT}/scripts/version.sh" "${TMP}/repo/scripts/"
chmod 0755 "${TMP}/repo/scripts/version.sh"
cd "${TMP}/repo"
git init -q
git config user.email "test@example.com"
git config user.name "test"
git config commit.gpgsign false

printf 'a\n' >file
git add file
git commit -qm "one"

# No tag yet: the short hash.
got="$(scripts/version.sh)"
want="$(git rev-parse --short HEAD)"
[[ "$got" == "$want" ]] || fail "without tags: got '$got', want '$want'"

git tag v1.2.3
got="$(scripts/version.sh)"
[[ "$got" == "v1.2.3" ]] || fail "on the version tag: got '$got'"

# The rolling tag moves with every commit and must be ignored.
printf 'b\n' >>file
git commit -qam "two"
git tag continuous
got="$(scripts/version.sh)"
case "$got" in
v1.2.3-1-g*) ;;
*) fail "the continuous tag became the version: got '$got'" ;;
esac

# An uncommitted change is still visible.
printf 'c\n' >>file
got="$(scripts/version.sh)"
[[ "$got" == *-dirty ]] || fail "dirty tree without -dirty: got '$got'"

echo "version_test: ok"
