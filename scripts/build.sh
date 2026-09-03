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

export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"

cd "$BACKEND_DIR"
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