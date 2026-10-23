#!/usr/bin/env bash
# End-to-end QA for indexarr.
#
# Runs static checks and the Go test suite, then starts a real binary against
# an isolated temporary database and verifies the main HTTP surfaces, the
# embedded mobile stylesheet and the Torznab caps response.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

echo "==> gofmt"
UNFORMATTED="$(gofmt -l .)"
if [[ -n "$UNFORMATTED" ]]; then
  echo "FAIL: unformatted Go files:" >&2
  echo "$UNFORMATTED" >&2
  exit 1
fi

echo "==> go vet"
go vet ./...

echo "==> go test"
go test ./...

TMP="$(mktemp -d "${TMPDIR:-/tmp}/indexarr-qa.XXXXXX")"
PORT="${INDEXARR_QA_PORT:-$((20000 + RANDOM % 30000))}"
BASE="http://127.0.0.1:$PORT"
LOG="$TMP/indexarr.log"
PID=""

cleanup() {
  if [[ -n "$PID" ]]; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

echo "==> build"
CGO_ENABLED=0 go build -trimpath -o "$TMP/indexarr" ./cmd/indexarr

cat >"$TMP/indexarr.json" <<JSON
{
  "listen": "127.0.0.1:$PORT",
  "db_path": "$TMP/qa.db",
  "log_level": "info"
}
JSON

"$TMP/indexarr" -config "$TMP/indexarr.json" >"$LOG" 2>&1 &
PID=$!

for _ in $(seq 1 100); do
  if curl -fsS "$BASE/healthz" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$PID" 2>/dev/null; then
    echo "FAIL: server exited during startup" >&2
    cat "$LOG" >&2
    exit 1
  fi
  sleep 0.1
done

if ! curl -fsS "$BASE/healthz" >/dev/null 2>&1; then
  echo "FAIL: server did not become ready on $BASE" >&2
  cat "$LOG" >&2
  exit 1
fi

check() {
  local path="$1"
  local want="$2"
  local body
  body="$(curl -fsS "$BASE$path")"
  if ! grep -q -- "$want" <<<"$body"; then
    echo "FAIL $path: missing $(printf '%q' "$want")" >&2
    exit 1
  fi
  echo "ok   $path"
}

echo "==> HTTP smoke tests"
check / "Dashboard"
check /releases "Releases"
check /sources "Quick add"
check /maintenance "Database report"
check /maintenance "Cleanup and retention"
check /healthz '"status":"ok"'
check /static/app.css "max-width: 720px"
check /static/manifest.webmanifest '"name": "indexarr"'
check /sw.js "indexarr-static"

KEY="$(grep -o 'apikey=[0-9a-f]\{64\}' "$LOG" | head -n 1 | cut -d= -f2)"
if [[ -z "$KEY" ]]; then
  echo "FAIL: no Torznab API key in the startup log" >&2
  cat "$LOG" >&2
  exit 1
fi
CAPS="$(curl -fsS "$BASE/api?t=caps&apikey=$KEY")"
if ! grep -q '<caps' <<<"$CAPS"; then
  echo "FAIL /api?t=caps: no <caps> element" >&2
  exit 1
fi
echo "ok   /api?t=caps"

echo "QA passed"
