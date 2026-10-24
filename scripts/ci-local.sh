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

finish_fail() {
  local job="$1"
  echo "CI-LOCAL $COMMIT FAIL $job"
  exit 1
}

run_job() {
  local name="$1"
  shift
  echo
  echo "==> $name"
  if ! "$@"; then
    finish_fail "$name"
  fi
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
run_job "qa" make qa
run_job "go test" go test "${TEST_FLAGS[@]}" ./...
run_job "go test -race" go test "${RACE_FLAGS[@]}" ./...
run_job "build matrix" ./scripts/build-matrix.sh
run_job "test compile windows" env GOOS=windows CGO_ENABLED=0 go vet ./...
run_job "test compile darwin" env GOOS=darwin CGO_ENABLED=0 go vet ./...
run_job "resource budget" ./scripts/budget.sh
run_job "e2e" make e2e

echo "CI-LOCAL $COMMIT PASS"
echo "log: $LOG"
