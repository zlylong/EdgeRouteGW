#!/bin/bash
set -euo pipefail

echo "=== Building EdgeRouteGW Backend ==="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
BACKEND_DIR="$ROOT_DIR/backend"

if [ ! -d "$BACKEND_DIR" ]; then
    echo "Error: backend directory not found: $BACKEND_DIR"
    exit 1
fi

cd "$BACKEND_DIR"

# Default to a mirror reachable from mainland China, but only when nothing else
# is configured: an environment variable beats `go env -w GOPROXY=...`, so
# setting it unconditionally silently overrode a developer's own choice.
if [ -z "${GOPROXY:-}" ] && [ "$(go env GOPROXY)" = "https://proxy.golang.org,direct" ]; then
    export GOPROXY="https://goproxy.cn,direct"
fi

# mattn/go-sqlite3 needs cgo. Since Go 1.20 cgo is silently disabled when no C
# compiler is found, and the resulting binary fails at runtime with
# "go-sqlite3 requires cgo" -- after this script may already have restarted
# the service into it. Fail here instead.
export CGO_ENABLED=1
if ! command -v "${CC:-gcc}" >/dev/null 2>&1; then
    echo "Error: a C compiler is required (CGO_ENABLED=1 for go-sqlite3); install gcc or set CC"
    exit 1
fi

go build -o proxygw-backend .

echo "Build successful: $BACKEND_DIR/proxygw-backend"

# Deploying is a separate decision from building. This used to restart proxygw
# unconditionally, so compiling on the gateway dropped every active connection
# whether or not that was the intent.
if [ "${PROXYGW_RESTART_AFTER_BUILD:-0}" = "1" ]; then
    echo "PROXYGW_RESTART_AFTER_BUILD=1, restarting service..."
    systemctl restart proxygw
    echo "Service restarted."
else
    echo "Not restarting proxygw. To deploy this build:"
    echo "  systemctl restart proxygw"
    echo "  (or re-run with PROXYGW_RESTART_AFTER_BUILD=1)"
fi