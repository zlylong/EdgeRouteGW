package main

import (
	"log"
	"os"
	"time"
)

// maintenanceInitialDelay keeps the first prune away from the startup burst.
var maintenanceInitialDelay = 5 * time.Minute

func runDatabaseMaintenance() {
	initial := time.NewTimer(maintenanceInitialDelay)
	<-initial.C

	maintenanceTicker := time.NewTicker(24 * time.Hour)
	defer maintenanceTicker.Stop()

	// Run once on startup
	performDBPruning()

	for range maintenanceTicker.C {
		performDBPruning()
	}
}

func rotateLogs() {
	logPaths := []string{
		getPath("core", "mosdns", "mosdns.log"),
		xrayAccessLogPath,
		xrayErrorLogPath,
	}

	const maxLogSize = 50 * 1024 * 1024 // 50MB

	for _, p := range logPaths {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if fi.Size() > maxLogSize {
			log.Printf("[MAINTENANCE] Truncating large log file: %s (%d bytes)", p, fi.Size())
			if err := os.Truncate(p, 0); err != nil {
				log.Printf("[MAINTENANCE] Failed to truncate %s: %v", p, err)
			}
		}
	}
}

type dbPruneQuery struct {
	name string
	sql  string
}

// dbPruneQueries is the retention policy applied by the daily maintenance job.
// It is a package variable so tests can run a single entry against a seeded DB.
var dbPruneQueries = []dbPruneQuery{
	{
		// expire_at is written as a Unix timestamp (see resolveDomainIPv4Cached),
		// not as a datetime string. Comparing it with datetime('now') compared
		// an INTEGER with TEXT, and in SQLite every integer sorts before every
		// string, so the predicate matched every row and this job wiped the
		// whole resolve cache once a day. CAST both sides to integers; legacy
		// rows that hold a numeric string cast the same way.
		name: "Expired DNS Cache",
		sql:  "DELETE FROM domain_resolve_cache WHERE CAST(expire_at AS INTEGER) < CAST(strftime('%s','now') AS INTEGER)",
	},
	{
		name: "API Audit Logs (7d)",
		sql:  "DELETE FROM gateway_events WHERE module = 'api' AND ts < datetime('now', '-7 days')",
	},
	{
		name: "System Events (30d)",
		sql:  "DELETE FROM gateway_events WHERE ts < datetime('now', '-30 days')",
	},
	{
		name: "Detailed Traffic History (60d)",
		sql:  "DELETE FROM traffic_history WHERE ts < datetime('now', '-60 days')",
	},
	{
		name: "Node Traffic History (60d)",
		sql:  "DELETE FROM node_traffic_history WHERE ts < datetime('now', '-60 days')",
	},
	{
		name: "Remote Node Logs (30d)",
		sql:  "DELETE FROM remote_node_logs WHERE created_at < datetime('now', '-30 days')",
	},
	{
		name: "Remote Node History (30d)",
		sql:  "DELETE FROM remote_node_history WHERE created_at < datetime('now', '-30 days')",
	},
	{
		name: "Geosite Expand Cache (30d)",
		sql:  "DELETE FROM geosite_expand_cache WHERE updated_at < datetime('now', '-30 days')",
	},
	{
		name: "Domain GeoIP Locks (30d)",
		sql:  "DELETE FROM domain_geoip_lock WHERE updated_at < datetime('now', '-30 days')",
	},
}

func performDBPruning() {
	log.Println("[MAINTENANCE] Starting database pruning...")

	// Log Rotation / Truncation
	rotateLogs()

	for _, q := range dbPruneQueries {
		res, err := getDB().Exec(q.sql)
		if err != nil {
			log.Printf("[MAINTENANCE] Failed to prune %s: %v", q.name, err)
			continue
		}
		rows, _ := res.RowsAffected()
		if rows > 0 {
			log.Printf("[MAINTENANCE] Pruned %s: %d rows removed", q.name, rows)
		}
	}

	// Optimize the database occasionally
	if time.Now().Weekday() == time.Sunday {
		log.Println("[MAINTENANCE] Performing weekly database optimization (VACUUM/ANALYZE)...")
		if _, err := getDB().Exec("VACUUM"); err != nil {
			log.Printf("[MAINTENANCE] VACUUM failed: %v", err)
		}
		if _, err := getDB().Exec("ANALYZE"); err != nil {
			log.Printf("[MAINTENANCE] ANALYZE failed: %v", err)
		}
	}

	log.Println("[MAINTENANCE] Database pruning completed.")
}
