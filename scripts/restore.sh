#!/bin/bash
# EdgeRouteGW Restore Script
set -e

if [ -z "$1" ]; then
    echo "Usage: $0 <path_to_backup_tar_gz>"
    exit 1
fi

BACKUP_FILE=$1
REPO_DIR="/root/proxygw"

if [ ! -f "$BACKUP_FILE" ]; then
    echo "Error: Backup file not found: $BACKUP_FILE"
    exit 1
fi

echo "=== EdgeRouteGW Restore ==="
echo "Source: $BACKUP_FILE"

# Ensure environment is ready
if [ ! -d "$REPO_DIR" ]; then
    echo "[!] Target directory $REPO_DIR does not exist. Please clone the repo first."
    exit 1
fi

# Stop services
echo "[1/3] Stopping services..."
systemctl stop proxygw mosdns xray || true

# Extract files
echo "[2/3] Restoring configurations..."
tar -xzf "$BACKUP_FILE" -C "$REPO_DIR"

# Ensure correct permissions
chown -R root:root "$REPO_DIR"
chmod -R 755 "$REPO_DIR/scripts"

# Restart services
echo "[3/3] Restarting services and applying configuration..."
systemctl daemon-reload
systemctl restart xray mosdns proxygw

echo "=========================================="
echo "Restore completed successfully!"
echo "The system is now running with your previous configuration."
