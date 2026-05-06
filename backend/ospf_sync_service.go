package main

import (
	"fmt"
	"log"
	"os"
)

func syncFRRConfig() {
	var mode string
	if db != nil {
		db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&mode)
	}

	if mode == "A" || mode == "" {
		sysCmd.run("vtysh", "-c", "conf t", "-c", "no route-map OSPF-EXPORT permit 10")
		sysCmd.run("systemctl", "stop", "frr")
		return
	}

	ip, subnet := getPrimaryLANIPAndSubnet()
	if ip == "" || subnet == "" {
		return
	}

	var newContent string
	if mode == "B" {
		newContent = fmt.Sprintf(`! FRR OSPF Config (Generated)
ip route 198.18.0.0/16 127.0.0.1 tag 100
router ospf
 ospf router-id %s
 redistribute static route-map OSPF-EXPORT
 network %s area 0
!
route-map OSPF-EXPORT permit 10
 match tag 100
!`, ip, subnet)
	} else if mode == "C" {
		newContent = fmt.Sprintf(`! FRR OSPF Config (Generated)
router ospf
 ospf router-id %s
 redistribute static route-map OSPF-EXPORT
 network %s area 0
!
route-map OSPF-EXPORT permit 10
 match tag 100
!`, ip, subnet)
	}

	b, _ := os.ReadFile("/etc/frr/frr.conf")
	content_frr := string(b)

	configChanged := newContent != content_frr
	if configChanged {
		log.Printf("[OSPF] Auto-updating FRR config: mode=%s, router-id=%s, network=%s", mode, ip, subnet)
		os.WriteFile(getPath("core", "frr", "frr.conf"), []byte(newContent), 0644)
		os.WriteFile("/etc/frr/frr.conf", []byte(newContent), 0644)
	}

	// Mode B/C depend on FRR at runtime.  A reboot or package update can leave
	// frr.service disabled/stopped while /etc/frr/frr.conf is already current;
	// in that case the old code skipped the restart path and OSPF publishing kept
	// failing with "vtysh: failed to connect to any daemons".
	sysCmd.run("sed", "-i", "s/ospfd=no/ospfd=yes/", "/etc/frr/daemons")
	_ = sysCmd.run("systemctl", "enable", "frr")
	if configChanged {
		sysCmd.run("systemctl", "restart", "frr")
		db.Exec("UPDATE routes_table SET status='candidate' WHERE status='published'")
	} else if sysCmd.run("systemctl", "is-active", "--quiet", "frr") != nil {
		log.Printf("[OSPF] FRR config unchanged but service inactive; starting frr for mode=%s", mode)
		sysCmd.run("systemctl", "start", "frr")
	}
}

func scheduleStaticRouteSync(mode string) {
	if mode != "B" && mode != "C" {
		return
	}
	staticRouteSyncMu.Lock()
	staticRouteSyncPending = true
	if staticRouteSyncRunning {
		staticRouteSyncMu.Unlock()
		return
	}
	staticRouteSyncRunning = true
	staticRouteSyncMu.Unlock()

	go func() {
		for {
			staticRouteSyncMu.Lock()
			staticRouteSyncPending = false
			staticRouteSyncMu.Unlock()

			currentMode := mode
			if db != nil {
				var dbMode string
				if err := db.QueryRow("SELECT value FROM settings WHERE key='mode'").Scan(&dbMode); err == nil && dbMode != "" {
					currentMode = dbMode
				}
			}
			if currentMode == "B" || currentMode == "C" {
				syncStaticRoutesToOSPFFunc(currentMode)
			}

			staticRouteSyncMu.Lock()
			if !staticRouteSyncPending {
				staticRouteSyncRunning = false
				staticRouteSyncMu.Unlock()
				return
			}
			staticRouteSyncMu.Unlock()
		}
	}()
}

func syncStaticRoutesToOSPF(mode string) {
	protected := collectProtectedRouteKeys()
	staticRoutes, conflicts := collectStaticRoutesForMode(mode, protected)
	staticRoutes, pruned := pruneStaticRoutesPreferBroad(staticRoutes)
	if pruned > 0 {
		log.Printf("[OSPF] pruned %d narrower routes covered by broader prefixes", pruned)
	}
	if len(conflicts) > 0 {
		log.Printf("[OSPF] skipped %d protected endpoint routes to avoid loop, samples=%s", len(conflicts), sampleRouteKeys(conflicts, 10))
	}

	var toDelete []string
	oldRows, err := db.Query("SELECT ip FROM routes_table WHERE source='static'")
	if err == nil {
		for oldRows.Next() {
			var ip string
			if err := oldRows.Scan(&ip); err == nil {
				if _, ok := staticRoutes[ip]; !ok {
					toDelete = append(toDelete, ip)
				}
			}
		}
		oldRows.Close()
	}

	txSync, err := db.Begin()
	if err != nil {
		log.Printf("[WARN] syncStaticRoutesToOSPF begin tx failed: %v", err)
		return
	}
	for _, ipStr := range toDelete {
		if _, err := txSync.Exec("UPDATE routes_table SET miss_count=99, ttl=0, last_seen=datetime('now', '-1 hour') WHERE ip=?", ipStr); err != nil {
			_ = txSync.Rollback()
			log.Printf("[WARN] syncStaticRoutesToOSPF mark stale route failed: ip=%s err=%v", ipStr, err)
			return
		}
	}

	for ipStr, state := range staticRoutes {
		domain := state.domain
		if domain == "" {
			domain = "static_rule"
		}
		if _, err := txSync.Exec("INSERT INTO routes_table (ip, domain, source, first_seen, last_seen, ttl, status, miss_count) VALUES (?, ?, 'static', datetime('now', '-61 seconds'), datetime('now'), ?, 'candidate', 0) ON CONFLICT(ip) DO UPDATE SET domain=excluded.domain, source='static', ttl=excluded.ttl, miss_count=0, last_seen=datetime('now')", ipStr, domain, state.ttl); err != nil {
			_ = txSync.Rollback()
			log.Printf("[WARN] syncStaticRoutesToOSPF upsert route failed: ip=%s err=%v", ipStr, err)
			return
		}
	}
	if err := txSync.Commit(); err != nil {
		_ = txSync.Rollback()
		log.Printf("[WARN] syncStaticRoutesToOSPF commit failed: %v", err)
		return
	}
}
