#!/usr/bin/env bash
# test_coverage.sh — Backend coverage report
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BACKEND_DIR="$ROOT_DIR/backend"
COVER_DIR="$ROOT_DIR/coverage"

mkdir -p "$COVER_DIR"

cd "$BACKEND_DIR"

echo "=== Running tests with coverage ==="
go test -count=1 -coverprofile="$COVER_DIR/coverage.out" ./...

echo ""
echo "=== Coverage by function ==="
go tool cover -func="$COVER_DIR/coverage.out" | tail -n +1

echo ""
echo "=== Summary ==="
go tool cover -func="$COVER_DIR/coverage.out" | tail -n 1

echo ""
echo "=== Coverage by package ==="
go tool cover -func="$COVER_DIR/coverage.out" | grep '^total'

# Generate HTML report
go tool cover -html="$COVER_DIR/coverage.out" -o "$COVER_DIR/coverage.html"
echo "HTML report: $COVER_DIR/coverage.html"
