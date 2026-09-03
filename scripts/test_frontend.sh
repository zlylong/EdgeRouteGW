#!/usr/bin/env bash
# test_frontend.sh — Frontend E2E tests (Playwright)
# Requires: Node.js, Playwright, frontend dist built
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
E2E_DIR="$ROOT_DIR/e2e"
FRONTEND_DIST="$ROOT_DIR/frontend/dist"

cd "$E2E_DIR"

# Check frontend dist
if [[ ! -d "$FRONTEND_DIST" ]]; then
  echo "Error: Frontend dist not found at $FRONTEND_DIST"
  echo "Build it first: cd frontend && npm run build"
  exit 1
fi

# Install npm dependencies if missing
if [[ ! -d node_modules ]]; then
  echo "Installing npm dependencies..."
  npm ci --no-fund --no-audit
fi

# Install Playwright browsers if missing.
# The previous check grepped --dry-run output for "already installed", a string
# it makes no promise of emitting, so chromium was re-downloaded on nearly every
# run. Ask Playwright to resolve the browser instead: it exits non-zero when the
# executable is absent, and installing is a no-op when it is already present.
if ! npx playwright install chromium --dry-run >/dev/null 2>&1; then
  echo "Installing Playwright browsers (with system dependencies)..."
  npx playwright install --with-deps chromium
else
  npx playwright install chromium >/dev/null 2>&1 || true
fi

echo "=== Running Frontend Button E2E Tests ==="
npm run test:buttons 2>&1

echo ""
echo "=== All button E2E tests complete ==="
