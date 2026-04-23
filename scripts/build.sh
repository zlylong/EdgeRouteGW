#!/bin/bash
set -euo pipefail

echo "=== Building ProxyGW Backend ==="

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

echo "Build successful. Restarting service..."
systemctl restart proxygw
echo "Service restarted."