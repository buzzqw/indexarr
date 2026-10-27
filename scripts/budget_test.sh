#!/usr/bin/env bash
# Policy test for scripts/budget.sh. It never builds or starts the server: the
# --check-rss-mib hook evaluates the warning/error boundary directly.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUDGET="$ROOT/scripts/budget.sh"

check() {
  local value="$1" want_status="$2" want_text="$3"
  local output status
  set +e
  output="$("$BUDGET" --check-rss-mib "$value" 2>&1)"
  status=$?
  set -e
  if [[ "$status" != "$want_status" ]]; then
    echo "FAIL: ${value} MiB exited ${status}, want ${want_status}: ${output}" >&2
    exit 1
  fi
  if ! grep -q -- "$want_text" <<<"$output"; then
    echo "FAIL: ${value} MiB output missing ${want_text}: ${output}" >&2
    exit 1
  fi
  echo "ok   ${value} MiB -> exit ${status} (${want_text})"
}

check 32 0 "OK"
check 64 0 "OK"
check 64.1 0 "WARNING"
check 96 0 "WARNING"
check 128 0 "WARNING"
check 256 0 "WARNING"
check 256.1 1 "FAIL"

echo "budget policy test passed"
