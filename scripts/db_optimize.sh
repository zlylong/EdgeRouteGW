#!/usr/bin/env bash
set -euo pipefail

DB_PATH="${1:-/root/proxygw/config/proxygw.db}"
MODE="${2:---full}" # --index-only | --full

if [[ ! -f "$DB_PATH" ]]; then
  echo "[ERROR] DB not found: $DB_PATH" >&2
  exit 1
fi

TS="$(date +%Y%m%d_%H%M%S)"
BACKUP="${DB_PATH}.bak.${TS}"

echo "[INFO] DB: $DB_PATH"
echo "[INFO] Mode: $MODE"

echo "\n[STEP] Before snapshot"
stat -c '%n %s bytes' "$DB_PATH"
sqlite3 "$DB_PATH" "PRAGMA page_size; PRAGMA page_count; PRAGMA freelist_count; PRAGMA journal_mode; PRAGMA synchronous; PRAGMA auto_vacuum;" \
  | awk 'NR==1{print "page_size=" $1} NR==2{print "page_count=" $1} NR==3{print "freelist_count=" $1} NR==4{print "journal_mode=" $1} NR==5{print "synchronous=" $1} NR==6{print "auto_vacuum=" $1}'

echo "\n[STEP] Backup"
cp -a "$DB_PATH" "$BACKUP"
stat -c '%n %s bytes' "$BACKUP"

echo "\n[STEP] Create/refresh indexes"
sqlite3 "$DB_PATH" <<'SQL'
CREATE INDEX IF NOT EXISTS idx_dgl_domain_resolver_ver
ON domain_geoip_lock(domain, resolver_group, geodata_ver);

CREATE INDEX IF NOT EXISTS idx_gateway_events_module_level_id
ON gateway_events(module, level, id DESC);

ANALYZE;
PRAGMA optimize;
SQL

if [[ "$MODE" == "--full" ]]; then
  echo "\n[STEP] VACUUM (may take time and hold write lock)"
  sqlite3 "$DB_PATH" "VACUUM;"
  sqlite3 "$DB_PATH" "ANALYZE; PRAGMA optimize;"
fi

echo "\n[STEP] After snapshot"
stat -c '%n %s bytes' "$DB_PATH"
sqlite3 "$DB_PATH" "PRAGMA page_size; PRAGMA page_count; PRAGMA freelist_count; PRAGMA journal_mode; PRAGMA synchronous; PRAGMA auto_vacuum;" \
  | awk 'NR==1{print "page_size=" $1} NR==2{print "page_count=" $1} NR==3{print "freelist_count=" $1} NR==4{print "journal_mode=" $1} NR==5{print "synchronous=" $1} NR==6{print "auto_vacuum=" $1}'

echo "\n[STEP] Query plan check"
sqlite3 "$DB_PATH" "EXPLAIN QUERY PLAN SELECT geoip_tag FROM domain_geoip_lock WHERE domain='example.com' AND resolver_group='direct' AND geodata_ver='v1';"
sqlite3 "$DB_PATH" "EXPLAIN QUERY PLAN SELECT id,module,level,ts FROM gateway_events WHERE module='ospf' AND level='info' ORDER BY id DESC LIMIT 50;"

echo "\n[DONE] Backup: $BACKUP"
