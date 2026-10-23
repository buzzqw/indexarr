#!/usr/bin/env bash
# Docker end-to-end simulation on isolated host ports 18484 and 19080.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SIM="$ROOT/sim"
COMPOSE="$SIM/docker-compose.yml"
WORK=$(mktemp -d "${TMPDIR:-/tmp}/indexarr-sim-docker.XXXXXX")

cleanup() {
    docker compose -f "$COMPOSE" down -v --remove-orphans >/dev/null 2>&1 || true
    rm -rf "$WORK"
}
trap cleanup EXIT

wait_for() {
    local url=$1 name=$2 i
    for ((i = 1; i <= 240; i++)); do
        if curl -fsS "$url" >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.5
    done
    printf 'timeout in attesa di %s (%s)\n' "$name" "$url" >&2
    return 1
}

printf '== reset dello stack di simulazione ==\n'
docker compose -f "$COMPOSE" down -v --remove-orphans >/dev/null 2>&1 || true

printf '== build e avvio (simmock, seed, indexarr) ==\n'
docker compose -f "$COMPOSE" up -d --build

wait_for "http://127.0.0.1:19080/healthz" simmock
wait_for "http://127.0.0.1:18484/healthz" indexarr

KEY=$(docker compose -f "$COMPOSE" logs --no-color indexarr 2>/dev/null \
    | grep -o 'apikey=[0-9a-f]\{64\}' | head -n1 | cut -d= -f2 || true)
if [[ -z ${KEY:-} ]]; then
    KEY=$(docker compose -f "$COMPOSE" run --rm --no-deps -T seed -print-key 2>/dev/null | tail -n1 || true)
fi
if [[ -z ${KEY:-} ]]; then
    printf 'impossibile leggere la API key di indexarr dai log\n' >&2
    docker compose -f "$COMPOSE" logs --no-color indexarr >&2 || true
    exit 1
fi
export KEY

printf 'indexarr: http://127.0.0.1:18484 (apikey=%s...)\n' "${KEY:0:8}"
printf 'simmock:  http://127.0.0.1:19080\n'

source "$SIM/assertions.sh"
STATUS=0
if run_assertions \
    "http://127.0.0.1:18484" \
    "http://127.0.0.1:19080" \
    "http://simmock:19080" \
    "$SIM/definitions/torznab-demo.yaml" \
    "$WORK"; then
    STATUS=0
else
    STATUS=1
    printf '\n== log indexarr ==\n' >&2
    docker compose -f "$COMPOSE" logs --no-color indexarr >&2 || true
fi

printf '\n== riepilogo ==\n'
if [[ $STATUS == 0 ]]; then
    printf 'Simulazione Docker completata: tutte le asserzioni sono passate.\n'
else
    printf 'Simulazione Docker fallita.\n' >&2
fi
exit "$STATUS"
