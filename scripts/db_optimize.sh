#!/usr/bin/env bash
set -euo pipefail

DB_PATH="${1:-/root/proxygw/config/proxygw.db}"
# Default to the safe mode. --full takes a VACUUM, which holds a write lock for
# the whole run; on a live gateway that stalls every DNS resolve and OSPF push
# behind it. Taking the disruptive path must be an explicit request.
MODE="${2:---index-only}" # --index-only | --full

case "$MODE" in
  --index-only|--full) ;;
  *)
    echo "[ERROR] Unknown mode: $MODE (expected --index-only or --full)" >&2
    exit 1
    ;;
esac

if [[ ! -f "$DB_PATH" ]]; then
  echo "[ERROR] DB not found: $DB_PATH" >&2
  exit 1
fi

if ! command -v sqlite3 >/dev/null 2>&1; then
  echo "[ERROR] sqlite3 not found in PATH" >&2
  exit 1
fi

# How many timestamped backups to keep. install.sh and update.sh run this on
# every deploy, so an unpruned backup per run left a full copy of the database
# behind each time -- on a long-lived gateway proxygw.db reaches hundreds of MB
# and the copies were never reclaimed.
BACKUP_KEEP="${DB_OPTIMIZE_BACKUP_KEEP:-3}"

TS="$(date +%Y%m%d_%H%M%S)"
# update.sh runs this right after restarting the backend, which is then busy
# re-encrypting credentials, applying configs and writing traffic samples. With
# sqlite3's default busy timeout of 0 the first CREATE INDEX lost that race and
# died with "database is locked", so the indexes were silently never built.
# Wait for the writer instead.
sq() { sqlite3 -cmd ".timeout 20000" "$DB_PATH" "$@"; }

BACKUP="${DB_PATH}.bak.${TS}"

prune_old_backups() {
  local keep="$1"
  local victims
  # Newest first; everything past the keep count goes.
  victims=$(ls -1t "${DB_PATH}".bak.* 2>/dev/null | tail -n +"$((keep + 1))" || true)
  if [[ -n "$victims" ]]; then
    while IFS= read -r old; do
      [[ -n "$old" ]] || continue
      echo "[INFO] Removing old backup: $old"
      rm -f "$old"
    done <<< "$victims"
  fi
}

echo "[INFO] DB: $DB_PATH"
echo "[INFO] Mode: $MODE"

printf '\n[STEP] Before snapshot\n'
stat -c '%n %s bytes' "$DB_PATH"
sq "PRAGMA page_size; PRAGMA page_count; PRAGMA freelist_count; PRAGMA journal_mode; PRAGMA synchronous; PRAGMA auto_vacuum;" \
  | awk 'NR==1{print "page_size=" $1} NR==2{print "page_count=" $1} NR==3{print "freelist_count=" $1} NR==4{print "journal_mode=" $1} NR==5{print "synchronous=" $1} NR==6{print "auto_vacuum=" $1}'

printf '\n[STEP] Backup\n'
# The database runs in WAL mode and the backend keeps writing to it. A plain
# cp copies the main file without the -wal contents, i.e. a backup that is
# missing the most recent transactions and may not even be consistent. Use
# the online backup API; fall back to cp only if that is unavailable.
if ! sq ".backup '$BACKUP'" 2>/dev/null || [ ! -s "$BACKUP" ]; then
  echo "[WARN] sqlite3 .backup failed, falling back to file copy"
  cp -a "$DB_PATH" "$BACKUP"
fi
stat -c '%n %s bytes' "$BACKUP"
prune_old_backups "$BACKUP_KEEP"

printf '\n[STEP] Create/refresh indexes\n'
# domain_geoip_lock is created lazily by the backend (ospf_geoip_lock.go) the
# first time Mode C locks a GeoIP tag. On a host that has never done that it
# does not exist, and the CREATE INDEX for it fails with "no such table".
# sqlite3 carries on, so the other index was still built — but every upgrade
# printed a Parse error for something that is not a problem. Only touch it
# when it is there.
table_exists() {
  [ "$(sq "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='$1';")" = "1" ]
}
if table_exists domain_geoip_lock; then
  sq "CREATE INDEX IF NOT EXISTS idx_dgl_domain_resolver_ver ON domain_geoip_lock(domain, resolver_group, geodata_ver);"
else
  echo "[SKIP] domain_geoip_lock not present yet (created on first Mode C GeoIP lock); its index is deferred"
fi
sq "CREATE INDEX IF NOT EXISTS idx_gateway_events_module_level_id ON gateway_events(module, level, id DESC);"
sq "ANALYZE; PRAGMA optimize;"

if [[ "$MODE" == "--full" ]]; then
  printf '\n[STEP] VACUUM (may take time and hold write lock)\n'
  sq "VACUUM;"
  sq "ANALYZE; PRAGMA optimize;"
fi

printf '\n[STEP] After snapshot\n'
stat -c '%n %s bytes' "$DB_PATH"
sq "PRAGMA page_size; PRAGMA page_count; PRAGMA freelist_count; PRAGMA journal_mode; PRAGMA synchronous; PRAGMA auto_vacuum;" \
  | awk 'NR==1{print "page_size=" $1} NR==2{print "page_count=" $1} NR==3{print "freelist_count=" $1} NR==4{print "journal_mode=" $1} NR==5{print "synchronous=" $1} NR==6{print "auto_vacuum=" $1}'

printf '\n[STEP] Query plan check\n'
if table_exists domain_geoip_lock; then
  sq "EXPLAIN QUERY PLAN SELECT geoip_tag FROM domain_geoip_lock WHERE domain='example.com' AND resolver_group='direct' AND geodata_ver='v1';"
fi
sq "EXPLAIN QUERY PLAN SELECT id,module,level,ts FROM gateway_events WHERE module='ospf' AND level='info' ORDER BY id DESC LIMIT 50;"

printf '\n[DONE] Backup: %s\n' "$BACKUP"
