#!/usr/bin/env bash
# version.sh — print the version stamped into the binaries.
#
# The version is the git description of the current commit against the
# versioned tags only (v2.1.3, v2.1.3-4-gabc1234). Rolling build tags such as
# "continuous" are deliberately ignored: they move at every commit and would
# turn the version of the next build into the tag name itself.
#
# Usage: scripts/version.sh [git-describe-args...]
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if git describe --tags --match 'v[0-9]*' --always --dirty "$@" 2>/dev/null; then
	exit 0
fi
echo dev
