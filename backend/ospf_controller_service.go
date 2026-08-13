package main

import (
	"database/sql"
	"log"
	"time"
)

func ospfController() {
	var lastUpdate time.Time
	var lastReconcile time.Time
	modeDemotedForNonBC := false

	for {
		time.Sleep(2 * time.Second)
		settings := getOspfControllerSettings()
		coolingTime := time.Duration(settings.PushIntervalSeconds) * time.Second
		var mode string
		if err := getDB().QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil && err != sql.ErrNoRows {
			log.Printf("[WARN] SELECT value FROM settings WHERE key='mode' err: %v", err)
		}
		if mode != "C" && mode != "B" {
			if !modeDemotedForNonBC {
				if _, err := getDB().Exec("UPDATE routes_table SET status='candidate' WHERE status='published'"); err != nil {
					log.Printf("[WARN] demote published routes to candidate failed: %v", err)
				}
				modeDemotedForNonBC = true
			}
			continue
		}
		modeDemotedForNonBC = false
		if lastReconcile.IsZero() || time.Since(lastReconcile) >= defaultOspfReconcileInterval {
			reconcilePublishedRoutesWithFRR()
			lastReconcile = time.Now()
		}

		if time.Since(lastUpdate) < coolingTime {
			continue
		}
		updated := false

		getDB().Exec("UPDATE routes_table SET miss_count = miss_count + 1 WHERE status='published' AND datetime(last_seen, '+' || ttl || ' seconds') < datetime('now')")

		var toDel []string
		rowsDel, err := getDB().Query("SELECT ip FROM routes_table WHERE status='published' AND miss_count >= 3 LIMIT ?", settings.PushBatchLimit)
		if err == nil {
			for rowsDel.Next() {
				var ip string
				if err := rowsDel.Scan(&ip); err == nil {
					toDel = append(toDel, ip)
				}
			}
			if err := rowsDel.Err(); err != nil {
				log.Printf("[WARN] rowsDel err: %v", err)
			}
			rowsDel.Close()
		} else {
			log.Printf("[WARN] query rowsDel err: %v", err)
		}

		log.Printf("[DEBUG] toDel len = %d", len(toDel))
		if applyOspfDeleteBatch(toDel) {
			updated = true
		}

		var toAdd []string
		rowsAdd, err := getDB().Query("SELECT ip FROM routes_table WHERE status='candidate' AND first_seen <= datetime('now', '-60 seconds') LIMIT ?", settings.PushBatchLimit)
		if err == nil {
			for rowsAdd.Next() {
				var ip string
				if err := rowsAdd.Scan(&ip); err == nil {
					toAdd = append(toAdd, ip)
				}
			}
			if err := rowsAdd.Err(); err != nil {
				log.Printf("[WARN] rowsAdd err: %v", err)
			}
			rowsAdd.Close()
		} else {
			log.Printf("[WARN] query rowsAdd err: %v", err)
		}

		if applyOspfAddBatch(toAdd) {
			updated = true
		}

		if updated {
			lastUpdate = time.Now()
		}
	}
}
