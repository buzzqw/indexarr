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
TEST_FLAGS=()
if [[ "${CI_SKIP_PRODUCTION_COPY:-0}" == "1" ]]; then
  TEST_FLAGS=(-skip TestProductionCopyHasNoFalseAlarms)
  echo "NOTE: TestProductionCopyHasNoFalseAlarms skipped (CI_SKIP_PRODUCTION_COPY=1)"
fi
go test "${TEST_FLAGS[@]}" ./...

TMP="$(mktemp -d "${TMPDIR:-/tmp}/indexarr-qa.XXXXXX")"
# Keep the vault key out of the developer's real user config: the vault resolves
# UserConfigDir()/indexarr/master.key when no old <db>.key file exists.
export XDG_CONFIG_HOME="$TMP/config"
mkdir -p "$XDG_CONFIG_HOME"
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
check / "Welcome to indexarr"
check /releases "Release"
check "/indexers" "Indexers"
check "/settings?section=maintenance" "Database report"
check "/settings?section=backup" "Backup"
check "/activity?tab=logs" "Log"
check "/activity?tab=logs&component=indexer" "Indexer"
check "/activity?tab=feed" "New releases"
check "/activity?tab=searches" "Searches"
check /healthz '"status":"ok"'
check /static/app.css "max-width: 720px"
check /static/manifest.webmanifest '"name": "indexarr"'
check /sw.js "indexarr-static"
check "/settings?section=advanced" "Definitions"
check /definitions/new "New definition"
check /indexers "Indexers"
check /indexers/new "Add indexer"
check /applications "Download clients"

# The startup log masks the key, so read it from the isolated QA database.
KEY="$(python3 - "$TMP/qa.db" <<'PYKEY'
import sqlite3, sys
print(sqlite3.connect(sys.argv[1]).execute("select value from settings where key='api_key'").fetchone()[0])
PYKEY
)"
if [[ -z "$KEY" ]]; then
  echo "FAIL: no Torznab API key in the QA database" >&2
  exit 1
fi
CAPS="$(curl -fsS "$BASE/api?t=caps&apikey=$KEY")"
if ! grep -q '<caps' <<<"$CAPS"; then
  echo "FAIL /api?t=caps: no <caps> element" >&2
  exit 1
fi
echo "ok   /api?t=caps"

NEWZNAB="$(curl -fsS "$BASE/newznab/api?t=caps&apikey=$KEY")"
if ! grep -q '<caps' <<<"$NEWZNAB"; then
  echo "FAIL /newznab/api?t=caps: no <caps> element" >&2
  exit 1
fi
echo "ok   /newznab/api?t=caps"

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
PER_INDEXER="$(curl -fsS "$BASE/torznab/qa-native/api?t=caps&apikey=$KEY")"
if ! grep -q '<caps' <<<"$PER_INDEXER"; then
  echo "FAIL /torznab/qa-native/api: no <caps>" >&2
  exit 1
fi
JACKETT_ALIAS="$(curl -fsS "$BASE/api/v2.0/indexers/qa-native/results/torznab/api?t=caps&apikey=$KEY")"
if ! grep -q '<caps' <<<"$JACKETT_ALIAS"; then
  echo "FAIL Jackett per-indexer alias: no <caps>" >&2
  exit 1
fi
echo "ok   per-indexer routes (torznab + Jackett alias)"
curl -fsS -X DELETE "$BASE/api/v2/indexers/$NATIVE_ID?apikey=$KEY" >/dev/null
echo "ok   /api/v2/indexers (vault create/delete)"

APP="$(curl -fsS -X POST "$BASE/api/v2/applications?apikey=$KEY" \
  -H 'Content-Type: application/json' \
  -d '{"kind":"sonarr","name":"QA Sonarr","url":"http://127.0.0.1:8989","api_key":"qa-app-key","sync_level":"disabled"}')"
if ! grep -q '"has_api_key":true' <<<"$APP"; then
  echo "FAIL /api/v2/applications create: $APP" >&2
  exit 1
fi
if grep -q 'qa-app-key' <<<"$APP"; then
  echo "FAIL /api/v2/applications leaked the API key" >&2
  exit 1
fi
APP_ID="$(grep -o '"id":[0-9]*' <<<"$APP" | head -n 1 | cut -d: -f2)"
if [[ -z "$APP_ID" ]]; then
  echo "FAIL /api/v2/applications create: no id" >&2
  echo "$APP" >&2
  exit 1
fi
SYNC="$(curl -fsS -X POST "$BASE/api/v2/applications/$APP_ID/sync?apikey=$KEY")"
if ! grep -q '"added":0' <<<"$SYNC"; then
  echo "FAIL /api/v2/applications sync: $SYNC" >&2
  exit 1
fi
curl -fsS -X DELETE "$BASE/api/v2/applications/$APP_ID?apikey=$KEY" >/dev/null
echo "ok   /api/v2/applications (vault + sync)"

EXPORT="$(curl -fsS "$BASE/indexers/export")"
if ! grep -q '"indexers"' <<<"$EXPORT"; then
  echo "FAIL /indexers/export: $EXPORT" >&2
  exit 1
fi
echo "ok   /indexers/export"

# Optional browser layer: it uses its own temporary instance and local
# fixtures, so it is independent of $BASE above. Without a browser it prints
# a SKIP line instead of failing.
if command -v google-chrome >/dev/null 2>&1 \
  || command -v google-chrome-stable >/dev/null 2>&1 \
  || command -v chromium >/dev/null 2>&1 \
  || command -v chromium-browser >/dev/null 2>&1; then
  echo "==> browser end-to-end (ui-e2e)"
  "$ROOT/scripts/ui-e2e.py" --binary "$TMP/indexarr"
else
  echo "==> browser end-to-end skipped: no Chrome/Chromium"
fi

echo "QA passed"
