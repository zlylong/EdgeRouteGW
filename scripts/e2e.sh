#!/bin/bash
# Runs the Playwright suite against a freshly started binary.
#   make build && scripts/e2e.sh
set -euo pipefail
cd "$(dirname "$0")/.."
PORT=${ERGW_E2E_PORT:-18085}
DATA=$(mktemp -d)
LOG="$DATA/serve.log"
cleanup() { kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; rm -rf "$DATA"; }
trap cleanup EXIT
bin/edgeroutegw serve -no-reconcile -listen "127.0.0.1:$PORT" -data-dir "$DATA" -log-level info >"$LOG" 2>&1 &
PID=$!
ready=0
for _ in $(seq 1 100); do
  if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then ready=1; break; fi
  if ! kill -0 "$PID" 2>/dev/null; then break; fi
  sleep 0.2
done
if [ "$ready" -ne 1 ]; then echo "edgeroutegw did not become healthy on port $PORT:" >&2; cat "$LOG" >&2; exit 1; fi
cd web
ERGW_E2E_URL="http://127.0.0.1:$PORT" ERGW_E2E_PASSWORD_FILE="$DATA/bootstrap_password.txt" bunx playwright test "$@"
