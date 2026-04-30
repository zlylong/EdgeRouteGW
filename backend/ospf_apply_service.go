package main

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"time"
)

func contains(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}

func applyOspfDeleteBatch(toDel []string) bool {
	if len(toDel) == 0 {
		return false
	}
	var buf bytes.Buffer
	for _, ip := range toDel {
		addOspfLog("[DEL] " + ip + " (Miss count >= 3)")
		routeStr := formatRouteCIDR(ip)
		if routeStr == "" {
			continue
		}
		buf.WriteString(fmt.Sprintf("no ip route %s 127.0.0.1 tag 100\n", routeStr))
	}
	out, err := runVtyshConfigBatch(buf.String())
	if err != nil {
		log.Printf("[FRR] DEL batch=%d apply_failed: %v, out=%q", len(toDel), err, strings.TrimSpace(out))
		return false
	}
	tx, _ := db.Begin()
	for _, ip := range toDel {
		tx.Exec("DELETE FROM routes_table WHERE ip=?", ip)
	}
	tx.Commit()
	log.Printf("[FRR] DEL batch=%d applied via vtysh", len(toDel))
	return true
}

func applyOspfAddBatch(toAdd []string) bool {
	if len(toAdd) == 0 {
		return false
	}
	allowlist := loadOspfPublishAllowlist()
	var buf bytes.Buffer
	allowed := make([]string, 0, len(toAdd))
	skipped := 0
	for _, ip := range toAdd {
		if err := validateAdvertisableCIDR(ip); err != nil {
			skipped++
			logGatewayEventThrottled("ospf_publish_policy_reject", 30*time.Second, "warn", "ospf", "publish_policy_reject", "OSPF publish route rejected by policy", map[string]interface{}{"route": ip, "reason": err.Error()})
			continue
		}
		if !routeAllowedByOspfPublishAllowlist(ip, allowlist) {
			skipped++
			logGatewayEventThrottled("ospf_publish_allowlist_reject", 30*time.Second, "warn", "ospf", "publish_allowlist_reject", "OSPF publish route rejected by allowlist", map[string]interface{}{"route": ip})
			continue
		}
		allowed = append(allowed, ip)
		addOspfLog("[ADD] " + ip + " to published_set")
		routeStr := formatRouteCIDR(ip)
		if routeStr == "" {
			continue
		}
		buf.WriteString(fmt.Sprintf("ip route %s 127.0.0.1 tag 100\n", routeStr))
	}
	if len(allowed) == 0 {
		if skipped > 0 {
			log.Printf("[FRR] ADD blocked by ospf publish allowlist: requested=%d skipped=%d", len(toAdd), skipped)
			// GC blocked candidates: if a candidate is blocked by policy/allowlist,
			// mark it as 'failed_policy' so it doesn't stay in 'candidate' forever.
			tx, _ := db.Begin()
			for _, ip := range toAdd {
				// We don't want to keep retrying these in every sync cycle
				tx.Exec("UPDATE routes_table SET status='failed_policy', miss_count=miss_count+1 WHERE ip=? AND status='candidate'", ip)
			}
			tx.Commit()
		}
		return false
	}
	out, err := runVtyshConfigBatch(buf.String())
	if err != nil {
		log.Printf("[FRR] ADD batch=%d apply_failed: %v, out=%q", len(allowed), err, strings.TrimSpace(out))
		return false
	}
	tx, _ := db.Begin()
	for _, ip := range allowed {
		tx.Exec("UPDATE routes_table SET status='published', last_seen=datetime('now'), miss_count=0 WHERE ip=?", ip)
	}
	// Also mark skipped ones in this batch if some were allowed
	for _, ip := range toAdd {
		if !contains(allowed, ip) {
			tx.Exec("UPDATE routes_table SET status='failed_policy', miss_count=miss_count+1 WHERE ip=? AND status='candidate'", ip)
		}
	}
	tx.Commit()
	if skipped > 0 {
		log.Printf("[FRR] ADD batch=%d applied via vtysh (allowlist_skipped=%d)", len(allowed), skipped)
	} else {
		log.Printf("[FRR] ADD batch=%d applied via vtysh", len(allowed))
	}
	return true
}
