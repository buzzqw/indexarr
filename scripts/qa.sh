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
check /logs "Problems"
check "/logs?component=indexer" "Origin"
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

echo "==> admin API v2"
HEALTH="$(curl -fsS "$BASE/api/v2/health?apikey=$KEY")"
if ! grep -q '"status":"ok"' <<<"$HEALTH"; then
  echo "FAIL /api/v2/health: $HEALTH" >&2
  exit 1
fi
echo "ok   /api/v2/health"

DEFS="$(curl -fsS "$BASE/api/v2/definitions?apikey=$KEY")"
if ! grep -q '"example-html"' <<<"$DEFS"; then
  echo "FAIL /api/v2/definitions: built-in catalog missing" >&2
  echo "$DEFS" >&2
  exit 1
fi
echo "ok   /api/v2/definitions (catalog)"

UNAUTH="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/api/v2/definitions")"
if [[ "$UNAUTH" != "401" ]]; then
  echo "FAIL /api/v2/definitions without key = $UNAUTH, want 401" >&2
  exit 1
fi
echo "ok   /api/v2 auth"

CREATE="$(curl -fsS -X POST "$BASE/api/v2/indexers?apikey=$KEY" \
  -H 'Content-Type: application/json' \
  -d '{"definition_id":"newznab-generic","name":"QA Native","settings":{"url":"http://127.0.0.1:1/api","apikey":"qa-secret-value"}}')"
if ! grep -q '"has_secrets":true' <<<"$CREATE"; then
  echo "FAIL /api/v2/indexers create: $CREATE" >&2
  exit 1
fi
if grep -q 'qa-secret-value' <<<"$CREATE"; then
  echo "FAIL /api/v2/indexers leaked the secret" >&2
  exit 1
fi
NATIVE_ID="$(grep -o '"id":[0-9]*' <<<"$CREATE" | head -n 1 | cut -d: -f2)"
if [[ -z "$NATIVE_ID" ]]; then
  echo "FAIL /api/v2/indexers create: no id" >&2
  echo "$CREATE" >&2
  exit 1
fi
curl -fsS -X DELETE "$BASE/api/v2/indexers/$NATIVE_ID?apikey=$KEY" >/dev/null
echo "ok   /api/v2/indexers (vault create/delete)"

echo "QA passed"
