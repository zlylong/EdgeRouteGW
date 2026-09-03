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

# Run the backend tests when any Go file is staged. This previously derived a
# list of changed packages, used it only as an "is anything staged" flag, and
# then ran the whole suite anyway -- while discarding stderr, so a failure
# printed "Tests failed" with no indication of which test or why.
echo "  → Running backend tests..."
cd "$ROOT_DIR/backend"
if git diff --cached --name-only -- '*.go' | grep -q .; then
  if ! go test -count=1 -short ./...; then
    echo "❌ Tests failed — fix before committing"
    exit 1
  fi
  echo "  ✓ Tests passed"
else
  echo "  · No Go files staged, skipping tests"
fi

echo "=== ✓ Pre-commit checks passed ==="
