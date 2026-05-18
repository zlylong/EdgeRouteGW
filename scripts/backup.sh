#!/bin/bash
# EdgeRouteGW Backup Script
set -e

BACKUP_DIR="/root/proxygw_backups"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
BACKUP_FILE="${BACKUP_DIR}/edgeroutegw_backup_${TIMESTAMP}.tar.gz"

mkdir -p "$BACKUP_DIR"

echo "=== EdgeRouteGW Backup ==="
echo "Target: $BACKUP_FILE"

# Stop services to ensure DB consistency
echo "[1/3] Stopping services..."
systemctl stop proxygw mosdns xray || true

# Archive critical configuration files
echo "[2/3] Archiving configurations..."
tar -czf "$BACKUP_FILE" \
    -C /root/proxygw \
    config/ \
    core/mosdns/*.yaml \
    core/mosdns/*.txt \
    core/xray/*.json \
    --exclude="core/mosdns/*.log" \
    --exclude="core/xray/*.log"

# Restart services
echo "[3/3] Restarting services..."
systemctl start xray mosdns proxygw

echo "=========================================="
echo "Backup completed successfully!"
echo "File location: $BACKUP_FILE"
echo "You can transfer this file to your new machine."
