#!/bin/bash
# flush_cache.sh — clear legacy/dirty DNS cache and OSPF routes.
set -euo pipefail

DB_PATH="${1:-/root/proxygw/config/proxygw.db}"

echo "========================================================="
echo "  EdgeRouteGW / ProxyGW Cache Flush Utility"
echo "  This will clear legacy/dirty DNS cache and OSPF routes."
echo "  Database: $DB_PATH"
echo "========================================================="

# Every precondition is checked before the service is touched. This used to stop
# proxygw first and only then look for the database, so running it against a
# non-default install left the gateway stopped and offline while reporting
# nothing worse than "database not found".
if [ ! -f "$DB_PATH" ]; then
    echo "Error: Database not found at $DB_PATH" >&2
    echo "Pass the database path as the first argument for non-default installs." >&2
    exit 1
fi
if ! command -v sqlite3 >/dev/null 2>&1; then
    echo "Error: sqlite3 not found in PATH" >&2
    exit 1
fi

if [ -t 0 ]; then
    read -r -p "This deletes cached DNS results, OSPF routes and geosite expansions. Continue? [y/N]: " answer
    case "$answer" in
        [Yy]*) ;;
        *) echo "Aborted."; exit 0 ;;
    esac
fi

# From here on the service is down, so any early exit must bring it back up
# rather than leave the gateway offline.
service_stopped=0
restore_service() {
    if [ "$service_stopped" -eq 1 ]; then
        echo "Restarting proxygw service..."
        systemctl start proxygw || echo "Warning: failed to restart proxygw -- start it manually." >&2
    fi
}
trap restore_service EXIT

echo "[1/4] Stopping proxygw service to prevent DB locks..."
systemctl stop proxygw
service_stopped=1

echo "[2/4] Deleting DNS resolve cache (domain_resolve_cache)..."
sqlite3 "$DB_PATH" "DELETE FROM domain_resolve_cache;"

echo "[3/4] Deleting OSPF routes table (routes_table)..."
sqlite3 "$DB_PATH" "DELETE FROM routes_table;"

echo "[4/4] Deleting Geosite expand cache (geosite_expand_cache)..."
sqlite3 "$DB_PATH" "DELETE FROM geosite_expand_cache;"

echo "========================================================="
echo "Done! The system will now safely re-resolve rules via SOCKS5."
echo "You can check OSPF routes using: vtysh -c 'show ip ospf route'"
