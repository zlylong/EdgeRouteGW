#!/usr/bin/env bash
# test_benchmark.sh — Run backend benchmarks
# Usage: ./test_benchmark.sh [--bench=Pattern] [--cpu|--mem] [--count=N]
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BACKEND_DIR="$ROOT_DIR/backend"

cd "$BACKEND_DIR"

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
      echo "  $0 --bench=GeoQuery       Run GeoQuery benchmarks only"
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
go test -run='^$' -bench="$BENCH_PATTERN" -benchtime=1x -count=$COUNT "${BENCH_FLAGS[@]}" ./...
