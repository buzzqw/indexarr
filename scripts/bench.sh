#!/usr/bin/env bash
# Performance harness for indexarr.
#
# Builds the release binary, measures its size, then starts a fresh instance
# for a pre-seeded database (10k and 50k releases) and measures startup time,
# idle RSS, file descriptors and Torznab search latency. Latency is collected
# with curl only: 100 sequential requests plus 20 concurrent ones, reported as
# p50/p95/max.
#
# Usage: scripts/bench.sh [--keep]
#   --keep   keep the temporary directory and print its path
set -euo pipefail
# curl and awk must print numbers with a dot; the reporter uses commas on an
# Italian locale and sort -n would silently collapse every latency to zero.
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

KEEP=0
if [[ "${1:-}" == "--keep" ]]; then
  KEEP=1
fi

TMP="$(mktemp -d "${TMPDIR:-/tmp}/indexarr-bench.XXXXXX")"
PID=""
cleanup() {
  if [[ -n "$PID" ]]; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  if [[ "$KEEP" == "1" ]]; then
    echo "temporary directory: $TMP"
  else
    rm -rf "$TMP"
  fi
}
trap cleanup EXIT

API_KEY="bench0000000000000000000000000000000000000000000000000000000000"

echo "==> build (CGO_ENABLED=0, -trimpath -s -w)"
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$TMP/indexarr" ./cmd/indexarr
go build -o "$TMP/benchseed" ./internal/perf/cmd/benchseed

BIN_BYTES="$(stat -c %s "$TMP/indexarr")"
BIN_MIB="$(awk -v b="$BIN_BYTES" 'BEGIN { printf "%.1f", b/1048576 }')"

percentiles() {
  # Prints p50, p95 and max in milliseconds from a file of seconds.
  sort -n "$1" | awk '
    { v[NR] = $1 }
    END {
      if (NR == 0) { print "0.0 0.0 0.0"; exit }
      p50 = v[int((NR + 1) * 0.50)]
      p95 = v[int((NR + 1) * 0.95)]
      printf "%.1f %.1f %.1f\n", p50 * 1000, p95 * 1000, v[NR] * 1000
    }'
}

# measure_seq URL OUT: 100 sequential requests, one line per request.
measure_seq() {
  local url="$1" out="$2"
  : >"$out"
  local i
  for i in $(seq 1 100); do
    curl -fsS -o /dev/null -w '%{time_total}\n' "$url" >>"$out"
  done
}

# measure_concurrent URL OUT COUNT: COUNT requests started at the same time.
measure_concurrent() {
  local url="$1" out="$2" count="$3"
  local dir="$TMP/concurrent"
  rm -rf "$dir"
  mkdir -p "$dir"
  : >"$out"
  local pids=()
  local i
  for i in $(seq 1 "$count"); do
    (
      curl -fsS -o /dev/null -w '%{time_total}\n' "$url" >"$dir/$i"
    ) &
    pids+=("$!")
  done
  # Wait for the requests only: a bare wait would also wait for the server
  # process started with &.
  local pid
  for pid in "${pids[@]}"; do
    wait "$pid"
  done
  cat "$dir"/* >>"$out"
}

# url_with appends a raw query string to a URL that already has parameters.
url_with() {
  printf '%s&%s' "$1" "$2"
}

row() {
  printf '%-6s %-16s %-11s %-8s %-5s %-8s %-8s %-8s\n' "$@"
}

# start_server N PORT: seeds a database and waits for /healthz.
start_server() {
  local n="$1" port="$2"
  local db="$TMP/indexarr-$n.db"
  local log="$TMP/indexarr-$n.log"
  local cfg="$TMP/indexarr-$n.json"
  API_KEY="$("$TMP/benchseed" -db "$db" -n "$n" -key "$API_KEY" 2>"$TMP/seed-$n.log")"
  cat >"$cfg" <<JSON
{
  "listen": "127.0.0.1:$port",
  "db_path": "$db",
  "log_level": "warn"
}
JSON
  local start_ns end_ns
  start_ns="$(date +%s%N)"
  "$TMP/indexarr" -config "$cfg" >"$log" 2>&1 &
  PID=$!
  local ready=0
  local i
  for i in $(seq 1 3000); do
    if curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null 2>&1; then
      ready=1
      break
    fi
    if ! kill -0 "$PID" 2>/dev/null; then
      echo "FAIL: server exited during startup" >&2
      cat "$log" >&2
      exit 1
    fi
    sleep 0.01
  done
  if [[ "$ready" != "1" ]]; then
    echo "FAIL: server did not become ready on port $port" >&2
    cat "$log" >&2
    exit 1
  fi
  end_ns="$(date +%s%N)"
  STARTUP_MS=$(( (end_ns - start_ns) / 1000000 ))
  # Let the first GC and the scheduler settle before sampling resources.
  sleep 1
  RSS_KB="$(ps -o rss= -p "$PID" | tr -d ' ')"
  IDLE_FDS="$(find "/proc/$PID/fd" -maxdepth 1 -type l 2>/dev/null | wc -l | tr -d ' ')"
}

stop_server() {
  if [[ -n "$PID" ]]; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
    PID=""
  fi
}

run_dataset() {
  local n="$1" port="$2"
  echo "==> dataset $n releases"
  start_server "$n" "$port"
  local base
  base="http://127.0.0.1:$port/api?t=search&apikey=$API_KEY&limit=100"
  local rss_mib
  rss_mib="$(awk -v k="$RSS_KB" 'BEGIN { printf "%.1f", k/1024 }')"

  # Warm up every query shape before timing.
  curl -fsS -o /dev/null --get "$base" --data-urlencode "q=benchmark movie 1234"
  curl -fsS -o /dev/null "$base"
  curl -fsS -o /dev/null --get "$base" --data-urlencode "q=1080p"

  local p50 p95 max
  measure_seq "$base" "$TMP/empty.$n"
  read -r p50 p95 max < <(percentiles "$TMP/empty.$n")
  row "$n" "empty" "$STARTUP_MS" "$rss_mib" "$IDLE_FDS" "$p50" "$p95" "$max"

  measure_seq "$(url_with "$base" "q=benchmark+movie+1234")" "$TMP/title.$n"
  read -r p50 p95 max < <(percentiles "$TMP/title.$n")
  row "$n" "title" "-" "-" "-" "$p50" "$p95" "$max"

  measure_seq "$(url_with "$base" "q=benchmark+movie+1234&cat=2000")" "$TMP/titlecat.$n"
  read -r p50 p95 max < <(percentiles "$TMP/titlecat.$n")
  row "$n" "title+cat" "-" "-" "-" "$p50" "$p95" "$max"

  measure_seq "$(url_with "$base" "q=1080p")" "$TMP/stress.$n"
  read -r p50 p95 max < <(percentiles "$TMP/stress.$n")
  row "$n" "stress-1080p" "-" "-" "-" "$p50" "$p95" "$max"

  measure_concurrent "$(url_with "$base" "q=benchmark+movie+1234")" "$TMP/ctitle.$n" 20
  read -r p50 p95 max < <(percentiles "$TMP/ctitle.$n")
  row "$n" "20x title" "-" "-" "-" "$p50" "$p95" "$max"

  measure_concurrent "$(url_with "$base" "q=1080p")" "$TMP/cstress.$n" 20
  read -r p50 p95 max < <(percentiles "$TMP/cstress.$n")
  row "$n" "20x stress" "-" "-" "-" "$p50" "$p95" "$max"

  local after_fds rss_after_mib
  after_fds="$(find "/proc/$PID/fd" -maxdepth 1 -type l 2>/dev/null | wc -l | tr -d ' ')"
  RSS_KB="$(ps -o rss= -p "$PID" | tr -d ' ')"
  rss_after_mib="$(awk -v k="$RSS_KB" 'BEGIN { printf "%.1f", k/1024 }')"
  row "$n" "after load" "-" "$rss_after_mib" "$after_fds" "-" "-" "-"
  stop_server
}

PORT1=$((20000 + RANDOM % 15000))
PORT2=$((PORT1 + 1))

echo
echo "indexarr performance harness — $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
echo "host: $(uname -srm), $(nproc) CPUs"
echo "binary: ${BIN_MIB} MiB (${BIN_BYTES} bytes)"
echo
row "size" "scenario" "startup_ms" "rss_mib" "fds" "p50_ms" "p95_ms" "max_ms"
row "------" "----------------" "-----------" "--------" "-----" "--------" "--------" "--------"

run_dataset 10000 "$PORT1"
run_dataset 50000 "$PORT2"

echo
echo "note: 100 sequential requests per scenario; the 20x rows start 20 requests"
echo "      at once. stress-1080p is a deliberately unselective query (about 43%"
echo "      of the seeded titles): it is the upper bound, not the common case."
