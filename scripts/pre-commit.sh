#!/usr/bin/env bash
# pre-commit — Git pre-commit hook for ProxyGW
# Runs fast verification checks to prevent obvious breakage
set -euo pipefail

# --show-toplevel is the working tree root wherever the hook runs from. The
# previous dirname-of-git-dir form pointed at .git/worktrees/ inside a linked
# worktree and at the wrong directory when invoked from a subdirectory.
ROOT_DIR="$(git rev-parse --show-toplevel)"
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
if [ -n "$(git diff --cached --name-only -- '*.go')" ]; then
  if ! go test -count=1 -short ./...; then
    echo "❌ Tests failed — fix before committing"
    exit 1
  fi
  echo "  ✓ Tests passed"
else
  echo "  · No Go files staged, skipping tests"
fi

echo "=== ✓ Pre-commit checks passed ==="
