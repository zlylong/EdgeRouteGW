#!/usr/bin/env bash
# test_frontend.sh — Frontend E2E tests (Playwright)
# Requires: Node.js/npm, python3 (static web server used by playwright.config.js),
# and frontend/dist/libs/app.css (scripts/build_frontend_css.sh). Runs the
# mocked button suite; tests/test-tools.spec.js needs a live backend and is
# not run here.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
E2E_DIR="$ROOT_DIR/e2e"
FRONTEND_DIST="$ROOT_DIR/frontend/dist"

cd "$E2E_DIR"

# Check frontend dist. index.html is the source (no bundler); only the
# Tailwind CSS is generated, by scripts/build_frontend_css.sh.
if [[ ! -d "$FRONTEND_DIST" ]]; then
  echo "Error: Frontend dist not found at $FRONTEND_DIST"
  exit 1
fi
if [[ ! -f "$FRONTEND_DIST/libs/app.css" ]]; then
  echo "Error: $FRONTEND_DIST/libs/app.css missing"
  echo "Generate it first: scripts/build_frontend_css.sh"
  exit 1
fi

# Install npm dependencies if missing
if [[ ! -d node_modules ]]; then
  echo "Installing npm dependencies..."
  npm ci --no-fund --no-audit
fi

# Install Playwright's Chromium only when no usable build is present.
# `playwright install --dry-run` exits 0 whether or not the browser exists (it
# only prints what it would download), so it cannot be used as the check; the
# previous form therefore always fell through to a silent download attempt on
# every run. Ask browser.js instead: it accepts Playwright's own build, a
# preinstalled build under PLAYWRIGHT_BROWSERS_PATH, or PW_CHROMIUM_EXECUTABLE,
# exactly as playwright.config.js does when launching.
if node -e '
  const fs = require("fs");
  const { resolveChromiumExecutable } = require("./browser");
  let own = "";
  try { own = require("playwright-core").chromium.executablePath(); } catch (_) {}
  process.exit((own && fs.existsSync(own)) || resolveChromiumExecutable() ? 0 : 1);
'; then
  echo "Chromium available, skipping browser install."
else
  echo "Installing Playwright Chromium (with system dependencies)..."
  npx playwright install --with-deps chromium
fi

echo "=== Running Frontend Button E2E Tests ==="
npm run test:buttons 2>&1

echo ""
echo "=== All button E2E tests complete ==="
