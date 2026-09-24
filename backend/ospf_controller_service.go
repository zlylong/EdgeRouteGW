package main

import (
	"database/sql"
	"log"
	"time"
)

// ospfControllerState carries the timers of the publish loop between ticks so
// that a single tick can be driven from tests.
type ospfControllerState struct {
	lastUpdate          time.Time
	lastReconcile       time.Time
	modeDemotedForNonBC bool
}

func ospfController() {
	st := &ospfControllerState{}
	for {
		time.Sleep(2 * time.Second)
		ospfControllerTick(st)
	}
}

// ospfControllerTick runs one iteration of the publish loop. Outside Mode B/C it
// only demotes published routes once and returns without touching the settings
// table, so an idle Mode A gateway costs a single SELECT every two seconds.
// Inside B/C the miss-count sweep and the add/delete batches run at most once
// per push_interval_seconds; earlier versions only armed that interval after a
// change, so an idle loop re-ran the sweep every two seconds.
func ospfControllerTick(st *ospfControllerState) {
	var mode string
	if err := getDB().QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode); err != nil && err != sql.ErrNoRows {
		log.Printf("[WARN] SELECT value FROM settings WHERE key='mode' err: %v", err)
	}
	if mode != "C" && mode != "B" {
		if !st.modeDemotedForNonBC {
			if _, err := getDB().Exec("UPDATE routes_table SET status='candidate' WHERE status='published'"); err != nil {
				log.Printf("[WARN] demote published routes to candidate failed: %v", err)
			}
			st.modeDemotedForNonBC = true
		}
		return
	}
	st.modeDemotedForNonBC = false

	if st.lastReconcile.IsZero() || time.Since(st.lastReconcile) >= defaultOspfReconcileInterval {
		reconcilePublishedRoutesWithFRR()
		st.lastReconcile = time.Now()
	}

	settings := getOspfControllerSettings()
	coolingTime := time.Duration(settings.PushIntervalSeconds) * time.Second
	if !st.lastUpdate.IsZero() && time.Since(st.lastUpdate) < coolingTime {
		return
	}
	st.lastUpdate = time.Now()

	if _, err := getDB().Exec("UPDATE routes_table SET miss_count = miss_count + 1 WHERE status='published' AND datetime(last_seen, '+' || ttl || ' seconds') < datetime('now')"); err != nil {
		log.Printf("[WARN] routes_table miss_count sweep failed: %v", err)
	}

	toDel := queryRouteIPs("SELECT ip FROM routes_table WHERE status='published' AND miss_count >= 3 LIMIT ?", settings.PushBatchLimit)
	if len(toDel) > 0 {
		log.Printf("[OSPF] %d published route(s) expired, withdrawing", len(toDel))
	}
	applyOspfDeleteBatch(toDel)

	toAdd := queryRouteIPs("SELECT ip FROM routes_table WHERE status='candidate' AND first_seen <= datetime('now', '-60 seconds') LIMIT ?", settings.PushBatchLimit)
	applyOspfAddBatch(toAdd)
}

func queryRouteIPs(query string, limit int) []string {
	rows, err := getDB().Query(query, limit)
	if err != nil {
		log.Printf("[WARN] routes_table query failed: %v", err)
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err == nil {
			out = append(out, ip)
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("[WARN] routes_table rows err: %v", err)
	}
	return out
}
