#!/usr/bin/env bash
# test_all.sh — Master test orchestrator for ProxyGW
# Runs all test suites: backend unit/integration, race detection, coverage, frontend e2e
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

# Color output helpers
green() { printf '\033[32m%s\033[0m\n' "$1"; }
red() { printf '\033[31m%s\033[0m\n' "$1"; }
header() { printf '\n\033[1;36m━━━ %s ━━━\033[0m\n' "$1"; }
FAIL=0

# ──────────────────────────────────────────────
# [1/6] Backend unit + integration tests
# ──────────────────────────────────────────────
header "Backend Unit/Integration Tests"
if "$ROOT_DIR/scripts/test_backend.sh"; then
  green "✓ Backend tests passed"
else
  red "✗ Backend tests FAILED"
  FAIL=1
fi

# ──────────────────────────────────────────────
# [2/6] Backend race detection tests
# ──────────────────────────────────────────────
header "Backend Race Detection Tests"
if "$ROOT_DIR/scripts/test_backend.sh" --race; then
  green "✓ Race detection passed"
else
  red "✗ Race detection FAILED"
  FAIL=1
fi

# ──────────────────────────────────────────────
# [3/6] Backend coverage snapshot
# ──────────────────────────────────────────────
header "Backend Coverage Snapshot"
if "$ROOT_DIR/scripts/test_coverage.sh"; then
  green "✓ Coverage report generated"
else
  red "✗ Coverage FAILED"
  FAIL=1
fi

# ──────────────────────────────────────────────
# [4/6] Frontend button E2E tests
# ──────────────────────────────────────────────
header "Frontend Button E2E Tests"
if "$ROOT_DIR/scripts/test_frontend.sh"; then
  green "✓ Frontend E2E tests passed"
else
  red "✗ Frontend E2E tests FAILED"
  FAIL=1
fi

# ──────────────────────────────────────────────
# [5/6] Build verification
# ──────────────────────────────────────────────
header "Build Verification"
# backend is its own module (module proxygw) and the repo root has no go.mod, so
# building ./backend/... from here always failed -- and the redirect hid the
# reason, leaving the whole suite permanently reporting failure.
if (cd "$ROOT_DIR/backend" && go build -o /dev/null ./...); then
  green "✓ Backend builds successfully"
else
  red "✗ Build FAILED"
  FAIL=1
fi

# ──────────────────────────────────────────────
# [6/6] Git state check
# ──────────────────────────────────────────────
header "Git State Check"
if [[ -z "$(git status --porcelain)" ]]; then
  green "✓ Working tree clean"
else
  git status --short
fi

# ── Summary ──
printf '\n'
if [[ $FAIL -eq 0 ]]; then
  green '═══════════════════════════════════════'
  green '  ✨ All tests passed successfully!'
  green '═══════════════════════════════════════'
else
  red '═══════════════════════════════════════'
  red '  ❌ Some tests FAILED (see above)'
  red '═══════════════════════════════════════'
fi
exit $FAIL
