#!/bin/bash
set -e

echo "========================================================="
echo "  EdgeRouteGW / ProxyGW Cache Flush Utility"
echo "  This will clear legacy/dirty DNS cache and OSPF routes."
echo "========================================================="

echo "[1/4] Stopping proxygw service to prevent DB locks..."
systemctl stop proxygw

DB_PATH="/root/proxygw/config/proxygw.db"
if [ ! -f "$DB_PATH" ]; then
    echo "Error: Database not found at $DB_PATH"
    exit 1
fi

echo "[2/4] Deleting DNS resolve cache (domain_resolve_cache)..."
sqlite3 "$DB_PATH" "DELETE FROM domain_resolve_cache;"

echo "[3/4] Deleting OSPF routes table (routes_table)..."
sqlite3 "$DB_PATH" "DELETE FROM routes_table;"

echo "[4/4] Deleting Geosite expand cache (geosite_expand_cache)..."
sqlite3 "$DB_PATH" "DELETE FROM geosite_expand_cache;"

echo "Restarting proxygw service to trigger a clean OSPF push..."
systemctl start proxygw

echo "========================================================="
echo "Done! The system will now safely re-resolve rules via SOCKS5."
echo "You can check OSPF routes using: vtysh -c 'show ip ospf route'"
