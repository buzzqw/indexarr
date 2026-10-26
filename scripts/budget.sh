#!/usr/bin/env bash
# Resource budget check for indexarr (WP12).
#
# Two independent gates:
#
#   1. Binary size: the optimised linux/amd64 build
#      (CGO_ENABLED=0, -trimpath -ldflags "-s -w") must stay under
#      INDEXARR_BUDGET_BINARY_MB (default 25 MiB). This gate is hard.
#   2. Idle RSS: seed a disposable database, start the built binary on a free
#      loopback port, wait for /healthz, let it settle for
#      INDEXARR_BUDGET_RSS_SECONDS (default 60 s) and sample RSS with
#      `ps -o rss=`. The gate is Linux-only: on other systems it is skipped
#      with a clear message (there is no portable equivalent of `ps -o rss=`).
#
# The RSS policy has two levels: above 64 MiB the script prints a WARNING but
# succeeds, above 128 MiB it fails. Exactly 64 MiB is still OK. These are the
# coordinator's thresholds after the memory constraint was relaxed; the project
# still avoids unlimited caches and loads data in streams.
#
# Usage: scripts/budget.sh [--keep]
#   --keep                   keep the temporary directory and print its path
#   --check-rss-mib VALUE    only evaluate the RSS policy for a simulated
#                            value, then exit (used by the logic test)
#
# Environment:
#   INDEXARR_BUDGET_BINARY_MB      binary size ceiling in MiB      (default 25)
#   INDEXARR_BUDGET_RSS_MIB        RSS error ceiling in MiB        (default 128)
#   INDEXARR_BUDGET_RSS_WARN_MIB   RSS warning threshold in MiB    (default 64)
#   INDEXARR_BUDGET_RELEASES       releases in the seed database   (default 10000)
#   INDEXARR_BUDGET_RSS_SECONDS    settle time before sampling RSS (default 60)
#   INDEXARR_BUDGET_SKIP_RSS=1     only check the binary size
#   INDEXARR_BUDGET_KEEP=1         same as --keep
#
# The script never touches a running production instance: it uses a random
# high loopback port (never 8788) and a private temporary directory. The server
# process is always killed and the temporary data removed on exit.
set -euo pipefail
# Numbers must use a dot: awk comparisons and MiB conversion are locale
# sensitive and an Italian locale would print commas.
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BINARY_BUDGET_MIB="${INDEXARR_BUDGET_BINARY_MB:-25}"
RSS_BUDGET_MIB="${INDEXARR_BUDGET_RSS_MIB:-128}"
RSS_WARN_MIB="${INDEXARR_BUDGET_RSS_WARN_MIB:-64}"
SEED_RELEASES="${INDEXARR_BUDGET_RELEASES:-10000}"
RSS_SETTLE_SECONDS="${INDEXARR_BUDGET_RSS_SECONDS:-60}"
SKIP_RSS="${INDEXARR_BUDGET_SKIP_RSS:-0}"

for value in "$BINARY_BUDGET_MIB" "$RSS_BUDGET_MIB" "$RSS_WARN_MIB"; do
  [[ "$value" =~ ^[0-9]+([.][0-9]+)?$ ]] \
    || { echo "FAIL: budget must be a positive number, got '$value'" >&2; exit 2; }
done
[[ "$SEED_RELEASES" =~ ^[0-9]+$ ]] \
  || { echo "FAIL: INDEXARR_BUDGET_RELEASES must be an integer, got '$SEED_RELEASES'" >&2; exit 2; }
[[ "$RSS_SETTLE_SECONDS" =~ ^[0-9]+$ ]] \
  || { echo "FAIL: INDEXARR_BUDGET_RSS_SECONDS must be an integer, got '$RSS_SETTLE_SECONDS'" >&2; exit 2; }

# classify_rss <mib> prints ok, warn or fail. The comparison is strict: a value
# equal to the warning threshold does not warn, and equal to the ceiling does
# not fail.
classify_rss() {
  awk -v value="$1" -v warn="$RSS_WARN_MIB" -v max="$RSS_BUDGET_MIB" \
    'BEGIN { if (value > max) print "fail"; else if (value > warn) print "warn"; else print "ok" }'
}

# Test hook: no build and no server, just the policy decision. It keeps the
# warning/error boundary testable on any machine and in CI.
if [[ "${1:-}" == "--check-rss-mib" ]]; then
  SIMULATED="${2:-}"
  [[ "$SIMULATED" =~ ^[0-9]+([.][0-9]+)?$ ]] \
    || { echo "FAIL: --check-rss-mib needs a number, got '$SIMULATED'" >&2; exit 2; }
  case "$(classify_rss "$SIMULATED")" in
    ok)
      echo "OK: simulated RSS ${SIMULATED} MiB is at or below ${RSS_WARN_MIB} MiB"
      exit 0
      ;;
    warn)
      echo "WARNING: simulated RSS ${SIMULATED} MiB is above ${RSS_WARN_MIB} MiB but at or below ${RSS_BUDGET_MIB} MiB" >&2
      exit 0
      ;;
    *)
      echo "FAIL: simulated RSS ${SIMULATED} MiB is above ${RSS_BUDGET_MIB} MiB" >&2
      exit 1
      ;;
  esac
fi

KEEP=0
if [[ "${1:-}" == "--keep" || "${INDEXARR_BUDGET_KEEP:-0}" == "1" ]]; then
  KEEP=1
fi

TMP="$(mktemp -d "${TMPDIR:-/tmp}/indexarr-budget.XXXXXX")"
PID=""
cleanup() {
  if [[ -n "$PID" ]]; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
    PID=""
  fi
  if [[ "$KEEP" == "1" ]]; then
    echo "temporary directory: $TMP"
  else
    rm -rf "$TMP"
  fi
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

for tool in go curl ps awk; do
  command -v "$tool" >/dev/null 2>&1 || fail "required tool not found: $tool"
done

BIN="$TMP/indexarr"
DB="$TMP/indexarr.db"
CONFIG="$TMP/indexarr.json"
LOG="$TMP/indexarr.log"

# --- 1. Binary size -------------------------------------------------------

echo "==> build linux/amd64 (CGO_ENABLED=0, -trimpath -s -w)"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" -o "$BIN" ./cmd/indexarr

BIN_BYTES="$(wc -c <"$BIN" | tr -d ' ')"
BIN_MIB="$(awk -v b="$BIN_BYTES" 'BEGIN { printf "%.1f", b / 1048576 }')"

echo
echo "indexarr resource budget — $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
echo "host: $(uname -srm)"
echo "binary: ${BIN_MIB} MiB (${BIN_BYTES} bytes), limit ${BINARY_BUDGET_MIB} MiB"

SIZE_OK=1
if awk -v b="$BIN_BYTES" -v max="$BINARY_BUDGET_MIB" \
  'BEGIN { exit !(b / 1048576 > max) }'; then
  SIZE_OK=0
  echo "FAIL: binary is ${BIN_MIB} MiB > ${BINARY_BUDGET_MIB} MiB"
fi

# The RSS run costs a startup plus the settle window: do not pay for it when
# the size gate has already failed.
if [[ "$SIZE_OK" != "1" ]]; then
  exit 1
fi
echo "OK: binary size within budget"

if [[ "$SKIP_RSS" == "1" ]]; then
  echo "RSS budget skipped (INDEXARR_BUDGET_SKIP_RSS=1)"
  exit 0
fi
if [[ "$(uname -s)" != "Linux" ]]; then
  echo "RSS budget skipped: RSS sampling via 'ps -o rss=' is Linux-only on $(uname -s)"
  exit 0
fi

# --- 2. Seed a disposable database ---------------------------------------

echo
echo "==> seed ${SEED_RELEASES} releases (internal/perf/cmd/benchseed)"
SEED_START="$(date +%s)"
API_KEY="$(go run ./internal/perf/cmd/benchseed -db "$DB" -n "$SEED_RELEASES" 2>"$TMP/seed.log")"
SEED_SECONDS=$(( $(date +%s) - SEED_START ))
if [[ -z "$API_KEY" ]]; then
  cat "$TMP/seed.log" >&2
  fail "benchseed did not print an API key"
fi
if [[ ! -s "$DB" ]]; then
  cat "$TMP/seed.log" >&2
  fail "benchseed did not create $DB"
fi

# --- 3. Start the server on a free loopback port --------------------------

free_port() {
  local candidate
  local i
  for i in $(seq 1 50); do
    candidate=$((20000 + RANDOM % 30000))
    # /dev/tcp is a bash builtin: if the connect fails, the port is free.
    if ! (exec 3<>"/dev/tcp/127.0.0.1/$candidate") 2>/dev/null; then
      echo "$candidate"
      return 0
    fi
  done
  return 1
}

PORT="$(free_port)" || fail "cannot find a free loopback port"
cat >"$CONFIG" <<JSON
{
  "listen": "127.0.0.1:$PORT",
  "db_path": "$DB",
  "log_level": "warn"
}
JSON

echo "==> start server on 127.0.0.1:${PORT}"
START="$(date +%s)"
"$BIN" -config "$CONFIG" >"$LOG" 2>&1 &
PID=$!

READY=0
for _ in $(seq 1 300); do
  if curl -fsS --max-time 2 "http://127.0.0.1:${PORT}/healthz" >/dev/null 2>&1; then
    READY=1
    break
  fi
  if ! kill -0 "$PID" 2>/dev/null; then
    tail -n 20 "$LOG" >&2 || true
    fail "server exited before becoming ready"
  fi
  sleep 0.2
done
if [[ "$READY" != "1" ]]; then
  tail -n 20 "$LOG" >&2 || true
  fail "server did not answer /healthz within 60 s"
fi
STARTUP_SECONDS=$(( $(date +%s) - START ))

# One real Torznab query proves the seeded database is served, and avoids
# measuring a process that only opened its listener.
curl -fsS --max-time 5 \
  "http://127.0.0.1:${PORT}/api?t=search&apikey=${API_KEY}&limit=5" \
  >/dev/null

echo "==> settle for ${RSS_SETTLE_SECONDS}s before sampling RSS"
sleep "$RSS_SETTLE_SECONDS"

if ! kill -0 "$PID" 2>/dev/null; then
  tail -n 20 "$LOG" >&2 || true
  fail "server exited during the settle window"
fi

RSS_KB="$(ps -o rss= -p "$PID" | tr -d ' ')"
if [[ -z "$RSS_KB" ]]; then
  fail "cannot read RSS for pid $PID"
fi
RSS_MIB="$(awk -v k="$RSS_KB" 'BEGIN { printf "%.1f", k / 1024 }')"

echo
echo "database: ${SEED_RELEASES} releases seeded in ${SEED_SECONDS}s"
echo "startup: ${STARTUP_SECONDS}s, port ${PORT}"
echo "RSS: ${RSS_MIB} MiB (${RSS_KB} KiB), warning above ${RSS_WARN_MIB} MiB, limit ${RSS_BUDGET_MIB} MiB after ${RSS_SETTLE_SECONDS}s"

case "$(classify_rss "$RSS_MIB")" in
  ok)
    echo "OK: RSS within budget"
    echo
    echo "budget report: binary=${BIN_MIB} MiB/${BINARY_BUDGET_MIB} MiB ok, rss=${RSS_MIB} MiB/${RSS_BUDGET_MIB} MiB ok"
    ;;
  warn)
    echo "WARNING: RSS is ${RSS_MIB} MiB, above ${RSS_WARN_MIB} MiB (still below the ${RSS_BUDGET_MIB} MiB limit)" >&2
    echo
    echo "budget report: binary=${BIN_MIB} MiB/${BINARY_BUDGET_MIB} MiB ok, rss=${RSS_MIB} MiB/${RSS_BUDGET_MIB} MiB warning"
    ;;
  *)
    echo "FAIL: RSS is ${RSS_MIB} MiB > ${RSS_BUDGET_MIB} MiB"
    echo
    echo "budget report: binary=${BIN_MIB} MiB/${BINARY_BUDGET_MIB} MiB ok, rss=${RSS_MIB} MiB/${RSS_BUDGET_MIB} MiB OVER"
    exit 1
    ;;
esac
