package main

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
)

func initDB() {
	var err error
	db, err = sql.Open("sqlite3", getPath("config", "proxygw.db"))
	if err != nil {
		log.Fatal(err)
	}

	// Enable WAL mode for high concurrency
	db.Exec("PRAGMA journal_mode=WAL;")
	db.Exec("PRAGMA synchronous=NORMAL;")
	db.Exec("PRAGMA busy_timeout=5000;")

	tables := []string{

		"CREATE TABLE IF NOT EXISTS remote_nodes (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, type TEXT, ssh_host TEXT, ssh_port INTEGER, ssh_user TEXT, ssh_auth_type TEXT, ssh_credential TEXT, ssh_host_key TEXT, region TEXT, status TEXT, remark TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP);",
		"CREATE TABLE IF NOT EXISTS remote_node_wg (node_id INTEGER PRIMARY KEY, server_priv TEXT, server_pub TEXT, client_priv TEXT, client_pub TEXT, endpoint TEXT, port INTEGER, tunnel_addr TEXT, client_addr TEXT);",
		"CREATE TABLE IF NOT EXISTS remote_node_vless (node_id INTEGER PRIMARY KEY, uuid TEXT, reality_priv TEXT, reality_pub TEXT, short_id TEXT, server_name TEXT, dest TEXT, port INTEGER, share_link TEXT);",
		"CREATE TABLE IF NOT EXISTS remote_node_logs (id INTEGER PRIMARY KEY AUTOINCREMENT, node_id INTEGER, action TEXT, status TEXT, log_text TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);",

		`CREATE TABLE IF NOT EXISTS routes_table (
			ip TEXT PRIMARY KEY, domain TEXT, source TEXT,
			first_seen DATETIME, last_seen DATETIME, ttl INTEGER, status TEXT, miss_count INTEGER DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS nodes (
			id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, grp TEXT, type TEXT, address TEXT, port INTEGER, uuid TEXT, active BOOLEAN DEFAULT 1, ping INTEGER DEFAULT 0
		);`,
		`CREATE TABLE IF NOT EXISTS rules (
			id INTEGER PRIMARY KEY AUTOINCREMENT, type TEXT, value TEXT, policy TEXT, priority INTEGER NOT NULL DEFAULT 0, group_id TEXT NOT NULL DEFAULT '', group_name TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT);`,
		`CREATE TABLE IF NOT EXISTS lan_acls (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT,
			value TEXT,
			policy TEXT,
			remark TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS geosite_expand_cache (
			tag TEXT NOT NULL,
			geodata_ver TEXT NOT NULL,
			domains_json TEXT NOT NULL,
			skipped_count INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (tag, geodata_ver)
		);`,
		`CREATE TABLE IF NOT EXISTS domain_resolve_cache (
			domain TEXT PRIMARY KEY,
			ips_json TEXT NOT NULL,
			dns_ttl INTEGER NOT NULL DEFAULT 300,
			resolved_at DATETIME NOT NULL,
			expire_at DATETIME NOT NULL,
			last_error TEXT NOT NULL DEFAULT '',
			fail_count INTEGER NOT NULL DEFAULT 0,
			geodata_ver TEXT NOT NULL DEFAULT ''
		);`,
	}
	for _, t := range tables {
		if _, err := db.Exec(t); err != nil {
			log.Fatalf("[FATAL] failed to create table: %v", err)
		}
	}
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS protected_ips (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT NOT NULL UNIQUE, remark TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)"); err != nil {
		log.Fatalf("[FATAL] failed to create protected_ips table: %v", err)
	}
	ensureGatewayEventTable()

	if _, err := db.Exec("ALTER TABLE nodes ADD COLUMN params TEXT DEFAULT '{}'"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		log.Printf("[WARN] ALTER TABLE failed: %v", err)
	}

	if _, err := db.Exec("ALTER TABLE remote_nodes ADD COLUMN ssh_host_key TEXT DEFAULT ''"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		log.Printf("[WARN] ALTER TABLE failed: %v", err)
	}
	if _, err := db.Exec("ALTER TABLE nodes ADD COLUMN ping INTEGER DEFAULT 0"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		log.Printf("[WARN] ALTER TABLE failed: %v", err)
	}
	if _, err := db.Exec("ALTER TABLE routes_table ADD COLUMN miss_count INTEGER DEFAULT 0"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		log.Printf("[WARN] ALTER TABLE failed: %v", err)
	}
	if _, err := db.Exec("ALTER TABLE rules ADD COLUMN priority INTEGER NOT NULL DEFAULT 0"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		log.Printf("[WARN] ALTER TABLE failed: %v", err)
	}
	if _, err := db.Exec("UPDATE rules SET priority=id WHERE priority=0"); err != nil {
		log.Printf("[WARN] rules priority backfill failed: %v", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_rules_priority_id ON rules(priority ASC, id ASC)"); err != nil {
		log.Printf("[WARN] create rules priority index failed: %v", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_routes_status_firstseen ON routes_table(status, first_seen)"); err != nil {
		log.Printf("[WARN] create routes status+first_seen index failed: %v", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_routes_status_misscount ON routes_table(status, miss_count)"); err != nil {
		log.Printf("[WARN] create routes status+miss_count index failed: %v", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_gateway_events_ts ON gateway_events(ts)"); err != nil {
		log.Printf("[WARN] create gateway_events ts index failed: %v", err)
	}
	if _, err := db.Exec("CREATE INDEX IF NOT EXISTS idx_node_traffic_history_ts_node ON node_traffic_history(ts, node_id)"); err != nil {
		log.Printf("[WARN] create node_traffic_history ts+node_id index failed: %v", err)
	}
	if _, err := db.Exec("ALTER TABLE rules ADD COLUMN group_id TEXT NOT NULL DEFAULT ''"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		log.Printf("[WARN] ALTER TABLE failed: %v", err)
	}
	if _, err := db.Exec("ALTER TABLE rules ADD COLUMN group_name TEXT NOT NULL DEFAULT ''"); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		log.Printf("[WARN] ALTER TABLE failed: %v", err)
	}

	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('mode', 'B')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('dns_local', '119.29.29.29,223.5.5.5')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('dns_remote', '1.1.1.1,8.8.8.8')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('dns_lazy', 'true')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('dns_mode', 'smart')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('cron_enabled', 'true')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('cron_time', '04:00')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('cron_schedule_type', 'daily')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('cron_weekday', '1')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('cron_monthday', '1')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('ospf_push_batch_limit', '500')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('ospf_push_interval_seconds', '10')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('ospf_resolve_workers', '16')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}
	if _, err := db.Exec("INSERT OR IGNORE INTO settings (key, value) VALUES ('lan_default_policy', 'proxy')"); err != nil {
		log.Printf("[WARN] default data insert failed: %v", err)
	}

	purgeDirtyRoutesTable()
	db.Exec("UPDATE routes_table SET status='candidate' WHERE status='published'")

	migrateLegacyCredentialsIfNeeded()
	ensurePasswordInitialized()
}

func purgeDirtyRoutesTable() {
	rows, err := db.Query("SELECT ip FROM routes_table")
	if err != nil {
		log.Printf("[WARN] purge dirty routes query failed: %v", err)
		return
	}
	defer rows.Close()

	var dirty []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			continue
		}
		if _, ok := normalizeRouteKey(ip); !ok {
			dirty = append(dirty, ip)
		}
	}
	if len(dirty) == 0 {
		return
	}

	tx, err := db.Begin()
	if err != nil {
		log.Printf("[WARN] purge dirty routes begin tx failed: %v", err)
		return
	}
	for _, ip := range dirty {
		if _, err := tx.Exec("DELETE FROM routes_table WHERE ip=?", ip); err != nil {
			_ = tx.Rollback()
			log.Printf("[WARN] purge dirty route delete failed for %q: %v", ip, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[WARN] purge dirty routes commit failed: %v", err)
		return
	}
	log.Printf("[OSPF] purged %d dirty routes from routes_table", len(dirty))
}

func ensurePasswordInitialized() {
	var pwdHash, legacyPwd string
	err := db.QueryRow("SELECT value FROM settings WHERE key='password_hash'").Scan(&pwdHash)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] get password_hash err: %v", err)
	}
	err = db.QueryRow("SELECT value FROM settings WHERE key='password'").Scan(&legacyPwd)
	if err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] get legacy pwd err: %v", err)
	}
	if strings.TrimSpace(pwdHash) != "" || strings.TrimSpace(legacyPwd) != "" {
		return
	}

	bootstrap := strings.TrimSpace(os.Getenv("PROXYGW_BOOTSTRAP_PASSWORD"))
	generated := false
	if bootstrap == "" {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err == nil {
			bootstrap = fmt.Sprintf("%x", b)
			generated = true
		}
	}
	if strings.TrimSpace(bootstrap) == "" {
		log.Println("[SECURITY] password bootstrap failed: empty bootstrap password")
		return
	}

	hash, err := hashPassword(bootstrap)
	if err != nil {
		log.Printf("[SECURITY] password bootstrap hash failed: %v", err)
		return
	}
	if _, err = db.Exec("INSERT OR REPLACE INTO settings (key, value) VALUES ('password_hash', ?)", hash); err != nil {
		log.Printf("[SECURITY] password bootstrap db write failed: %v", err)
		return
	}

	if generated {
		bootstrapPath := getPath("config", "bootstrap_password.txt")
		if err := os.WriteFile(bootstrapPath, []byte(bootstrap+"\n"), 0600); err != nil {
			log.Printf("[SECURITY] initialized random bootstrap password (save failed: %v)", err)
		} else {
			log.Printf("[SECURITY] initialized random bootstrap password, saved to %s (change it immediately)", bootstrapPath)
		}
	} else {
		log.Println("[SECURITY] initialized password from PROXYGW_BOOTSTRAP_PASSWORD")
	}
}
