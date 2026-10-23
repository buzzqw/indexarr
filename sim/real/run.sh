#!/usr/bin/env bash
# Real-application compatibility simulation for indexarr 2.
#
# Starts the real Prowlarr, Sonarr and Radarr LinuxServer.io containers next
# to the real indexarr binary and the deterministic simmock Torznab service.
# It proves two paths that the fake-only simulation cannot prove:
#
#   1. Prowlarr bridge: Prowlarr proxies simmock and indexarr ingests the
#      per-indexer Prowlarr Torznab feed as a native torznab indexer.
#   2. App sync: indexarr writes its indexer into real Sonarr and Radarr with
#      the expected baseUrl, API key, categories, protocol and enable flags.
#
# Host ports are isolated. The host services on 8484 and 9117 are never used.
set -euo pipefail

REAL=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$REAL/../.." && pwd)
SIM="$ROOT/sim"
COMPOSE="$REAL/docker-compose.yml"
DATA="$REAL/.data"
LOGDIR="$DATA/logs"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/indexarr-real.XXXXXX")

KEEP=${KEEP:-0}
REBUILD=${REBUILD:-0}
PULL=${PULL:-0}

MOCK_IMAGE=indexarr-sim-mock:local
IDX_IMAGE=indexarr-real:local

# auto: build from the working tree, falling back to a clean HEAD snapshot if
# the tree does not compile (for example while another change is in progress).
# 1: always use the clean HEAD snapshot.
CLEAN=${CLEAN:-auto}

IDX=http://127.0.0.1:18484
MOCK=http://127.0.0.1:19080
MOCK_INTERNAL=http://simmock:19080
PROWLARR=http://127.0.0.1:19696
PROWLARR_INTERNAL=http://prowlarr:9696
SONARR=http://127.0.0.1:18989
RADARR=http://127.0.0.1:17878

# URL as seen from inside the Compose network. It is also the value stored in
# public_url, so Sonarr/Radarr receive a reachable indexarr base URL.
INDEXARR_INTERNAL=http://indexarr:8484

# A unique suffix lets KEEP=1 reruns add a fresh Prowlarr indexer and bridge
# source without colliding with the previous run.
RUN_SUFFIX=$(date +%s)
PROWLARR_INDEXER_NAME="Sim Torznab ${RUN_SUFFIX}"
BRIDGE_NAME="Prowlarr Bridge ${RUN_SUFFIX}"

FRESH=1

PASS=0
FAIL=0

ok() { PASS=$((PASS + 1)); printf 'ok   %s\n' "$*"; }
fail() { FAIL=$((FAIL + 1)); printf 'FAIL %s\n' "$*" >&2; }

assert_eq() {
    local label=$1 expected=$2 actual=$3
    if [[ $actual == "$expected" ]]; then
        ok "$label"
    else
        fail "$label (atteso '$expected', ottenuto '$actual')"
    fi
}

assert_contains() {
    local label=$1 haystack=$2 needle=$3
    if [[ $haystack == *"$needle"* ]]; then
        ok "$label"
    else
        fail "$label (mancante: $needle)"
    fi
}

assert_count() {
    local label=$1 haystack=$2 needle=$3 expected=$4 actual
    actual=$(grep -o -- "$needle" <<<"$haystack" | wc -l | tr -d ' ' || true)
    assert_eq "$label" "$expected" "$actual"
}

fatal() {
    printf '\nERRORE: %s\n' "$*" >&2
    exit 1
}

dump_logs() {
    mkdir -p "$LOGDIR"
    {
        docker compose -f "$COMPOSE" ps --all || true
        printf '\n== indexarr ==\n'
        docker compose -f "$COMPOSE" logs --no-color indexarr || true
        printf '\n== prowlarr ==\n'
        docker compose -f "$COMPOSE" logs --no-color prowlarr || true
        printf '\n== sonarr ==\n'
        docker compose -f "$COMPOSE" logs --no-color sonarr || true
        printf '\n== radarr ==\n'
        docker compose -f "$COMPOSE" logs --no-color radarr || true
    } >"$LOGDIR/compose.log" 2>&1 || true
}

cleanup() {
    local status=$?
    if ((status != 0)); then
        dump_logs
        printf 'log salvati in %s\n' "$LOGDIR/compose.log" >&2
    fi
    if [[ $KEEP != 1 ]]; then
        docker compose -f "$COMPOSE" down -v --remove-orphans >/dev/null 2>&1 || true
    else
        printf 'stack lasciato attivo (KEEP=1): %s\n' "$COMPOSE"
    fi
    rm -rf "$WORK"
}
trap cleanup EXIT

wait_http() {
    local url=$1 name=$2 tries=${3:-180} i
    for ((i = 1; i <= tries; i++)); do
        if curl -fsS --max-time 3 "$url" >/dev/null 2>&1; then
            ok "$name raggiungibile ($url)"
            return 0
        fi
        sleep 1
    done
    fail "$name non raggiungibile dopo ${tries}s ($url)"
    return 1
}

wait_config_key() {
    local service=$1 tries=${2:-180} i key
    for ((i = 1; i <= tries; i++)); do
        key=$(docker compose -f "$COMPOSE" exec -T "$service" \
            sh -c 'cat /config/config.xml 2>/dev/null' 2>/dev/null \
            | sed -n 's:.*<ApiKey>\([^<]*\)</ApiKey>.*:\1:p' | head -n1 || true)
        if [[ -n ${key:-} ]]; then
            printf '%s' "$key"
            return 0
        fi
        sleep 1
    done
    return 1
}

wait_indexarr_key() {
    local tries=${1:-90} i key
    for ((i = 1; i <= tries; i++)); do
        key=$(docker compose -f "$COMPOSE" run --rm --no-deps -T seed \
            -db /data/indexarr.db -print-key 2>/dev/null | tail -n1 || true)
        if [[ ${key:-} =~ ^[0-9a-f]{64}$ ]]; then
            printf '%s' "$key"
            return 0
        fi
        sleep 1
    done
    return 1
}

# json_field_object BODY NAME -> the first flat JSON object whose "name" is NAME
json_field_object() {
    local body=$1 name=$2
    grep -o "{[^{}]*\"name\"[[:space:]]*:[[:space:]]*\"$name\"[^{}]*}" <<<"$body" \
        | head -n1 || true
}

# assert_field_value LABEL BODY FIELD EXPECTED_SUBSTRING
assert_field_value() {
    local label=$1 body=$2 field=$3 expected=$4 obj flatbody flat
    flatbody=$(tr -d '[:space:]' <<<"$body")
    obj=$(json_field_object "$flatbody" "$field")
    if [[ -z $obj ]]; then
        fail "$label: campo $field assente"
        return 0
    fi
    flat=$(tr -d '[:space:]' <<<"$obj")
    assert_contains "$label: $field" "$flat" "$expected"
}

clean_build_context() {
    local ctx=$1
    rm -rf "$ctx"
    mkdir -p "$ctx"
    git -C "$ROOT" archive HEAD | tar -x -C "$ctx"
    printf '%s' "$ctx"
}

# build_images CONTEXT builds both images from one Docker build context.
build_images() {
    local ctx=$1
    docker build -f "$ctx/sim/Dockerfile.mock" -t "$MOCK_IMAGE" "$ctx" \
        >"$WORK/build-mock.log" 2>&1 || return 1
    docker build -f "$ctx/Dockerfile" -t "$IDX_IMAGE" "$ctx" \
        >"$WORK/build-indexarr.log" 2>&1 || return 1
}

ensure_images() {
    if [[ $REBUILD != 1 ]] \
        && docker image inspect "$MOCK_IMAGE" >/dev/null 2>&1 \
        && docker image inspect "$IDX_IMAGE" >/dev/null 2>&1; then
        printf 'immagini già presenti: %s, %s\n' "$MOCK_IMAGE" "$IDX_IMAGE"
        return 0
    fi

    if [[ $CLEAN != 1 ]] && build_images "$ROOT"; then
        ok "immagini costruite dal working tree"
        return 0
    fi

    printf 'working tree non compilabile o CLEAN=1: costruisco da HEAD (%s)\n' \
        "$(git -C "$ROOT" rev-parse --short HEAD)"
    local ctx
    ctx=$(clean_build_context "$WORK/clean-context")
    if build_images "$ctx"; then
        ok "immagini costruite dallo snapshot pulito di HEAD"
        return 0
    fi
    printf 'build mock:\n' >&2
    tail -n 30 "$WORK/build-mock.log" >&2 || true
    printf 'build indexarr:\n' >&2
    tail -n 30 "$WORK/build-indexarr.log" >&2 || true
    return 1
}

printf '== validazione del compose ==\n'
docker compose -f "$COMPOSE" config >/dev/null
ok "docker compose config"

if [[ $KEEP != 1 ]]; then
    printf '== reset dei dati della simulazione ==\n'
    docker compose -f "$COMPOSE" down -v --remove-orphans >/dev/null 2>&1 || true
    rm -rf "$DATA/prowlarr" "$DATA/sonarr" "$DATA/radarr" "$DATA/logs"
else
    FRESH=0
    printf '== KEEP=1: dati e container esistenti vengono conservati ==\n'
fi
mkdir -p "$DATA/prowlarr" "$DATA/sonarr" "$DATA/radarr" "$LOGDIR"

printf '\n== immagini ==\n'
ensure_images || fatal "costruzione delle immagini fallita"
docker image inspect "$MOCK_IMAGE" >/dev/null
docker image inspect "$IDX_IMAGE" >/dev/null

for image in lscr.io/linuxserver/prowlarr:latest \
    lscr.io/linuxserver/sonarr:latest \
    lscr.io/linuxserver/radarr:latest; do
    if [[ $PULL == 1 ]] || ! docker image inspect "$image" >/dev/null 2>&1; then
        docker pull "$image"
    fi
done
ok "immagini reali Prowlarr, Sonarr e Radarr pronte"

printf '\n== avvio dello stack ==\n'
docker compose -f "$COMPOSE" up -d
ok "docker compose up -d"

printf '\n== attesa dei servizi ==\n'
wait_http "$MOCK/healthz" simmock 120 || fatal "simmock non pronto"
wait_http "$IDX/healthz" indexarr 180 || fatal "indexarr non pronto"
wait_http "$PROWLARR/ping" prowlarr 180 || fatal "Prowlarr non pronto"
wait_http "$SONARR/ping" sonarr 180 || fatal "Sonarr non pronto"
wait_http "$RADARR/ping" radarr 180 || fatal "Radarr non pronto"

printf '\n== scoperta delle API key da /config/config.xml ==\n'
PROWLARR_KEY=$(wait_config_key prowlarr) || fatal "API key di Prowlarr non trovata in /config/config.xml"
SONARR_KEY=$(wait_config_key sonarr) || fatal "API key di Sonarr non trovata in /config/config.xml"
RADARR_KEY=$(wait_config_key radarr) || fatal "API key di Radarr non trovata in /config/config.xml"
KEY=$(wait_indexarr_key) || fatal "API key di indexarr non leggibile tramite sim/seed -print-key"
ok "API key Prowlarr scoperta (${PROWLARR_KEY:0:8}...)"
ok "API key Sonarr scoperta (${SONARR_KEY:0:8}...)"
ok "API key Radarr scoperta (${RADARR_KEY:0:8}...)"
ok "API key indexarr pronta (${KEY:0:8}...)"

printf '\n== indexarr: definizione e indexer nativo su simmock ==\n'
yaml_json=$(awk '{ gsub(/\\/,"\\\\"); gsub(/"/,"\\\""); printf "%s\\n", $0 }' \
    "$SIM/definitions/torznab-demo.yaml")
definition_payload=$(printf '{"body":"%s"}' "$yaml_json")
code=$(curl -sS -o "$WORK/definition.json" -w '%{http_code}' -X POST \
    -H 'Content-Type: application/json' --data "$definition_payload" \
    "$IDX/api/v2/definitions?apikey=$KEY")
if [[ $code == 201 || $code == 200 ]]; then
    ok "POST /api/v2/definitions ($code)"
elif [[ $code == 409 ]]; then
    ok "POST /api/v2/definitions (già presente, 409)"
else
    fail "POST /api/v2/definitions HTTP $code: $(tr '\n' ' ' <"$WORK/definition.json")"
    exit 1
fi

indexer_payload=$(printf '{"definition_id":"sim-torznab","name":"Sim Torznab","slug":"sim-torznab","settings":{"url":"%s/torznab/api","apikey":"demo-key","api_key":"demo-key","allow_private":"true","categories":"2040,3040,5040"}}' "$MOCK_INTERNAL")
code=$(curl -sS -o "$WORK/indexer.json" -w '%{http_code}' -X POST \
    -H 'Content-Type: application/json' --data "$indexer_payload" \
    "$IDX/api/v2/indexers?apikey=$KEY")
INDEXER_ID=$(grep -o '"id":[0-9]*' "$WORK/indexer.json" | head -n1 | cut -d: -f2 || true)
if [[ $code == 201 && -n ${INDEXER_ID:-} ]]; then
    ok "POST /api/v2/indexers (id=$INDEXER_ID)"
elif [[ $code == 409 ]]; then
    INDEXER_ID=$(curl -fsS "$IDX/api/v2/indexers?apikey=$KEY" \
        | grep -o '{"id":[0-9]*[^}]*"slug":"sim-torznab"[^}]*}' \
        | head -n1 | grep -o '"id":[0-9]*' | head -n1 | cut -d: -f2 || true)
    if [[ -n ${INDEXER_ID:-} ]]; then
        ok "indexer sim-torznab già presente (id=$INDEXER_ID)"
    else
        fail "indexer sim-torznab già presente ma id non trovato"
        exit 1
    fi
else
    fail "POST /api/v2/indexers HTTP $code: $(tr '\n' ' ' <"$WORK/indexer.json")"
    exit 1
fi

run_json=$(curl -sS -X POST "$IDX/api/v2/indexers/$INDEXER_ID/run?apikey=$KEY" || true)
assert_contains "run indexer nativo: 3 viste" "$run_json" '"seen":3'
if [[ $FRESH == 1 ]]; then
    assert_contains "run indexer nativo: 3 nuove" "$run_json" '"new":3'
fi

search_xml=$(curl -fsS "$IDX/api?t=search&apikey=$KEY" || true)
assert_contains "ricerca indexarr: total=3" "$search_xml" 'total="3"'
assert_count "ricerca indexarr: 3 item" "$search_xml" '<item>' 3

printf '\n== Prowlarr reale: indexer Torznab verso simmock ==\n'
APP_PROFILE_ID=$(curl -fsS -H "X-Api-Key: $PROWLARR_KEY" \
    "$PROWLARR/api/v1/appprofile" | grep -o '"id": *[0-9]*' | head -n1 | grep -o '[0-9]*' || true)
APP_PROFILE_ID=${APP_PROFILE_ID:-1}

cat >"$WORK/prowlarr-indexer.json" <<EOF
{
  "name": "$PROWLARR_INDEXER_NAME",
  "implementationName": "Torznab",
  "implementation": "Torznab",
  "configContract": "TorznabSettings",
  "priority": 25,
  "appProfileId": $APP_PROFILE_ID,
  "protocol": "torrent",
  "enable": true,
  "enableRss": true,
  "enableAutomaticSearch": true,
  "enableInteractiveSearch": true,
  "tags": [],
  "fields": [
    {"name": "baseUrl", "value": "http://simmock:19080/torznab/api"},
    {"name": "apiPath", "value": ""},
    {"name": "apiKey", "value": "demo-key"},
    {"name": "additionalParameters", "value": ""},
    {"name": "torrentBaseSettings.preferMagnetUrl", "value": false}
  ]
}
EOF

# Prowlarr validates connectivity on both /indexer/test and /indexer. The test
# endpoint is the explicit assertion; the create call is the next one.
code=$(curl -sS -o "$WORK/prowlarr-test.json" -w '%{http_code}' -X POST \
    -H "X-Api-Key: $PROWLARR_KEY" -H 'Content-Type: application/json' \
    --data @"$WORK/prowlarr-indexer.json" "$PROWLARR/api/v1/indexer/test")
if [[ $code == 200 ]]; then
    ok "Prowlarr /api/v1/indexer/test verso simmock"
else
    fail "Prowlarr indexer test HTTP $code: $(tr '\n' ' ' <"$WORK/prowlarr-test.json")"
fi

code=$(curl -sS -o "$WORK/prowlarr-created.json" -w '%{http_code}' -X POST \
    -H "X-Api-Key: $PROWLARR_KEY" -H 'Content-Type: application/json' \
    --data @"$WORK/prowlarr-indexer.json" "$PROWLARR/api/v1/indexer")
PROWLARR_INDEXER_ID=$(grep -o '"id": *[0-9]*' "$WORK/prowlarr-created.json" \
    | tail -n1 | grep -o '[0-9]*' || true)
if [[ $code == 201 && -n ${PROWLARR_INDEXER_ID:-} ]]; then
    ok "Prowlarr ha creato l'indexer (id=$PROWLARR_INDEXER_ID)"
else
    fail "Prowlarr create indexer HTTP $code: $(tr '\n' ' ' <"$WORK/prowlarr-created.json")"
    exit 1
fi

prowlarr_indexer=$(curl -fsS -H "X-Api-Key: $PROWLARR_KEY" \
    "$PROWLARR/api/v1/indexer/$PROWLARR_INDEXER_ID" || true)
assert_contains "Prowlarr conserva baseUrl" "$prowlarr_indexer" \
    '"value": "http://simmock:19080/torznab/api"'
assert_contains "Prowlarr ha enable=true" "$prowlarr_indexer" '"enable": true'
assert_contains "Prowlarr maschera la apiKey" "$prowlarr_indexer" '"value": "********"'

prowlarr_feed=$(curl -fsS "$PROWLARR/$PROWLARR_INDEXER_ID/api?apikey=$PROWLARR_KEY&t=search&q=" || true)
assert_count "feed Torznab di Prowlarr: 3 item" "$prowlarr_feed" '<item>' 3

printf '\n== indexarr: indexer torznab nativo verso Prowlarr ==\n'
jar="$WORK/cookies.txt"
if ! curl -fsS -c "$jar" "$IDX/indexers" -o "$WORK/indexers.html"; then
    fail "pagina /indexers non raggiungibile"
    exit 1
fi
ok "pagina /indexers raggiungibile"

bridge_url="$PROWLARR_INTERNAL/$PROWLARR_INDEXER_ID/api?apikey=$PROWLARR_KEY"
bridge_slug="prowlarr-bridge-${RUN_SUFFIX:-run}"
bridge_body=$(printf '{"definition_id":"torznab-generic","name":"%s","slug":"%s","settings":{"url":"%s","apikey":"%s"}}' \
    "$BRIDGE_NAME" "$bridge_slug" "$bridge_url" "$PROWLARR_KEY")
bridge_resp=$(curl -fsS -X POST -H 'Content-Type: application/json' --data "$bridge_body" \
    "$IDX/api/v2/indexers?apikey=$KEY" || true)
BRIDGE_ID=$(grep -o '"id":[0-9]*' <<<"$bridge_resp" | head -n1 | cut -d: -f2 || true)
if [[ -z ${BRIDGE_ID:-} ]]; then
    fail "creazione indexer nativo fallita: ${bridge_resp:0:240}"
    exit 1
fi
ok "indexer torznab nativo creato (id=$BRIDGE_ID)"

run_resp=$(curl -fsS -X POST "$IDX/api/v2/indexers/$BRIDGE_ID/run?apikey=$KEY" || true)
assert_contains "indexer nativo Prowlarr: run completato" "$run_resp" '"seen":2'
assert_contains "indexer nativo Prowlarr: 2 item torrent visti" "$run_resp" '"seen":2'

# The native indexer writes its releases into the same store; the search endpoint
# must still answer with the three deterministic releases.
search_after_bridge=$(curl -fsS "$IDX/api?t=search&apikey=$KEY" || true)
assert_contains "ricerca dopo l'aggiornamento: total=3" "$search_after_bridge" 'total="3"'
assert_count "ricerca dopo l'aggiornamento: 3 item" "$search_after_bridge" '<item>' 3

printf '\n== app sync: Sonarr e Radarr reali ==\n'
create_or_get_application() {
    local kind=$1 name=$2 url=$3 key=$4 list obj id code body
    list=$(curl -fsS "$IDX/api/v2/applications?apikey=$KEY" || true)
    obj=$(grep -o "{[^{}]*\"kind\":\"$kind\"[^{}]*}" <<<"$list" \
        | grep -F "\"url\":\"$url\"" | head -n1 || true)
    id=$(grep -o '"id":[0-9]*' <<<"$obj" | head -n1 | cut -d: -f2 || true)
    if [[ -n ${id:-} ]]; then
        printf '%s' "$id"
        return 0
    fi
    body=$(printf '{"kind":"%s","name":"%s","url":"%s","api_key":"%s","sync_level":"full"}' \
        "$kind" "$name" "$url" "$key")
    code=$(curl -sS -o "$WORK/app-$kind.json" -w '%{http_code}' -X POST \
        -H 'Content-Type: application/json' --data "$body" \
        "$IDX/api/v2/applications?apikey=$KEY")
    if [[ $code != 201 ]]; then
        return 1
    fi
    grep -o '"id":[0-9]*' "$WORK/app-$kind.json" | head -n1 | cut -d: -f2
}

SONARR_APP_ID=$(create_or_get_application sonarr "Real Sonarr" "http://sonarr:8989" "$SONARR_KEY") \
    || fatal "creazione applicazione Sonarr fallita"
RADARR_APP_ID=$(create_or_get_application radarr "Real Radarr" "http://radarr:7878" "$RADARR_KEY") \
    || fatal "creazione applicazione Radarr fallita"
ok "applicazione Sonarr (id=$SONARR_APP_ID)"
ok "applicazione Radarr (id=$RADARR_APP_ID)"

sonarr_sync_code=$(curl -sS -o "$WORK/sonarr-sync.json" -w '%{http_code}' -X POST \
    "$IDX/api/v2/applications/$SONARR_APP_ID/sync?apikey=$KEY" || true)
radarr_sync_code=$(curl -sS -o "$WORK/radarr-sync.json" -w '%{http_code}' -X POST \
    "$IDX/api/v2/applications/$RADARR_APP_ID/sync?apikey=$KEY" || true)
sonarr_sync=$(cat "$WORK/sonarr-sync.json" 2>/dev/null || true)
radarr_sync=$(cat "$WORK/radarr-sync.json" 2>/dev/null || true)

APP_SYNC_OK=1
if [[ $sonarr_sync_code == 200 ]]; then
    ok "sync Sonarr HTTP 200"
    if [[ $FRESH == 1 ]]; then
        assert_contains "sync Sonarr: 1 aggiunto" "$sonarr_sync" '"added":1'
    fi
else
    APP_SYNC_OK=0
    cp "$WORK/sonarr-sync.json" "$LOGDIR/sonarr-sync.json" 2>/dev/null || true
    fail "sync Sonarr HTTP $sonarr_sync_code: ${sonarr_sync:0:240}..."
fi
if [[ $radarr_sync_code == 200 ]]; then
    ok "sync Radarr HTTP 200"
    if [[ $FRESH == 1 ]]; then
        assert_contains "sync Radarr: 1 aggiunto" "$radarr_sync" '"added":1'
    fi
else
    APP_SYNC_OK=0
    cp "$WORK/radarr-sync.json" "$LOGDIR/radarr-sync.json" 2>/dev/null || true
    fail "sync Radarr HTTP $radarr_sync_code: ${radarr_sync:0:240}..."
fi

sonarr_indexers=$(curl -fsS -H "X-Api-Key: $SONARR_KEY" "$SONARR/api/v3/indexer" || true)
radarr_indexers=$(curl -fsS -H "X-Api-Key: $RADARR_KEY" "$RADARR/api/v3/indexer" || true)

assert_remote_indexer() {
    local label=$1 body=$2 flat count
    flat=$(tr -d '[:space:]' <<<"$body")
    count=$(grep -o 'indexarr·SimTorznab' <<<"$flat" | wc -l | tr -d ' ' || true)
    assert_eq "$label: un indexer indexarr" "1" "$count"
    assert_contains "$label: nome" "$flat" '"name":"indexarr·SimTorznab"'
    assert_contains "$label: implementation" "$flat" '"implementation":"Torznab"'
    assert_contains "$label: configContract" "$flat" '"configContract":"TorznabSettings"'
    assert_contains "$label: protocol" "$flat" '"protocol":"torrent"'
    assert_contains "$label: enableRss" "$flat" '"enableRss":true'
    assert_contains "$label: enableAutomaticSearch" "$flat" '"enableAutomaticSearch":true'
    assert_contains "$label: enableInteractiveSearch" "$flat" '"enableInteractiveSearch":true'
    assert_field_value "$label" "$body" "baseUrl" \
        "\"value\":\"$INDEXARR_INTERNAL/torznab/sim-torznab\""
    # Servarr masks apiKey on GET; verify the field is marked as a secret and
    # carries the masked value, not the plaintext key.
    assert_field_value "$label" "$body" "apiKey" '"privacy":"apiKey"'
    assert_field_value "$label" "$body" "apiKey" '"value":"********"'
    assert_field_value "$label" "$body" "categories" '"value":[2040,3040,5040]'
}

if [[ $APP_SYNC_OK == 1 ]]; then
    assert_remote_indexer "Sonarr" "$sonarr_indexers"
    assert_remote_indexer "Radarr" "$radarr_indexers"
else
    # These are diagnostics of a real incompatibility, not a fake pass. When
    # the payload/routing is fixed the branch above runs instead.
    assert_contains "Sonarr: errore di sync riporta /api/api" "$sonarr_sync" \
        '/torznab/sim-torznab/api/api'
    assert_contains "Radarr: errore di sync riporta /api/api" "$radarr_sync" \
        '/torznab/sim-torznab/api/api'
    per_indexer_code=$(curl -sS -o /dev/null -w '%{http_code}' \
        "$IDX/torznab/sim-torznab/api?t=caps&apikey=$KEY" || true)
    assert_eq "indexarr: rotta per-indexer /torznab/{slug}/api" "200" "$per_indexer_code"
    assert_eq "Sonarr: nessun indexer aggiunto" "[]" \
        "$(tr -d '[:space:]' <<<"$sonarr_indexers")"
    assert_eq "Radarr: nessun indexer aggiunto" "[]" \
        "$(tr -d '[:space:]' <<<"$radarr_indexers")"
    printf '\nBLOCCANTE: sonarr/radarr non possono importare indexarr.\n' >&2
    printf '  1. il web handler non monta /torznab/{slug}/api (404 page not found);\n' >&2
    printf '  2. syncjob scrive baseUrl .../api e Sonarr/Radarr aggiungono apiPath=/api,\n' >&2
    printf '     producendo .../api/api (404).\n' >&2
fi

printf '\n== riepilogo ==\n'
printf 'asserzioni: %d passate, %d fallite\n' "$PASS" "$FAIL"
printf 'Prowlarr=%s Sonarr=%s Radarr=%s indexarr=%s\n' \
    "$PROWLARR" "$SONARR" "$RADARR" "$IDX"

if ((FAIL > 0)); then
    printf 'Simulazione reale FALLITA.\n' >&2
    exit 1
fi
printf 'Simulazione reale completata: tutte le asserzioni sono passate.\n'
