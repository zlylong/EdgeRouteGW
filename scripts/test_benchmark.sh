#!/usr/bin/env bash
# test_benchmark.sh — Run backend benchmarks
# Usage: ./test_benchmark.sh [--bench=Pattern] [--cpu|--mem] [--count=N]
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BACKEND_DIR="$ROOT_DIR/backend"

cd "$BACKEND_DIR"

# crypto_utils.go's package init reads config/aes.key under PROXYGW_HOME (default
# /root/proxygw) and creates it when missing, so every `go test` run touches that
# directory even though each test sets its own temporary home. Point the run at
# a throwaway directory instead so tests never write into a real install.
if [[ -z "${PROXYGW_HOME:-}" ]]; then
  PROXYGW_HOME="$(mktemp -d)"
  trap 'rm -rf "$PROXYGW_HOME"' EXIT
fi
export PROXYGW_HOME
mkdir -p "$PROXYGW_HOME/config"

BENCH_PATTERN="."
BENCH_FLAGS=("-benchmem")
COUNT=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --bench=*) BENCH_PATTERN="${1#*=}" ;;
    --cpu) BENCH_FLAGS=() ;;  # remove -benchmem
    --mem) BENCH_FLAGS=("-benchmem") ;;
    --count=*) COUNT="${1#*=}" ;;
    --help)
      echo "Usage: $0 [--bench=Pattern] [--cpu|--mem] [--count=N]"
      echo ""
      echo "Examples:"
      echo "  $0 --bench=QueryGeoIP     Run the GeoIP lookup benchmark only"
      echo "  $0 --bench=. --count=5    Run all benchmarks 5 times"
      exit 0
      ;;
    *)
      echo "Unknown option: $1"
      exit 1
      ;;
  esac
  shift
done

echo "=== Running Benchmarks (pattern=$BENCH_PATTERN, count=$COUNT) ==="
# -v makes a skipped benchmark say why (e.g. "geoip.dat not found"); without it a
# run with no usable data prints nothing but PASS and looks like it measured.
go test -v -run='^$' -bench="$BENCH_PATTERN" -benchtime=1x -count="$COUNT" "${BENCH_FLAGS[@]}" ./...
