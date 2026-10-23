#!/usr/bin/env bash
# Cross-compile matrix for indexarr (WP12).
#
# Builds the optimised, pure-Go binaries (the SQLite driver is modernc.org,
# so CGO is never needed) for every target listed in docs/NEXT-STEPS.md §WP12:
#
#   linux/amd64 linux/arm64 linux/arm/7 linux/arm/6 linux/386 linux/riscv64
#   windows/amd64 windows/arm64 darwin/amd64 darwin/arm64
#   freebsd/amd64 freebsd/arm64
#
# Every target must build ./cmd/indexarr and ./cmd/indexarr-def: a failure in
# either is fatal, on every operating system. ./cmd/simmock is a
# development-only tool: it is attempted for every target and skipped with a
# warning where it does not compile.
#
# Usage: scripts/build-matrix.sh [--target GOOS/GOARCH[/GOARM]] [--out DIR]
#   --target   build a single matrix entry (used by the CI matrix job)
#   --out      output directory (default: ${INDEXARR_MATRIX_OUT:-bin/matrix})
#
# Output lives under bin/matrix/ (git-ignored) and is intentionally ephemeral:
# CI does not upload the binaries.
set -euo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TARGETS=(
  "linux/amd64"
  "linux/arm64"
  "linux/arm/7"
  "linux/arm/6"
  "linux/386"
  "linux/riscv64"
  "windows/amd64"
  "windows/arm64"
  "darwin/amd64"
  "darwin/arm64"
  "freebsd/amd64"
  "freebsd/arm64"
)

OUT="${INDEXARR_MATRIX_OUT:-bin/matrix}"
ONLY_TARGET=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target)
      [[ $# -ge 2 ]] || { echo "build-matrix: --target needs a value" >&2; exit 2; }
      ONLY_TARGET="$2"
      shift 2
      ;;
    --out)
      [[ $# -ge 2 ]] || { echo "build-matrix: --out needs a value" >&2; exit 2; }
      OUT="$2"
      shift 2
      ;;
    -h|--help)
      sed -n '2,24p' "$0"
      exit 0
      ;;
    *)
      echo "build-matrix: unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -n "$ONLY_TARGET" && " ${TARGETS[*]} " != *" $ONLY_TARGET "* ]]; then
  echo "build-matrix: unsupported target: $ONLY_TARGET" >&2
  echo "known targets: ${TARGETS[*]}" >&2
  exit 2
fi

mkdir -p "$OUT"

# build BIN GOOS GOARCH GOARM OUTDIR EXT: returns 0 on success, 1 on failure.
build_one() {
  local bin="$1" goos="$2" goarch="$3" goarm="$4" outdir="$5" ext="$6"
  local err="$outdir/$bin.error"
  if CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" \
    go build -trimpath -ldflags "-s -w" -o "$outdir/$bin$ext" "./cmd/$bin" \
    2>"$err"; then
    rm -f "$err"
    return 0
  fi
  return 1
}

HARD_FAILURES=0
SKIPPED=0
BUILT=0
ROWS=()

for target in "${TARGETS[@]}"; do
  if [[ -n "$ONLY_TARGET" && "$target" != "$ONLY_TARGET" ]]; then
    continue
  fi

  IFS=/ read -r goos goarch goarm <<<"$target"
  goarm="${goarm:-}"
  dir="$OUT/$goos-$goarch${goarm:+-armv$goarm}"
  mkdir -p "$dir"
  ext=""
  [[ "$goos" == "windows" ]] && ext=".exe"
  status="ok"

  if build_one "indexarr" "$goos" "$goarch" "$goarm" "$dir" "$ext"; then
    BUILT=$((BUILT + 1))
  else
    status="FAIL"
    HARD_FAILURES=$((HARD_FAILURES + 1))
    echo "FAIL: $target indexarr" >&2
    cat "$dir/indexarr.error" >&2
  fi

  if ! build_one "indexarr-def" "$goos" "$goarch" "$goarm" "$dir" "$ext"; then
    status="FAIL"
    HARD_FAILURES=$((HARD_FAILURES + 1))
    echo "FAIL: $target indexarr-def" >&2
    cat "$dir/indexarr-def.error" >&2
  else
    BUILT=$((BUILT + 1))
  fi

  # simmock is a developer tool, not a shipped binary: best effort only.
  if build_one "simmock" "$goos" "$goarch" "$goarm" "$dir" "$ext"; then
    BUILT=$((BUILT + 1))
  else
    SKIPPED=$((SKIPPED + 1))
    rm -f "$dir/simmock$ext" "$dir/simmock.error"
    [[ "$status" == "FAIL" ]] || status="${status}+simmock-skipped"
    echo "note: simmock is not supported on $target; skipping" >&2
  fi

  ROWS+=("$target|$status")
done

echo
echo "cross-compile matrix (CGO_ENABLED=0, -trimpath -s -w)"
printf '%-22s %-10s %s\n' "target" "status" "output"
printf '%-22s %-10s %s\n' "------" "------" "------"
for row in "${ROWS[@]}"; do
  target="${row%%|*}"
  status="${row##*|}"
  IFS=/ read -r goos goarch goarm <<<"$target"
  dir="$OUT/$goos-$goarch${goarm:+-armv$goarm}"
  printf '%-22s %-10s %s\n' "$target" "$status" "$dir"
done
echo
echo "built ${BUILT} binaries, ${SKIPPED} simmock target(s) skipped, ${HARD_FAILURES} failure(s)"

if [[ "$HARD_FAILURES" -gt 0 ]]; then
  echo "build-matrix: FAILED" >&2
  exit 1
fi
echo "build-matrix: OK"
