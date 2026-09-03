#!/usr/bin/env bash
# test_backend.sh — Backend Go tests runner
# Usage: ./test_backend.sh [--race] [--verbose|-v] [--short] [package...]
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BACKEND_DIR="$ROOT_DIR/backend"

cd "$BACKEND_DIR"

# Parse arguments
RACE=false
VERBOSE=false
SHORT=false
PACKAGES=("./...")
ARGS=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --race) RACE=true ;;
    --verbose|-v) VERBOSE=true ;;
    --short) SHORT=true ;;
    --help)
      echo "Usage: $0 [--race] [--verbose|-v] [--short] [package...]"
      echo ""
      echo "Options:"
      echo "  --race           Enable Go race detector"
      echo "  --verbose|-v     Verbose output"
      echo "  --short          Run only short tests"
      echo "  package          Package pattern (default: ./...)"
      exit 0
      ;;
    -*)
      echo "Unknown option: $1"
      exit 1
      ;;
    *)
      PACKAGES=("$1")
      ;;
  esac
  shift
done

if $VERBOSE; then
  ARGS+=("-v")
fi
if $RACE; then
  ARGS+=("-race")
fi
if $SHORT; then
  ARGS+=("-short")
fi

# -count=1 disables the test result cache; it is passed unconditionally below.
# An interactive branch used to append it a second time, which did nothing.
echo "Running: go test -count=1 ${ARGS[*]} ${PACKAGES[*]}"
go test -count=1 "${ARGS[@]}" "${PACKAGES[@]}"
