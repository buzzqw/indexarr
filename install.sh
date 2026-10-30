#!/usr/bin/env bash
# Convenience entry point: the installer lives in scripts/install-user-service.sh
# but the common case is a single command from the repository root.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "${ROOT}/scripts/install-user-service.sh" "$@"
