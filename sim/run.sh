#!/usr/bin/env bash
# Local end-to-end simulation: simmock + indexarr on isolated ports.
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SIM="$ROOT/sim"
BIN="$SIM/.bin"
DATA=$(mktemp -d "${TMPDIR:-/tmp}/indexarr-sim.XXXXXX")
KEEP=${KEEP:-0}
PIDS=()

cleanup() {
    local pid
    if ((${#PIDS[@]})); then
        for pid in "${PIDS[@]}"; do
            kill "$pid" 2>/dev/null || true
        done
        for pid in "${PIDS[@]}"; do
            wait "$pid" 2>/dev/null || true
        done
    fi
    if [[ $KEEP == 1 ]]; then
        printf 'Dati e log conservati in %s\n' "$DATA"
    else
        rm -rf "$DATA"
    fi
}
trap cleanup EXIT

wait_for() {
    local url=$1 name=$2 i
    for ((i = 1; i <= 150; i++)); do
        if curl -fsS "$url" >/dev/null 2>&1; then
            return 0
        fi
        sleep 0.2
    done
    printf 'timeout in attesa di %s (%s)\n' "$name" "$url" >&2
    return 1
}

printf '== build ==\n'
mkdir -p "$BIN"
(
    cd "$ROOT"
    go build -o "$BIN/simmock" ./cmd/simmock
    go build -o "$BIN/indexarr" ./cmd/indexarr
    go build -o "$BIN/seed" ./sim/seed
)

printf '== seed del database ==\n'
"$BIN/seed" -db "$DATA/indexarr.db" -public-url "http://127.0.0.1:18484"

printf '== avvio simmock e indexarr ==\n'
SIMM_MOCK_ADDR=127.0.0.1:19080 "$BIN/simmock" \
    -addr 127.0.0.1:19080 \
    -apikey demo-key \
    -sonarr-key demo-sonarr \
    -prowlarr-key demo-prowlarr \
    >"$DATA/simmock.log" 2>&1 &
PIDS+=("$!")

"$BIN/indexarr" \
    -config "$DATA/indexarr.json" \
    -listen 127.0.0.1:18484 \
    -db "$DATA/indexarr.db" \
    >"$DATA/indexarr.log" 2>&1 &
PIDS+=("$!")

wait_for "http://127.0.0.1:19080/healthz" simmock
wait_for "http://127.0.0.1:18484/healthz" indexarr

KEY=$(grep -o 'apikey=[0-9a-f]\{64\}' "$DATA/indexarr.log" | head -n1 | cut -d= -f2 || true)
if [[ -z ${KEY:-} ]]; then
    KEY=$("$BIN/seed" -db "$DATA/indexarr.db" -print-key)
fi
if [[ -z ${KEY:-} ]]; then
    printf 'impossibile leggere la API key di indexarr\n' >&2
    if [[ $KEEP != 1 ]]; then
        printf 'log: %s\n' "$DATA/indexarr.log" >&2
    fi
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
    "http://127.0.0.1:19080" \
    "$SIM/definitions/torznab-demo.yaml" \
    "$DATA"; then
    STATUS=0
else
    STATUS=1
fi

printf '\n== riepilogo ==\n'
if [[ $STATUS == 0 ]]; then
    printf 'Simulazione locale completata: tutte le asserzioni sono passate.\n'
else
    printf 'Simulazione locale fallita. Log in %s\n' "$DATA" >&2
    KEEP=1
fi
exit "$STATUS"
