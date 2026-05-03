package main

import (
	"log"
	"time"
)

func runDatabaseMaintenance() {
	// Wait a bit after startup to avoid contention
	time.Sleep(5 * time.Minute)

	maintenanceTicker := time.NewTicker(24 * time.Hour)
	defer maintenanceTicker.Stop()

	// Run once on startup
	performDBPruning()

	for range maintenanceTicker.C {
		performDBPruning()
	}
}

func performDBPruning() {
	log.Println("[MAINTENANCE] Starting database pruning...")

	queries := []struct {
		name string
		sql  string
	}{
		{
			name: "Expired DNS Cache",
			sql:  "DELETE FROM domain_resolve_cache WHERE expire_at < datetime('now')",
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

	for _, q := range queries {
		res, err := db.Exec(q.sql)
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
		if _, err := db.Exec("VACUUM"); err != nil {
			log.Printf("[MAINTENANCE] VACUUM failed: %v", err)
		}
		if _, err := db.Exec("ANALYZE"); err != nil {
			log.Printf("[MAINTENANCE] ANALYZE failed: %v", err)
		}
	}

	log.Println("[MAINTENANCE] Database pruning completed.")
}
