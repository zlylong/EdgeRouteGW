#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"

printf '\n[1/3] Backend unit/integration tests...\n'
(
  cd "$ROOT_DIR/backend"
  go test ./...
)

printf '\n[2/3] Backend coverage snapshot...\n'
(
  cd "$ROOT_DIR/backend"
  go test -coverprofile=coverage.out ./... >/tmp/proxygw_go_test_cover.log
  go tool cover -func=coverage.out | tail -n 1
)

printf '\n[3/3] Frontend button E2E tests...\n'
(
  cd "$ROOT_DIR/e2e"
  if [[ ! -d node_modules ]]; then
    npm ci --no-fund --no-audit
  fi
  npx playwright install --with-deps chromium
  npm run test:buttons
)

printf '\nAll tests completed.\n'
