#!/usr/bin/env bash
# pre-commit — Git pre-commit hook for ProxyGW
# Runs fast verification checks to prevent obvious breakage
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$(git rev-parse --git-dir 2>/dev/null)")" && pwd 2>/dev/null || pwd)"
cd "$ROOT_DIR"

echo "=== Pre-commit checks ==="

# Check for Go compilation errors (fast)
echo "  → Go build check..."
if ! (cd backend && go build -o /dev/null .); then
  echo "❌ Build failed — fix errors before committing"
  exit 1
fi
echo "  ✓ Build OK"

# Run only changed package tests (fast)
echo "  → Running affected backend tests..."
cd "$ROOT_DIR/backend"
CHANGED=$(git diff --cached --name-only -- '*.go' | sed 's/backend\///' | xargs -I{} dirname {} | sort -u | tr '\n' ' ')
if [[ -n "$CHANGED" ]]; then
  if ! go test -count=1 -short ./... 2>/dev/null; then
    echo "❌ Tests failed — fix before committing"
    exit 1
  fi
fi
echo "  ✓ Tests passed"

echo "=== ✓ Pre-commit checks passed ==="
