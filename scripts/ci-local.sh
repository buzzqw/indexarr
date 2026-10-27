#!/usr/bin/env bash
# Local replacement for .github/workflows/qa.yml. The commit must be clean:
# the script records its result in ci-logs/<commit>.log and exits with
# "CI-LOCAL <commit> PASS" or "CI-LOCAL <commit> FAIL <job>".
set -uo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
COMMIT="$(git rev-parse --short HEAD)"
LOG_DIR="$ROOT/ci-logs"
mkdir -p "$LOG_DIR"
LOG="$LOG_DIR/$COMMIT.log"
: >"$LOG"
exec > >(tee -a "$LOG") 2>&1

BUDGET_PID=""
BUDGET_LOG=""

finish_fail() {
  local job="$1"
  # A failure before the budget is collected stops it: budget.sh removes its
  # temporary server and data on exit.
  if [[ -n "$BUDGET_PID" ]] && kill -0 "$BUDGET_PID" 2>/dev/null; then
    kill "$BUDGET_PID" 2>/dev/null || true
    wait "$BUDGET_PID" 2>/dev/null || true
  fi
  [[ -n "$BUDGET_LOG" ]] && rm -f "$BUDGET_LOG"
  [[ -n "${MATRIX_OUT:-}" ]] && rm -rf "$MATRIX_OUT"
  echo "CI-LOCAL $COMMIT FAIL $job"
  exit 1
}

run_job() {
  local name="$1"
  shift
  echo
  echo "==> $name"
  local started=$SECONDS
  if ! "$@"; then
    finish_fail "$name"
  fi
  echo "<== $name: $((SECONDS - started))s"
}

echo "CI-LOCAL $COMMIT START $(date -Is)"
if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  echo "working tree is not clean"
  finish_fail dirty
fi

# The production-copy test is environmental and is skipped only when the
# caller explicitly opts in. The report and the local log state this.
SKIP_PRODUCTION_COPY="${CI_SKIP_PRODUCTION_COPY:-0}"
TEST_FLAGS=()
RACE_FLAGS=(-race)
if [[ "$SKIP_PRODUCTION_COPY" == "1" ]]; then
  TEST_FLAGS=(-skip TestProductionCopyHasNoFalseAlarms)
  RACE_FLAGS+=("${TEST_FLAGS[@]}")
  echo "NOTE: TestProductionCopyHasNoFalseAlarms skipped (CI_SKIP_PRODUCTION_COPY=1)"
fi

run_job "gofmt and go vet" bash -c '
  unformatted="$(gofmt -l .)"
  if [[ -n "$unformatted" ]]; then
    echo "FAIL: unformatted Go files:" >&2
    echo "$unformatted" >&2
    exit 1
  fi
  go vet ./...
'
run_job "def-validate" make def-validate

# The resource budget spends a minute waiting for the idle server to settle
# before it samples RSS. It runs in the background while the tests run: the
# RSS of an idle process does not depend on the load of the others, and the
# binary-size gate is a file size. Its output is appended to the log and its
# result checked before the end.
BUDGET_LOG="$(mktemp "$LOG_DIR/.budget-$COMMIT.XXXXXX")"
BUDGET_STARTED=$SECONDS
./scripts/budget.sh >"$BUDGET_LOG" 2>&1 &
BUDGET_PID=$!
run_job "qa" make qa
run_job "go test" go test "${TEST_FLAGS[@]}" ./...
run_job "go test -race" go test "${RACE_FLAGS[@]}" ./...
# The matrix only has to compile: its 12 binaries (about 360 MB) go to a
# temporary directory removed right after, instead of piling up in bin/matrix.
MATRIX_OUT="$(mktemp -d "$LOG_DIR/.matrix-$COMMIT.XXXXXX")"
run_job "build matrix" env INDEXARR_MATRIX_OUT="$MATRIX_OUT" ./scripts/build-matrix.sh
rm -rf "$MATRIX_OUT"
run_job "test compile windows" env GOOS=windows CGO_ENABLED=0 go vet ./...
run_job "test compile darwin" env GOOS=darwin CGO_ENABLED=0 go vet ./...
echo
echo "==> resource budget (started in the background $((SECONDS - BUDGET_STARTED))s ago)"
budget_status=0
wait "$BUDGET_PID" || budget_status=$?
BUDGET_PID=""
cat "$BUDGET_LOG"
rm -f "$BUDGET_LOG"
BUDGET_LOG=""
if [[ "$budget_status" -ne 0 ]]; then
  finish_fail "resource budget"
fi
run_job "e2e" make e2e

echo "CI-LOCAL $COMMIT PASS"
echo "log: $LOG"
